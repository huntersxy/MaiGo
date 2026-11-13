package filesvc

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Service 签发限时下载凭证并协调 Store
type Service struct {
	store *Store
	cfg   Config

	mu      sync.Mutex
	tickets map[string]TicketClaims

	readMu  sync.Mutex
	readers int

	senderMu     sync.RWMutex
	pluginSender PluginSender

	resolverMu     sync.RWMutex
	remoteResolver RemoteURLResolver
}

// TicketClaims 文件凭证声明
//
// Purpose 区分 plugin/upload/download；Scope 保存插件调用作用域
// PluginID 与 SessionID 仅在对应凭证上使用
type TicketClaims struct {
	Purpose   string
	FileRef   string
	Expires   float64
	Scope     string
	PluginID  string
	SessionID string
}

// NewService 创建文件服务
func NewService(store *Store, cfg Config) *Service {
	return &Service{
		store:   store,
		cfg:     cfg,
		tickets: make(map[string]TicketClaims),
	}
}

// Enabled 是否允许文件能力
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled
}

// ErrDisabled 表示文件能力总开关已关闭
var ErrDisabled = errors.New("文件功能已关闭，请在能力设置中开启“文件功能”")

// ErrReadSlotDoubleRelease 对齐 Python release_read 在名额已归零时抛出的 RuntimeError
var ErrReadSlotDoubleRelease = errors.New("文件读取名额重复释放")

func (s *Service) requireEnabled() error {
	if !s.Enabled() {
		return ErrDisabled
	}
	return nil
}

// AcquireRead 占用一个读取名额；名额与传输共用 max_concurrent_transfers（对齐 FileService.acquire_read）
func (s *Service) AcquireRead() bool {
	if s == nil {
		return false
	}
	limit := s.cfg.MaxConcurrentTransfers
	if limit <= 0 {
		limit = 4
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.readers >= limit {
		return false
	}
	s.readers++
	return true
}

// ReleaseRead 释放读取名额；名额已归零时返回错误，对齐 Python release_read
func (s *Service) ReleaseRead() error {
	if s == nil {
		return ErrReadSlotDoubleRelease
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.readers <= 0 {
		return ErrReadSlotDoubleRelease
	}
	s.readers--
	return nil
}

// SaveContent 保存字节并绑定会话
func (s *Service) SaveContent(sessionID, name, mimeType string, content []byte) (Asset, error) {
	if err := s.requireEnabled(); err != nil {
		return Asset{}, err
	}
	return s.store.CreateLocal(sessionID, name, mimeType, content)
}

// BeginUpload 预留配额并创建流式上传附件
func (s *Service) BeginUpload(sessionID, name, mimeType string) (Asset, error) {
	if err := s.requireEnabled(); err != nil {
		return Asset{}, err
	}
	return s.store.BeginUpload(sessionID, name, mimeType)
}

// Cleanup 清理中断传输与过期缓存
func (s *Service) Cleanup(startup bool) (map[string]int, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	return s.store.Cleanup(startup)
}

// ticketLifetime 返回配置的凭证有效期，零值回退 Python 默认 300 秒
func (s *Service) ticketLifetime() time.Duration {
	if s.cfg.TicketLifetime > 0 {
		return s.cfg.TicketLifetime
	}
	return 5 * time.Minute
}

// Ticket 签发限时凭证
func (s *Service) Ticket(purpose, fileRef string, lifetime time.Duration, extra map[string]string) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	if lifetime <= 0 {
		lifetime = s.ticketLifetime()
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	now := time.Now().Unix()
	claims := TicketClaims{Purpose: purpose, FileRef: fileRef, Expires: float64(now) + lifetime.Seconds()}
	if extra != nil {
		claims.Scope = extra["scope"]
		claims.PluginID = extra["plugin_id"]
		claims.SessionID = extra["session_id"]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
	if len(s.tickets) >= 1024 {
		return "", fmt.Errorf("文件传输凭证数量已满")
	}
	s.tickets[token] = claims
	return token, nil
}

func (s *Service) purgeExpiredLocked(now int64) {
	for key, value := range s.tickets {
		if value.Expires <= float64(now) {
			delete(s.tickets, key)
		}
	}
}

// Claims 校验并返回凭证内容
func (s *Service) Claims(token, purpose string) (TicketClaims, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(time.Now().Unix())
	claims, ok := s.tickets[token]
	if !ok || claims.Purpose != purpose || claims.Expires <= float64(time.Now().Unix()) {
		return TicketClaims{}, errors.New("文件凭证无效或已过期")
	}
	return claims, nil
}

// UploadTicket 签发一次性上传凭证，绑定 session_id
func (s *Service) UploadTicket(sessionID string, lifetime time.Duration) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	return s.Ticket("upload", "", lifetime, map[string]string{"session_id": sessionID})
}

// UploadTicketFor 签发绑定具体附件的流式上传凭证
func (s *Service) UploadTicketFor(fileRef, sessionID string, lifetime time.Duration) (string, error) {
	if !fileRefPattern.MatchString(strings.TrimSpace(fileRef)) {
		return "", ErrInvalidRef
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session_id 不能为空")
	}
	return s.Ticket("upload", fileRef, lifetime, map[string]string{"session_id": sessionID})
}

// Revoke 使凭证失效
func (s *Service) Revoke(token string) {
	s.mu.Lock()
	delete(s.tickets, token)
	s.mu.Unlock()
}

// DownloadURL 为已就绪附件签发下载地址；对齐 Python download_url：只 lease+ticket，不隐式 Ensure
func (s *Service) DownloadURL(fileRef, sessionID string, admin bool, baseURL string) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	if _, err := s.store.Lease(fileRef, sessionID, admin, int(s.ticketLifetime().Seconds())); err != nil {
		return "", err
	}
	token, err := s.Ticket("download", fileRef, s.cfg.TicketLifetime, nil)
	if err != nil {
		return "", err
	}
	transferURL, err := s.TransferURL(token, baseURL)
	if err != nil {
		s.Revoke(token)
		return "", err
	}
	return transferURL, nil
}

// TransferURL 拼接对外传输 URL，并校验基址格式
func (s *Service) TransferURL(token, baseURL string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = s.cfg.DefaultTransferBaseURL
	}
	base = strings.TrimRight(base, "/")
	if err := validateTransferBaseURL(base); err != nil {
		return "", err
	}
	return base + "/api/files/transfers/" + url.PathEscape(token), nil
}

func validateTransferBaseURL(base string) error {
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("files.transfer_base_url 必须是无凭证、无查询参数的 HTTP(S) 地址")
	}
	return nil
}

// Store 暴露底层存储供 HTTP 层读取
func (s *Service) Store() *Store { return s.store }

// Config 返回当前配置快照
func (s *Service) Config() Config { return s.cfg }
