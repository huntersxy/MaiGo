package host

// 持久登录会话，对齐 src/host/sessions.py。

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sessionSeconds = 8 * 60 * 60
	ticketSeconds  = 60
	maxTickets     = 64
)

type hostSession struct {
	expiresAt time.Time
	origin    string
}

// Sessions 登录会话；数据库只保存票号摘要与绝对过期时间
type Sessions struct {
	mu          sync.Mutex
	CookieName  string
	SelectName  string
	secure      bool
	credentials *Credentials
	tickets     map[string]time.Time
	sessions    map[string]hostSession
	path        string
}

// NewSessions 打开或重建会话库
func NewSessions(workspace string, credentials *Credentials, secureCookie bool) (*Sessions, error) {
	sum := sha256.Sum256([]byte(workspace))
	suffix := hex.EncodeToString(sum[:])[:12]
	instance := &Sessions{
		CookieName:  "maibot_host_session_" + suffix,
		SelectName:  "maibot_host_selection_" + suffix,
		secure:      secureCookie,
		credentials: credentials,
		tickets:     map[string]time.Time{},
		path:        filepath.Join(filepath.Dir(credentials.Path()), "sessions.sqlite3"),
	}
	if err := instance.load(); err != nil {
		var schemaErr *invalidSessionSchema
		if !errors.As(err, &schemaErr) {
			return nil, err
		}
		if err := instance.quarantine(); err != nil {
			return nil, err
		}
		if err := instance.load(); err != nil {
			return nil, err
		}
	}
	return instance, nil
}

type invalidSessionSchema struct{ message string }

func (e *invalidSessionSchema) Error() string { return e.message }

func (s *Sessions) load() error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS sessions (digest TEXT PRIMARY KEY, expires_at REAL NOT NULL)"); err != nil {
		return &invalidSessionSchema{message: "登录会话库损坏"}
	}
	columns := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(sessions)")
	if err != nil {
		return &invalidSessionSchema{message: "登录会话库无法读取"}
	}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err == nil {
			columns[name] = true
		}
	}
	rows.Close()
	if !columns["digest"] || !columns["expires_at"] {
		return &invalidSessionSchema{message: "登录会话表缺少必需列"}
	}
	if !columns["origin"] {
		if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN origin TEXT"); err != nil {
			return &invalidSessionSchema{message: "登录会话表无法补齐 origin 列"}
		}
	}
	if s.credentials.TokenSource() == "temporary" {
		if _, err := db.Exec("DELETE FROM sessions"); err != nil {
			return err
		}
	}
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := db.Exec("DELETE FROM sessions WHERE expires_at <= ?", now); err != nil {
		return err
	}
	loaded, err := db.Query("SELECT digest, expires_at, origin FROM sessions")
	if err != nil {
		return err
	}
	defer loaded.Close()
	sessions := map[string]hostSession{}
	for loaded.Next() {
		var digest string
		var expiresAt float64
		var origin sql.NullString
		if err := loaded.Scan(&digest, &expiresAt, &origin); err != nil {
			return err
		}
		sessions[digest] = hostSession{
			expiresAt: time.Unix(0, int64(expiresAt*1e9)),
			origin:    origin.String,
		}
	}
	s.sessions = sessions
	return loaded.Err()
}

func (s *Sessions) quarantine() error {
	backup := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().UnixNano())
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := s.path + suffix
		if _, err := os.Stat(sidecar); err == nil {
			_ = os.Rename(sidecar, backup+suffix)
		}
	}
	if err := os.Rename(s.path, backup); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Sessions) digest(session string) string {
	mac := hmac.New(sha256.New, []byte(s.credentials.Token()))
	mac.Write([]byte(session))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Sessions) pruneTickets(now time.Time) {
	for ticket, deadline := range s.tickets {
		if !deadline.After(now) {
			delete(s.tickets, ticket)
		}
	}
}

// IssueTicket 签发一次性交接票据
func (s *Sessions) IssueTicket() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneTickets(now)
	if len(s.tickets) >= maxTickets {
		return "", errors.New("交接请求过多，请稍后重试")
	}
	ticket := randomURLSafe(32)
	s.tickets[ticket] = now.Add(ticketSeconds * time.Second)
	return ticket, nil
}

// Exchange 用票据换登录会话
func (s *Sessions) Exchange(ticket, origin string) (string, bool) {
	s.mu.Lock()
	now := time.Now()
	s.pruneTickets(now)
	if _, exists := s.tickets[ticket]; !exists {
		s.mu.Unlock()
		return "", false
	}
	delete(s.tickets, ticket)
	s.mu.Unlock()
	session, err := s.CreateSession(origin)
	if err != nil {
		return "", false
	}
	return session, true
}

// CreateSession 创建会话并落库
func (s *Sessions) CreateSession(origin string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, record := range s.sessions {
		if !record.expiresAt.After(now) {
			delete(s.sessions, key)
		}
	}
	session := randomURLSafe(32)
	digest := s.digest(session)
	expiresAt := now.Add(sessionSeconds * time.Second)
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	if _, err := db.Exec("DELETE FROM sessions WHERE expires_at <= ?", float64(now.UnixNano())/1e9); err != nil {
		return "", err
	}
	if _, err := db.Exec("INSERT INTO sessions (digest, expires_at, origin) VALUES (?, ?, ?)",
		digest, float64(expiresAt.UnixNano())/1e9, origin); err != nil {
		return "", err
	}
	s.sessions[digest] = hostSession{expiresAt: expiresAt, origin: origin}
	return session, nil
}

func (s *Sessions) active(session string) (hostSession, bool) {
	if session == "" {
		return hostSession{}, false
	}
	record, exists := s.sessions[s.digest(session)]
	if !exists || !record.expiresAt.After(time.Now()) {
		return hostSession{}, false
	}
	return record, true
}

// LoginOrigin 会话首次登录来源
func (s *Sessions) LoginOrigin(session string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.active(session)
	if !ok {
		return ""
	}
	return record.origin
}

// SetCookie 写入会话 Cookie
func (s *Sessions) SetCookie(w http.ResponseWriter, session string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.CookieName, Value: session, MaxAge: sessionSeconds,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Path: "/", Secure: s.secure,
	})
}

// ClearAll 清空全部会话与票据
func (s *Sessions) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := sql.Open("sqlite", s.path)
	if err == nil {
		_, _ = db.Exec("DELETE FROM sessions")
		db.Close()
	}
	s.sessions = map[string]hostSession{}
	s.tickets = map[string]time.Time{}
}

// UpdatePassword 修改口令并清空会话；首次设置时返回新会话
func (s *Sessions) UpdatePassword(session, newToken string) (string, error) {
	s.mu.Lock()
	record, ok := s.active(session)
	if !ok {
		s.mu.Unlock()
		return "", errors.New("登录已失效，请重新登录")
	}
	initial := s.credentials.TokenSource() == "temporary"
	s.mu.Unlock()
	if err := s.credentials.Update(newToken); err != nil {
		return "", err
	}
	s.ClearAll()
	if initial {
		return s.CreateSession(record.origin)
	}
	return "", nil
}

// Valid 会话是否仍然有效
func (s *Sessions) Valid(session string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active(session)
	return ok
}

// Revoke 注销单个会话
func (s *Sessions) Revoke(session string) {
	if session == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := s.digest(session)
	db, err := sql.Open("sqlite", s.path)
	if err == nil {
		_, _ = db.Exec("DELETE FROM sessions WHERE digest = ?", digest)
		db.Close()
	}
	delete(s.sessions, digest)
}

func randomURLSafe(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), ":", "")
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}
