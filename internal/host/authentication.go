package host

// 宿主持有的 WebUI 登录凭据，对齐 src/host/authentication.py。

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Credentials WebUI 登录凭据
type Credentials struct {
	path string
	mu   sync.Mutex
	data map[string]any
}

// NewCredentials 加载或创建凭据；临时密码每次运行重新生成
func NewCredentials(path string) (*Credentials, error) {
	credentials := &Credentials{path: path}
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := credentials.save(map[string]any{
			"access_token": randomHexToken(), "token_source": "temporary", "first_setup_completed": false,
		}); err != nil {
			return nil, err
		}
		return credentials, nil
	}
	data, err := credentials.read()
	if err != nil {
		return nil, err
	}
	credentials.data = data
	if credentials.TokenSource() == "temporary" {
		data["access_token"] = randomHexToken()
		if err := credentials.save(data); err != nil {
			return nil, err
		}
	}
	return credentials, nil
}

func (c *Credentials) read() (map[string]any, error) {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return nil, fmt.Errorf("WebUI 凭据文件无法读取：%s", c.path)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		return nil, fmt.Errorf("WebUI 凭据文件无法解析：%s", c.path)
	}
	token, _ := data["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("WebUI 凭据文件无效，拒绝覆盖：%s", c.path)
	}
	source, _ := data["token_source"].(string)
	if source != "temporary" && source != "configured" {
		if len(token) == 64 && isLowerHex(token) {
			data["token_source"] = "temporary"
		} else {
			data["token_source"] = "configured"
		}
	}
	return data, nil
}

func (c *Credentials) save(data map[string]any) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	temporary := fmt.Sprintf("%s.%d.tmp", c.path, time.Now().UnixNano())
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, c.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	c.data = data
	return nil
}

// Path 凭据文件路径
func (c *Credentials) Path() string { return c.path }

// Token 当前访问口令
func (c *Credentials) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	token, _ := c.data["access_token"].(string)
	return token
}

// TokenSource 口令来源：temporary 或 configured
func (c *Credentials) TokenSource() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	source, _ := c.data["token_source"].(string)
	return source
}

// Verify 恒时比较访问口令
func (c *Credentials) Verify(token string) bool {
	return subtle.ConstantTimeCompare([]byte(c.Token()), []byte(token)) == 1
}

// SetupStatus 首次设置状态
func (c *Credentials) SetupStatus() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	completed, _ := c.data["first_setup_completed"].(bool)
	source, _ := c.data["token_source"].(string)
	return map[string]any{
		"is_first_setup":        !completed,
		"token_source":          source,
		"requires_custom_token": source == "temporary",
	}
}

// Update 设置固定口令
func (c *Credentials) Update(newToken string) error {
	if len(newToken) < 10 || !containsClass(newToken, 'A', 'Z') || !containsClass(newToken, 'a', 'z') {
		return errors.New("Token 至少 10 位，且包含大写字母、小写字母和特殊符号")
	}
	if !strings.ContainsAny(newToken, "!@#$%^&*()_+-=[]{}|;:,.<>?/") {
		return errors.New("Token 必须包含特殊符号")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data := cloneCredentials(c.data)
	data["access_token"] = newToken
	data["token_source"] = "configured"
	return c.save(data)
}

// Regenerate 生成新的固定口令
func (c *Credentials) Regenerate() (string, error) {
	token := randomHexToken()
	c.mu.Lock()
	defer c.mu.Unlock()
	data := cloneCredentials(c.data)
	data["access_token"] = token
	data["token_source"] = "configured"
	if err := c.save(data); err != nil {
		return "", err
	}
	return token, nil
}

// CompleteSetup 标记首次设置已完成
func (c *Credentials) CompleteSetup() error {
	if c.TokenSource() == "temporary" {
		return errors.New("请先设置固定 WebUI 密码")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	data := cloneCredentials(c.data)
	data["first_setup_completed"] = true
	return c.save(data)
}

// ResetSetup 重置首次设置，可选同时重置口令
func (c *Credentials) ResetSetup(resetPassword bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data := cloneCredentials(c.data)
	data["first_setup_completed"] = false
	if resetPassword {
		data["access_token"] = randomHexToken()
		data["token_source"] = "temporary"
	}
	return c.save(data)
}

func cloneCredentials(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func containsClass(value string, low, high rune) bool {
	for _, char := range value {
		if char >= low && char <= high {
			return true
		}
	}
	return false
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func randomHexToken() string {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

// LoginAttempts 认证失败限流
type LoginAttempts struct {
	mu       sync.Mutex
	failures map[string]loginFailure
}

type loginFailure struct {
	count    int
	deadline time.Time
}

// NewLoginAttempts 创建限流器
func NewLoginAttempts() *LoginAttempts {
	return &LoginAttempts{failures: map[string]loginFailure{}}
}

// Blocked 该地址是否已被封锁
func (l *LoginAttempts) Blocked(address string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for key, entry := range l.failures {
		if !entry.deadline.After(now) {
			delete(l.failures, key)
		}
	}
	entry, exists := l.failures[address]
	return exists && entry.count >= 5
}

// Record 记录一次认证结果
func (l *LoginAttempts) Record(address string, success bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if success {
		delete(l.failures, address)
		return nil
	}
	if len(l.failures) >= 2048 {
		if _, exists := l.failures[address]; !exists {
			return errors.New("登录请求过多，请稍后再试")
		}
	}
	entry := l.failures[address]
	if entry.deadline.IsZero() {
		entry.deadline = time.Now().Add(10 * time.Minute)
	}
	entry.count++
	l.failures[address] = entry
	return nil
}
