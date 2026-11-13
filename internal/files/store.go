package filesvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

var fileRefPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var (
	ErrNotFound    = errors.New("附件不存在")
	ErrForbidden   = errors.New("当前聊天没有该附件的访问权限")
	ErrNotReady    = errors.New("附件尚未就绪")
	ErrLimit       = errors.New("文件大小或并发读取超限")
	ErrInvalidRef  = errors.New("无效文件引用")
	ErrInvalidName = errors.New("文件名不能为空或超过 255 个字符")
)

// nowSeconds 返回 Unix 秒浮点数，等价 Python time.time()
func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// Store 维护数据库索引与按 file_ref 命名的 blob 文件
//
// 落盘布局对齐 MaiBot store.py：正式文件 {root}/{file_ref}.blob、传输中 {root}/{file_ref}.part，
// 不做内容寻址去重——同一份内容在不同 file_ref 下各存一份，与 Python 侧行为一致。
type Store struct {
	db  *gorm.DB
	cfg Config
	// mu 串行化同一进程内的配额检查、传输写入与清理，对应 Python transaction() 的写锁语义
	mu sync.Mutex
}

// Config 返回存储配置快照
func (s *Store) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

// NewStore 创建文件存储；rootDir 通常落在媒体目录下的 files 子目录
func NewStore(db *gorm.DB, cfg Config) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	root := strings.TrimSpace(cfg.RootDir)
	if root == "" {
		return nil, fmt.Errorf("文件存储根目录不能为空")
	}
	blobRoot := filepath.Join(root, "files", "blobs")
	if err := os.MkdirAll(blobRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建文件 blob 目录失败: %w", err)
	}
	cfg.RootDir = blobRoot
	return &Store{db: db, cfg: cfg}, nil
}

// blobPath 返回附件的正式落盘路径 {file_ref}.blob
func (s *Store) blobPath(fileRef string) (string, error) {
	if !fileRefPattern.MatchString(fileRef) {
		return "", ErrInvalidRef
	}
	path := filepath.Join(s.cfg.RootDir, fileRef+".blob")
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("无效存储路径")
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("附件必须是普通文件")
	}
	return path, nil
}

// NormalizeName 清理展示用文件名
func NormalizeName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	var b strings.Builder
	for _, r := range name {
		if r >= ' ' && r != 0x7f {
			b.WriteRune(r)
		}
	}
	name = strings.TrimSpace(b.String())
	if name == "" || utf8.RuneCountInString(name) > 255 {
		return "", ErrInvalidName
	}
	return name, nil
}

func (s *Store) authorize(tx *gorm.DB, fileRef, sessionID string, admin bool) error {
	if admin {
		return nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return ErrForbidden
	}
	var count int64
	if err := tx.Model(&Binding{}).Where("file_ref = ? AND session_id = ?", fileRef, sessionID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return ErrForbidden
	}
	return nil
}

// Get 读取元数据并校验会话权限
func (s *Store) Get(fileRef, sessionID string, admin bool) (Asset, error) {
	if !fileRefPattern.MatchString(fileRef) {
		return Asset{}, ErrInvalidRef
	}
	var asset Asset
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&asset, "file_ref = ?", fileRef).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if asset.State == "deleted" {
			return ErrNotFound
		}
		return s.authorize(tx, fileRef, sessionID, admin)
	})
	return asset, err
}

// PublicView 返回可对外暴露的字段，键与类型对齐 MaiBot FileStore.public
func PublicView(asset Asset) map[string]any {
	return map[string]any{
		"file_ref":    asset.FileRef,
		"name":        asset.Name,
		"mime_type":   asset.MimeType,
		"size":        asset.Size,
		"sha256":      asset.SHA256,
		"origin":      asset.Origin,
		"state":       asset.State,
		"error":       asset.Error,
		"created_at":  asset.CreatedAt,
		"accessed_at": asset.AccessedAt,
	}
}

// CreateLocal 通过带配额预留的流式路径写入新附件并绑定会话
func (s *Store) CreateLocal(sessionID, name, mimeType string, content []byte) (Asset, error) {
	if int64(len(content)) > s.maxFileBytes() {
		return Asset{}, ErrLimit
	}
	asset, err := s.BeginUpload(sessionID, name, mimeType)
	if err != nil {
		return Asset{}, err
	}
	if err := s.Append(asset.FileRef, asset.TransferID, content); err != nil {
		_ = s.Fail(asset.FileRef, asset.TransferID, err.Error())
		return Asset{}, err
	}
	return s.Finish(asset.FileRef, asset.TransferID)
}

// Grant 由管理员转发入口建立目标会话绑定，普通读取仍需命中绑定
func (s *Store) Grant(fileRef, sessionID string) error {
	fileRef = strings.TrimSpace(fileRef)
	sessionID = strings.TrimSpace(sessionID)
	if !fileRefPattern.MatchString(fileRef) {
		return ErrInvalidRef
	}
	if sessionID == "" {
		return ErrForbidden
	}
	if _, err := s.Get(fileRef, "", true); err != nil {
		return err
	}
	now := nowSeconds()
	return s.db.Where("file_ref = ? AND session_id = ?", fileRef, sessionID).
		FirstOrCreate(&Binding{FileRef: fileRef, SessionID: sessionID, CreatedAt: now}).Error
}

// Touch 在授权校验后更新访问时间，对齐 MaiBot FileStore.touch
//
// 与 Python 一致：先按 file_ref + session_id（或 admin）校验归属，再写 accessed_at；
// 未绑定会话的调用方拿不到资产，避免仅凭 file_ref 就刷新访问时间。
func (s *Store) Touch(fileRef, sessionID string, admin bool) error {
	if _, err := s.Get(fileRef, sessionID, admin); err != nil {
		return err
	}
	return s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Update("accessed_at", nowSeconds()).Error
}

// OpenBlob 打开附件的只读句柄并校验长度
func (s *Store) OpenBlob(asset Asset) (*os.File, error) {
	if asset.State != "ready" {
		return nil, ErrNotReady
	}
	path, err := s.blobPath(asset.FileRef)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.Size() != asset.Size {
		file.Close()
		return nil, fmt.Errorf("附件长度与索引不一致")
	}
	return file, nil
}

// OpenReader 打开附件读句柄并计入 open_readers
func (s *Store) OpenReader(asset Asset) (*os.File, error) {
	file, err := s.OpenBlob(asset)
	if err != nil {
		return nil, err
	}
	err = s.db.Model(&Asset{}).Where("file_ref = ?", asset.FileRef).Updates(map[string]any{
		"open_readers": gorm.Expr("open_readers + 1"),
		"accessed_at":  nowSeconds(),
	}).Error
	if err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

// CloseReader 释放附件读句柄计数
func (s *Store) CloseReader(fileRef string) error {
	return s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"open_readers": gorm.Expr("CASE WHEN open_readers > 0 THEN open_readers - 1 ELSE 0 END"),
		"accessed_at":  nowSeconds(),
	}).Error
}

// CopyBlobRange 将指定区间写入 w
func CopyBlobRange(file *os.File, start, length int64, w io.Writer) error {
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(w, file, length)
	return err
}

// CopyBlobRangeContext 在 ctx 取消时停止复制，避免写超时后继续占用连接
func CopyBlobRangeContext(ctx context.Context, file *os.File, start, length int64, w io.Writer) error {
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	remaining := length
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n := int64(len(buf))
		if remaining < n {
			n = remaining
		}
		read, err := file.Read(buf[:n])
		if read > 0 {
			if _, writeErr := w.Write(buf[:read]); writeErr != nil {
				return writeErr
			}
			remaining -= int64(read)
		}
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}

func newFileRef() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hashFile 计算文件内容的 sha256 与字节数，对应 finish() 里的流式计算
func hashFile(path string) (string, int64, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer handle.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, handle)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}
