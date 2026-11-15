package server

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	envEnable = "MAIBOT_MAIM_MESSAGE_ENABLE"
	envHost   = "MAIBOT_MAIM_MESSAGE_HOST"
	envPort   = "MAIBOT_MAIM_MESSAGE_PORT"
	envToken  = "MAIBOT_MAIM_MESSAGE_TOKEN"

	envAPIEnable     = "MAIBOT_MAIM_MESSAGE_API_ENABLE"
	envAPIHost       = "MAIBOT_MAIM_MESSAGE_API_HOST"
	envAPIPort       = "MAIBOT_MAIM_MESSAGE_API_PORT"
	envAPIPath       = "MAIBOT_MAIM_MESSAGE_API_PATH"
	envAPIKeys       = "MAIBOT_MAIM_MESSAGE_API_KEYS"
	envAPIUserID     = "MAIBOT_MAIM_MESSAGE_API_USER_ID"
	envAPIIdentities = "MAIBOT_MAIM_MESSAGE_API_KEY_IDENTITIES"

	envLegacySSLEnabled = "MAIBOT_MAIM_MESSAGE_SSL_ENABLED"
	envLegacySSLVerify  = "MAIBOT_MAIM_MESSAGE_SSL_VERIFY"
	envLegacySSL        = "MAIBOT_MAIM_MESSAGE_SSL"
	envLegacySSLCert    = "MAIBOT_MAIM_MESSAGE_SSL_CERTFILE"
	envLegacySSLKey     = "MAIBOT_MAIM_MESSAGE_SSL_KEYFILE"
	envLegacySSLCA      = "MAIBOT_MAIM_MESSAGE_SSL_CA_CERTS"

	envAPISSLEnabled  = "MAIBOT_MAIM_MESSAGE_API_SSL_ENABLED"
	envAPISSLVerify   = "MAIBOT_MAIM_MESSAGE_API_SSL_VERIFY"
	envAPISSL         = "MAIBOT_MAIM_MESSAGE_API_SSL"
	envAPISSLCert     = "MAIBOT_MAIM_MESSAGE_API_SSL_CERTFILE"
	envAPISSLKey      = "MAIBOT_MAIM_MESSAGE_API_SSL_KEYFILE"
	envAPISSLCA       = "MAIBOT_MAIM_MESSAGE_API_SSL_CA_CERTS"
	envAPIStatsEnable = "MAIBOT_MAIM_MESSAGE_API_STATS_ENABLED"
	envAPICacheEnable = "MAIBOT_MAIM_MESSAGE_API_MESSAGE_CACHE_ENABLED"
	envAPICacheTTL    = "MAIBOT_MAIM_MESSAGE_API_MESSAGE_CACHE_TTL"
	envAPICacheSize   = "MAIBOT_MAIM_MESSAGE_API_MESSAGE_CACHE_MAX_SIZE"
	envAPICacheClean  = "MAIBOT_MAIM_MESSAGE_API_MESSAGE_CACHE_CLEANUP_INTERVAL"
	envAPIMaxMessage  = "MAIBOT_MAIM_MESSAGE_API_MAX_MESSAGE_SIZE"

	defaultHost          = "127.0.0.1"
	defaultPort          = 8000
	defaultAPIPath       = "/ws"
	defaultAPIUserID     = "sys_default"
	defaultAPICacheTTL   = 300 * time.Second
	defaultAPICacheSize  = 1000
	defaultAPICacheClean = 60 * time.Second
	defaultAPIMaxMessage = 100 * 1024 * 1024
)

// APIKeyIdentity 单个 API Key 绑定的平台账号与作用域
// 同一平台可由多个 Key 分别代表不同账号，出站时按路由精确选择连接
type APIKeyIdentity struct {
	AccountID string
	Scope     string
}

// Config maim_message 服务配置：旧版 WS 与新 API Server 共用
type Config struct {
	Enabled bool
	Host    string
	Port    int
	Tokens  []string

	// LegacySSL 旧版 maim_message WebSocket 的 TLS 配置
	LegacySSLEnabled bool
	LegacySSLConfig  TLSConfig

	APIEnabled bool
	APIHost    string
	APIPort    int
	APIPath    string
	APIKeys    []string
	APIUserID  string

	// APIKeyIdentities 按 API Key 绑定平台账号/作用域；Key 不在 APIKeys 时自动加入白名单
	APIKeyIdentities map[string]APIKeyIdentity

	// APISSL 新 API Server 的 TLS 配置
	APISSLEnabled bool
	APISSLConfig  TLSConfig

	// API 统计与消息缓存，对齐 maim_message ServerConfig
	APIStatsEnabled        bool
	APIMessageCacheEnabled bool
	APIMessageCacheTTL     time.Duration
	APIMessageCacheMaxSize int
	APIMessageCacheCleanup time.Duration
	APIMaxMessageSize      int64
}

// TLSConfig 证书与客户端校验配置
type TLSConfig struct {
	CertFile   string
	KeyFile    string
	CAFile     string
	VerifyPeer bool
}

// Enabled 证书与私钥都配置时启用
func (c TLSConfig) Enabled() bool {
	return strings.TrimSpace(c.CertFile) != "" && strings.TrimSpace(c.KeyFile) != ""
}

// LoadConfigFromEnv 从环境变量读取配置；未设置 ENABLE 时 Enabled 为 false
func LoadConfigFromEnv() Config {
	defaults := legacyDefaults()
	cfg := defaults
	applyEnvOverrides(&cfg, defaults)
	return cfg
}

// loadAPIKeyIdentities 解析 MAIBOT_MAIM_MESSAGE_API_KEY_IDENTITIES
// 支持 JSON 对象 {"key":{"account_id":"...","scope":"..."}}，
// 也支持 key|account|scope 以逗号/分号分隔的简写
func loadAPIKeyIdentities(raw string) map[string]APIKeyIdentity {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	identities := map[string]APIKeyIdentity{}
	if strings.HasPrefix(raw, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil
		}
		for key, value := range payload {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			identity := APIKeyIdentity{}
			switch v := value.(type) {
			case string:
				identity.AccountID = strings.TrimSpace(v)
			case map[string]any:
				identity.AccountID = strings.TrimSpace(stringFromAny(v["account_id"]))
				if identity.AccountID == "" {
					identity.AccountID = strings.TrimSpace(stringFromAny(v["account"]))
				}
				identity.Scope = strings.TrimSpace(stringFromAny(v["scope"]))
			}
			identities[key] = identity
		}
		return identities
	}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		parts := strings.SplitN(strings.TrimSpace(entry), "|", 3)
		if len(parts) == 0 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		identity := APIKeyIdentity{}
		if len(parts) > 1 {
			identity.AccountID = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			identity.Scope = strings.TrimSpace(parts[2])
		}
		identities[key] = identity
	}
	return identities
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// Addr 返回 host:port
// splitHostPort 拆分监听地址为 host 与端口，解析失败时返回空 host 与 0
func splitHostPort(addr string) (string, int) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0
	}
	port := 0
	for _, ch := range portText {
		if ch < '0' || ch > '9' {
			return host, 0
		}
		port = port*10 + int(ch-'0')
	}
	return host, port
}

func (c Config) Addr() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// TokenRequired 是否启用旧版令牌校验
func (c Config) TokenRequired() bool {
	return len(c.Tokens) > 0
}

// APIAddr 返回新 API Server 监听地址
func (c Config) APIAddr() string {
	return c.APIHost + ":" + strconv.Itoa(c.APIPort)
}

// APIKeyRequired 新 API Server 是否配置了 API Key 白名单
func (c Config) APIKeyRequired() bool {
	return len(c.APIKeys) > 0 || len(c.APIKeyIdentities) > 0
}

// APIIdentityForKey 返回 API Key 绑定的账号/作用域；未绑定返回零值
func (c Config) APIIdentityForKey(apiKey string) APIKeyIdentity {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || len(c.APIKeyIdentities) == 0 {
		return APIKeyIdentity{}
	}
	return c.APIKeyIdentities[apiKey]
}

// APIUserIDOrDefault 返回 API 连接默认用户标识
func (c Config) APIUserIDOrDefault() string {
	if strings.TrimSpace(c.APIUserID) == "" {
		return defaultAPIUserID
	}
	return strings.TrimSpace(c.APIUserID)
}

func envBoolDefault(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envIntDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envInt64Default(raw string, fallback int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

// loadTLSConfig 解析 JSON 或简写证书配置
// 支持 {"cert_file":"...","key_file":"...","ca_certs":"...","verify":true}
func loadTLSConfig(raw, verifyRaw string) TLSConfig {
	cfg := TLSConfig{VerifyPeer: envBoolDefault(verifyRaw, false)}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg
	}
	if strings.HasPrefix(raw, "{") {
		var payload struct {
			CertFile string `json:"cert_file"`
			KeyFile  string `json:"key_file"`
			CAFile   string `json:"ca_certs"`
			CAFile2  string `json:"ca_file"`
			Verify   *bool  `json:"verify"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			cfg.CertFile = strings.TrimSpace(payload.CertFile)
			cfg.KeyFile = strings.TrimSpace(payload.KeyFile)
			cfg.CAFile = strings.TrimSpace(payload.CAFile)
			if cfg.CAFile == "" {
				cfg.CAFile = strings.TrimSpace(payload.CAFile2)
			}
			if payload.Verify != nil {
				cfg.VerifyPeer = *payload.Verify
			}
		}
		return cfg
	}
	cfg.CertFile = raw
	return cfg
}
