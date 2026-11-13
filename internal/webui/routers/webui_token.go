package webuiapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WebUI 访问令牌来源，对齐 MaiBot core/security.py
const webuiTokenSourceTemporary = "temporary"

var webuiTokenManagers sync.Map // webui.json 绝对路径 -> *webuiTokenManager

type webuiTokenManager struct {
	mu   sync.Mutex
	path string
	raw  map[string]any
}

// webUITokenManager 返回实例数据目录下的 WebUI 令牌管理器
func (d Deps) webUITokenManager() (*webuiTokenManager, error) {
	if strings.TrimSpace(d.InstanceDir) == "" {
		return nil, errors.New("需要设置 MAIBOT_INSTANCE_DIR 指向 MaiBot 实例根目录")
	}
	path, err := filepath.Abs(filepath.Join(d.InstanceDir, "data", "webui.json"))
	if err != nil {
		return nil, err
	}
	if cached, ok := webuiTokenManagers.Load(path); ok {
		return cached.(*webuiTokenManager), nil
	}
	manager, err := loadWebUITokenManager(path)
	if err != nil {
		return nil, err
	}
	actual, _ := webuiTokenManagers.LoadOrStore(path, manager)
	return actual.(*webuiTokenManager), nil
}

func loadWebUITokenManager(path string) (*webuiTokenManager, error) {
	manager := &webuiTokenManager{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	if token := manager.token(); token == "" {
		if _, err := manager.createNewToken(true, webuiTokenSourceTemporary); err != nil {
			return nil, err
		}
		return manager, nil
	}
	source := manager.resolveTokenSource()
	if stringValue(manager.raw["token_source"]) != source {
		manager.raw["token_source"] = source
		if err := manager.save(); err != nil {
			return nil, err
		}
	}
	if source == webuiTokenSourceTemporary {
		// 临时令牌每次启动重新生成，保留首次配置状态与未知字段
		if _, err := manager.createNewToken(true, webuiTokenSourceTemporary); err != nil {
			return nil, err
		}
	}
	return manager, nil
}

func (m *webuiTokenManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			m.raw = map[string]any{}
			return nil
		}
		return err
	}
	raw := map[string]any{}
	if err := json.Unmarshal(data, &raw); err != nil {
		// 与 Python 一致：配置损坏时重建
		m.raw = map[string]any{}
		return nil
	}
	m.raw = raw
	return nil
}

func (m *webuiTokenManager) save() error {
	encoded, err := json.MarshalIndent(m.raw, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary := m.path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, m.path)
}

func (m *webuiTokenManager) token() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.TrimSpace(stringValue(m.raw["access_token"]))
}

func (m *webuiTokenManager) resolveTokenSource() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return resolveWebUITokenSource(m.raw)
}

func resolveWebUITokenSource(raw map[string]any) string {
	source := strings.ToLower(strings.TrimSpace(stringValue(raw["token_source"])))
	if source == webuiTokenSourceTemporary || source == webuiTokenSourceConfigured {
		return source
	}
	token := strings.TrimSpace(stringValue(raw["access_token"]))
	if token == "" {
		return webuiTokenSourceTemporary
	}
	if !isSystemGeneratedWebUIToken(token) {
		return webuiTokenSourceConfigured
	}
	return webuiTokenSourceTemporary
}

func isSystemGeneratedWebUIToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func (m *webuiTokenManager) isFirstSetup() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !boolValue(m.raw["first_setup_completed"])
}

func (m *webuiTokenManager) createNewToken(preserveSetup bool, source string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	newToken, err := randomWebUIToken()
	if err != nil {
		return "", err
	}
	now := webuiNowTimestamp()
	firstSetup := false
	var completedAt any
	if preserveSetup {
		firstSetup = boolValue(m.raw["first_setup_completed"])
		completedAt = m.raw["setup_completed_at"]
	}
	replacement := map[string]any{
		"access_token":          newToken,
		"created_at":            now,
		"updated_at":            now,
		"first_setup_completed": firstSetup,
		"token_source":          source,
	}
	if preserveSetup {
		for key, value := range m.raw {
			if _, exists := replacement[key]; !exists {
				replacement[key] = value
			}
		}
	}
	if firstSetup && completedAt != nil {
		replacement["setup_completed_at"] = completedAt
	}
	m.raw = replacement
	return newToken, m.save()
}

func (m *webuiTokenManager) markSetupCompleted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw["first_setup_completed"] = true
	m.raw["setup_completed_at"] = webuiNowTimestamp()
	return m.save() == nil
}

func (m *webuiTokenManager) resetSetupStatus(resetPassword bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw["first_setup_completed"] = false
	delete(m.raw, "setup_completed_at")
	if resetPassword {
		newToken, err := randomWebUIToken()
		if err != nil {
			return false
		}
		m.raw["access_token"] = newToken
		m.raw["token_source"] = webuiTokenSourceTemporary
		m.raw["updated_at"] = webuiNowTimestamp()
	}
	return m.save() == nil
}

func (m *webuiTokenManager) updateToken(newToken string) (bool, string) {
	if ok, message := validateCustomWebUIToken(newToken); !ok {
		return false, message
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw["access_token"] = newToken
	m.raw["updated_at"] = webuiNowTimestamp()
	m.raw["token_source"] = webuiTokenSourceConfigured
	if err := m.save(); err != nil {
		return false, "更新失败: " + err.Error()
	}
	return true, "Token 更新成功"
}

func (m *webuiTokenManager) regenerateToken() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	newToken, err := randomWebUIToken()
	if err != nil {
		return "", err
	}
	firstSetup, ok := m.raw["first_setup_completed"].(bool)
	if !ok {
		firstSetup = true
	}
	m.raw["access_token"] = newToken
	m.raw["updated_at"] = webuiNowTimestamp()
	m.raw["first_setup_completed"] = firstSetup
	m.raw["token_source"] = webuiTokenSourceConfigured
	return newToken, m.save()
}

func (m *webuiTokenManager) verifyToken(token string) bool {
	current := m.token()
	if token == "" || current == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(current)) == 1
}

func (m *webuiTokenManager) verifyCookieToken(cookieValue string) bool {
	if cookieValue == "" {
		return false
	}
	candidates := []string{cookieValue}
	if decoded, err := url.QueryUnescape(cookieValue); err == nil && decoded != cookieValue {
		candidates = append(candidates, decoded)
	}
	for _, candidate := range candidates {
		if m.verifyToken(candidate) {
			return true
		}
	}
	return false
}

// EncodeCookieToken 把 Unicode 令牌编码为 ASCII Cookie 值，对齐 encode_cookie_token
func EncodeCookieToken(token string) string {
	if token == "" || isASCII(token) {
		return token
	}
	return url.QueryEscape(token)
}

func isASCII(value string) bool {
	for _, char := range value {
		if char > 127 {
			return false
		}
	}
	return true
}

func randomWebUIToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func webuiNowTimestamp() string {
	return time.Now().Format("2006-01-02T15:04:05.000000")
}

func validateCustomWebUIToken(token string) (bool, string) {
	if token == "" {
		return false, "Token 不能为空"
	}
	if len([]rune(token)) < 10 {
		return false, "Token 长度至少为 10 位"
	}
	hasUpper, hasLower, hasSpecial := false, false, false
	specialChars := "!@#$%^&*()_+-=[]{}|;:,.<>?/"
	for _, char := range token {
		switch {
		case char >= 'A' && char <= 'Z':
			hasUpper = true
		case char >= 'a' && char <= 'z':
			hasLower = true
		case strings.ContainsRune(specialChars, char):
			hasSpecial = true
		}
	}
	if !hasUpper {
		return false, "Token 必须包含大写字母"
	}
	if !hasLower {
		return false, "Token 必须包含小写字母"
	}
	if !hasSpecial {
		return false, "Token 必须包含特殊符号 (" + specialChars + ")"
	}
	return true, "Token 格式正确"
}
