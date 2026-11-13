package filesvc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ErrBusy 表示附件正在传输或仍被读取
var ErrBusy = errors.New("附件正在传输，请在完成后清理")

// ErrNotCache 表示本地原件不能被当作平台缓存清理
var ErrNotCache = errors.New("本地上传和生成的原件不能作为缓存清理")

// transferChunkSize 是流式读写磁盘的缓冲区大小，对齐 MaiBot 的 256 KiB 分块
const transferChunkSize = 256 * 1024

func (s *Store) maxFileBytes() int64 {
	if s.cfg.MaxFileBytes > 0 {
		return s.cfg.MaxFileBytes
	}
	return 50 * 1024 * 1024
}

func (s *Store) maxStorageBytes() int64 {
	if s.cfg.MaxStorageBytes > 0 {
		return s.cfg.MaxStorageBytes
	}
	return 10 * 1024 * 1024 * 1024
}

func (s *Store) maxConcurrentTransfers() int {
	if s.cfg.MaxConcurrentTransfers > 0 {
		return s.cfg.MaxConcurrentTransfers
	}
	return 4
}

func (s *Store) transferTimeout() time.Duration {
	if s.cfg.TransferTimeout > 0 {
		return s.cfg.TransferTimeout
	}
	return 120 * time.Second
}

// partialPath 返回传输中的临时文件路径 {file_ref}.part
func (s *Store) partialPath(fileRef string) (string, error) {
	if !fileRefPattern.MatchString(fileRef) {
		return "", ErrInvalidRef
	}
	path := filepath.Join(s.cfg.RootDir, fileRef+".part")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("附件临时路径必须是普通文件")
	}
	return path, nil
}

func newTransferID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Store) usageLocked() (used, reserved int64, active int64, err error) {
	if err = s.db.Model(&Asset{}).Where("state = ?", "ready").Select("COALESCE(SUM(size),0)").Scan(&used).Error; err != nil {
		return
	}
	if err = s.db.Model(&Asset{}).Select("COALESCE(SUM(reserved_bytes),0)").Scan(&reserved).Error; err != nil {
		return
	}
	err = s.db.Model(&Asset{}).Where("transfer_id <> ?", "").Count(&active).Error
	return
}

// reserveLocked 校验单文件上限、并发与配额，并写入传输凭证与预留空间
//
// 对应 MaiBot FileStore._reserve：远程声明大小不可信，始终按单文件最大空间预留，
// 接收完成后在 finish 里释放未使用部分。
func (s *Store) reserveLocked(asset *Asset) error {
	if asset.TransferID != "" {
		return fmt.Errorf("%w: 附件正在传输", ErrBusy)
	}
	maximum := s.maxFileBytes()
	if maximum <= 0 {
		return ErrLimit
	}
	if asset.Size > maximum {
		return fmt.Errorf("%w: 文件超过单文件大小上限", ErrLimit)
	}
	used, reserved, active, err := s.usageLocked()
	if err != nil {
		return err
	}
	if active >= int64(s.maxConcurrentTransfers()) {
		return fmt.Errorf("%w: 文件传输并发数已满，请稍后重试", ErrLimit)
	}
	if used+reserved+maximum > s.maxStorageBytes() {
		return fmt.Errorf("%w: 文件存储配额不足，请清理文件或调整配额", ErrLimit)
	}
	transferID, err := newTransferID()
	if err != nil {
		return err
	}
	partial, err := s.partialPath(asset.FileRef)
	if err != nil {
		return err
	}
	handle, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建文件临时文件失败: %w", err)
	}
	_ = handle.Close()
	now := nowSeconds()
	asset.TransferID = transferID
	asset.ReservedBytes = maximum
	asset.TransferStarted = now
	if asset.Origin == "remote" {
		asset.State = "downloading"
	} else {
		asset.State = "uploading"
	}
	asset.Error = ""
	return nil
}

// BeginUpload 预留单文件配额并创建可流式写入的临时附件
func (s *Store) BeginUpload(sessionID, name, mimeType string) (Asset, error) {
	name, err := NormalizeName(name)
	if err != nil {
		return Asset{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Asset{}, fmt.Errorf("session_id 不能为空")
	}
	fileRef, err := newFileRef()
	if err != nil {
		return Asset{}, err
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	now := nowSeconds()
	asset := Asset{
		FileRef: fileRef, Name: name, MimeType: mimeType,
		Origin: "local", CreatedAt: now, AccessedAt: now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reserveLocked(&asset); err != nil {
		return Asset{}, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&asset).Error; err != nil {
			return err
		}
		return tx.Create(&Binding{FileRef: fileRef, SessionID: sessionID, CreatedAt: now}).Error
	})
	if err != nil {
		if partial, pathErr := s.partialPath(fileRef); pathErr == nil {
			_ = os.Remove(partial)
		}
		return Asset{}, err
	}
	return asset, nil
}

func (s *Store) assetForTransfer(fileRef, transferID string) (Asset, error) {
	if !fileRefPattern.MatchString(fileRef) {
		return Asset{}, ErrInvalidRef
	}
	var asset Asset
	if err := s.db.First(&asset, "file_ref = ?", fileRef).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, err
	}
	if transferID == "" || asset.TransferID == "" ||
		len(asset.TransferID) != len(transferID) ||
		subtle.ConstantTimeCompare([]byte(asset.TransferID), []byte(transferID)) != 1 {
		return Asset{}, fmt.Errorf("文件传输凭证无效或已取消")
	}
	return asset, nil
}

// Append 将一个分块追加到临时文件，调用方负责最终 Finish 或 Fail
func (s *Store) Append(fileRef, transferID string, chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.assetForTransfer(fileRef, transferID)
	if err != nil {
		return err
	}
	if asset.State != "uploading" && asset.State != "downloading" {
		return ErrBusy
	}
	path, err := s.partialPath(fileRef)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("文件临时文件不存在")
		}
		return err
	}
	if info.Size()+int64(len(chunk)) > asset.ReservedBytes {
		return fmt.Errorf("%w: 实际文件大小超过单文件上限", ErrLimit)
	}
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := handle.Write(chunk); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}

// Finish 校验临时文件、计算哈希并按 file_ref 固化为正式文件
func (s *Store) Finish(fileRef, transferID string) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.assetForTransfer(fileRef, transferID)
	if err != nil {
		return Asset{}, err
	}
	partial, err := s.partialPath(fileRef)
	if err != nil {
		return Asset{}, err
	}
	sha256Hex, size, err := hashFile(partial)
	if err != nil {
		return Asset{}, err
	}
	if size > s.maxFileBytes() {
		return Asset{}, ErrLimit
	}
	blob, err := s.blobPath(fileRef)
	if err != nil {
		return Asset{}, err
	}
	if err := os.Rename(partial, blob); err != nil {
		return Asset{}, err
	}
	asset.Size = size
	asset.SHA256 = sha256Hex
	asset.State = "ready"
	asset.Error = ""
	asset.ReservedBytes = 0
	asset.TransferID = ""
	asset.TransferStarted = 0
	asset.AccessedAt = nowSeconds()
	if err := s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"size": size, "sha256": sha256Hex, "state": "ready", "error": "",
		"reserved_bytes": 0, "transfer_id": "", "transfer_started": 0,
		"accessed_at": asset.AccessedAt,
	}).Error; err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// Fail 放弃传输并清理临时文件与半成品
func (s *Store) Fail(fileRef, transferID, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.assetForTransfer(fileRef, transferID); err != nil {
		return nil
	}
	if path, pathErr := s.partialPath(fileRef); pathErr == nil {
		_ = os.Remove(path)
	}
	if path, pathErr := s.blobPath(fileRef); pathErr == nil {
		_ = os.Remove(path)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 2000 {
		reason = reason[:2000]
	}
	return s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"state": "failed", "error": reason, "reserved_bytes": 0,
		"transfer_id": "", "transfer_started": 0,
	}).Error
}

// Lease 在短期下载期间固定附件，避免清理线程误删
func (s *Store) Lease(fileRef, sessionID string, admin bool, seconds int) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.Get(fileRef, sessionID, admin)
	if err != nil {
		return Asset{}, err
	}
	if asset.State != "ready" {
		return Asset{}, ErrNotReady
	}
	if seconds <= 0 {
		seconds = 300
	}
	now := nowSeconds()
	until := now + float64(seconds)
	if asset.LeasedUntil < until {
		asset.LeasedUntil = until
	}
	asset.AccessedAt = now
	if err := s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"leased_until": asset.LeasedUntil, "accessed_at": now,
	}).Error; err != nil {
		return Asset{}, err
	}
	return asset, nil
}

// Usage 返回当前占用、预留和配额
func (s *Store) Usage() (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	used, reserved, _, err := s.usageLocked()
	if err != nil {
		return nil, err
	}
	return map[string]int64{
		"used_bytes": used, "reserved_bytes": reserved,
		"max_bytes": s.maxStorageBytes(), "max_file_bytes": s.maxFileBytes(),
	}, nil
}

// List 返回附件索引，管理员可带 session_id 过滤
func (s *Store) List(sessionID string, admin bool, offset, limit int) ([]map[string]any, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" && !admin {
		return nil, ErrForbidden
	}
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("分页参数无效")
	}
	query := s.db.Model(&Asset{}).Where("state <> ?", "deleted")
	if sessionID != "" {
		query = query.Joins("JOIN file_bindings ON file_bindings.file_ref = file_assets.file_ref").
			Where("file_bindings.session_id = ?", sessionID)
	}
	var assets []Asset
	if err := query.Order("file_assets.created_at DESC, file_assets.file_ref").Offset(offset).Limit(limit).Find(&assets).Error; err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		item := PublicView(asset)
		item["parse_state"] = "pending"
		item["parse_error"] = ""
		var parse Parse
		if err := s.db.First(&parse, "file_ref = ?", asset.FileRef).Error; err == nil {
			item["parse_state"] = parse.State
			item["parse_error"] = parse.Error
		}
		if admin {
			sessionIDs := make([]string, 0)
			if err := s.db.Model(&Binding{}).Where("file_ref = ?", asset.FileRef).Order("id").Pluck("session_id", &sessionIDs).Error; err != nil {
				return nil, err
			}
			item["session_ids"] = sessionIDs
		}
		items = append(items, item)
	}
	return items, nil
}

// Remove 删除附件；cache_only 只允许把远程缓存恢复为待下载状态
func (s *Store) Remove(fileRef string, cacheOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.Get(fileRef, "", true)
	if err != nil {
		return err
	}
	now := nowSeconds()
	if asset.TransferID != "" || asset.OpenReaders > 0 || asset.LeasedUntil > now {
		return ErrBusy
	}
	if cacheOnly && asset.Origin != "remote" {
		return ErrNotCache
	}
	if path, pathErr := s.blobPath(fileRef); pathErr == nil {
		_ = os.Remove(path)
	}
	state := "deleted"
	if cacheOnly {
		state = "remote"
	}
	if err := s.db.Model(&Asset{}).Where("file_ref = ?", fileRef).Updates(map[string]any{
		"state": state, "error": "",
	}).Error; err != nil {
		return err
	}
	return s.db.Where("file_ref = ?", fileRef).Delete(&Parse{}).Error
}

// Cleanup 清理中断传输、过期远程缓存和启动时遗留的临时文件
func (s *Store) Cleanup(startup bool) (map[string]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := nowSeconds()
	expired, interrupted := 0, 0
	var assets []Asset
	if err := s.db.Where("state <> ?", "deleted").Find(&assets).Error; err != nil {
		return nil, err
	}
	timeout := s.transferTimeout().Seconds()
	for _, asset := range assets {
		updates := map[string]any{}
		if startup && (asset.OpenReaders != 0 || asset.LeasedUntil != 0) {
			updates["open_readers"] = 0
			updates["leased_until"] = 0
			asset.OpenReaders = 0
			asset.LeasedUntil = 0
		}
		if asset.TransferID != "" && (startup || now-asset.TransferStarted > timeout+60) {
			if path, pathErr := s.partialPath(asset.FileRef); pathErr == nil {
				_ = os.Remove(path)
			}
			if path, pathErr := s.blobPath(asset.FileRef); pathErr == nil {
				_ = os.Remove(path)
			}
			updates["transfer_id"] = ""
			updates["reserved_bytes"] = 0
			updates["state"] = "failed"
			updates["error"] = "文件传输已中断，请重新发起"
			interrupted++
		} else if asset.Origin == "remote" && asset.State == "ready" && asset.OpenReaders == 0 &&
			asset.LeasedUntil <= now && now-asset.AccessedAt > float64(s.cacheDays())*86400 {
			if path, pathErr := s.blobPath(asset.FileRef); pathErr == nil {
				_ = os.Remove(path)
			}
			updates["state"] = "remote"
			if err := s.db.Where("file_ref = ?", asset.FileRef).Delete(&Parse{}).Error; err != nil {
				return nil, err
			}
			expired++
		}
		if len(updates) > 0 {
			if err := s.db.Model(&Asset{}).Where("file_ref = ?", asset.FileRef).Updates(updates).Error; err != nil {
				return nil, err
			}
		}
	}
	if startup {
		entries, err := os.ReadDir(s.cfg.RootDir)
		if err != nil {
			return nil, err
		}
		indexed := make(map[string]Asset, len(assets))
		for _, asset := range assets {
			indexed[asset.FileRef] = asset
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			extension := filepath.Ext(name)
			if extension != ".part" && extension != ".blob" {
				continue
			}
			fileRef := strings.TrimSuffix(name, extension)
			if !fileRefPattern.MatchString(fileRef) {
				continue
			}
			asset, found := indexed[fileRef]
			// 崩溃可能发生在创建临时文件后、索引提交前，只清理本服务生成的孤立路径
			if extension == ".part" || !found || asset.State != "ready" {
				_ = os.Remove(filepath.Join(s.cfg.RootDir, name))
			}
		}
	}
	return map[string]int{"expired": expired, "interrupted": interrupted}, nil
}

func (s *Store) cacheDays() int {
	if s.cfg.CacheDays > 0 {
		return s.cfg.CacheDays
	}
	return 30
}

// ReadBytes 在授权与长度校验后读取完整附件
//
// 对齐 MaiBot read_bytes：本方法不限制文本大小，文本上限由 ReadText 判定。
func (s *Store) ReadBytes(fileRef, sessionID string, admin bool) ([]byte, error) {
	asset, err := s.Get(fileRef, sessionID, admin)
	if err != nil {
		return nil, err
	}
	if asset.State != "ready" {
		return nil, ErrNotReady
	}
	path, err := s.blobPath(asset.FileRef)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != asset.Size || hex.EncodeToString(digest[:]) != asset.SHA256 {
		return nil, fmt.Errorf("附件内容校验失败")
	}
	_ = s.Touch(fileRef, sessionID, admin)
	return data, nil
}
