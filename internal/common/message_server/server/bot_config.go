package server

import (
	"os"
	"strconv"
	"strings"
	"time"

	maibotcfg "maibot-go/internal/config/maibot"
)

const (
	defaultAPIHost = "0.0.0.0"
	defaultAPIPort = 8090
)

// LoadConfigFromMaiBot 从 bot_config.toml 的 [maim_message] 构造配置
// TOML 为权威来源；Go 环境变量只在 TOML 仍为默认值时兼容覆盖
func LoadConfigFromMaiBot(bot maibotcfg.MaimMessageConfig) Config {
	defaults := maiBotDefaults()
	cfg := defaults
	cfg.Enabled = bot.Enabled
	if host := strings.TrimSpace(bot.WsServerHost); host != "" {
		cfg.Host = host
	}
	if bot.WsServerPort > 0 {
		cfg.Port = bot.WsServerPort
	}
	cfg.Tokens = normalizeStringList(bot.AuthToken)
	cfg.APIEnabled = bot.EnableApiServer
	if host := strings.TrimSpace(bot.ApiServerHost); host != "" {
		cfg.APIHost = host
	}
	if bot.ApiServerPort > 0 {
		cfg.APIPort = bot.ApiServerPort
	}
	cfg.APIKeys = normalizeStringList(bot.ApiServerAllowedApiKeys)
	cfg.APISSLEnabled = bot.ApiServerUseWss
	cfg.APISSLConfig = TLSConfig{
		CertFile: strings.TrimSpace(bot.ApiServerCertFile),
		KeyFile:  strings.TrimSpace(bot.ApiServerKeyFile),
	}
	applyEnvOverrides(&cfg, defaults)
	return cfg
}

func legacyDefaults() Config {
	return Config{
		Host:                   defaultHost,
		Port:                   defaultPort,
		APIHost:                defaultHost,
		APIPort:                defaultPort + 1,
		APIPath:                defaultAPIPath,
		APIUserID:              defaultAPIUserID,
		APIStatsEnabled:        true,
		APIMessageCacheEnabled: true,
		APIMessageCacheTTL:     defaultAPICacheTTL,
		APIMessageCacheMaxSize: defaultAPICacheSize,
		APIMessageCacheCleanup: defaultAPICacheClean,
		APIMaxMessageSize:      defaultAPIMaxMessage,
	}
}

func maiBotDefaults() Config {
	return Config{
		Enabled:                true,
		Host:                   defaultHost,
		Port:                   defaultPort,
		APIHost:                defaultAPIHost,
		APIPort:                defaultAPIPort,
		APIPath:                defaultAPIPath,
		APIUserID:              defaultAPIUserID,
		APIStatsEnabled:        true,
		APIMessageCacheEnabled: true,
		APIMessageCacheTTL:     defaultAPICacheTTL,
		APIMessageCacheMaxSize: defaultAPICacheSize,
		APIMessageCacheCleanup: defaultAPICacheClean,
		APIMaxMessageSize:      defaultAPIMaxMessage,
	}
}

// applyEnvOverrides 在 TOML 默认值上叠加 Go 兼容环境变量
// 与 Python legacy_migration 一致：TOML 非默认值优先，环境变量只填充默认值
func applyEnvOverrides(cfg *Config, defaults Config) {
	if cfg == nil {
		return
	}
	cfg.Enabled = envBoolOverride(envEnable, cfg.Enabled, defaults.Enabled)
	cfg.Host = envStringOverride(envHost, cfg.Host, defaults.Host)
	cfg.Port = envIntOverride(envPort, cfg.Port, defaults.Port)
	cfg.Tokens = envListOverride(envToken, cfg.Tokens)
	cfg.APIEnabled = envBoolOverride(envAPIEnable, cfg.APIEnabled, defaults.APIEnabled)
	cfg.APIHost = envStringOverride(envAPIHost, cfg.APIHost, defaults.APIHost)
	cfg.APIPort = envIntOverride(envAPIPort, cfg.APIPort, defaults.APIPort)
	cfg.APIPath = envStringOverride(envAPIPath, cfg.APIPath, defaults.APIPath)
	cfg.APIKeys = envListOverride(envAPIKeys, cfg.APIKeys)
	if v := strings.TrimSpace(os.Getenv(envAPIUserID)); v != "" {
		cfg.APIUserID = v
	}
	cfg.LegacySSLEnabled = envBoolOverride(envLegacySSLEnabled, cfg.LegacySSLEnabled, defaults.LegacySSLEnabled)
	if raw := strings.TrimSpace(os.Getenv(envLegacySSL)); raw != "" {
		cfg.LegacySSLConfig = loadTLSConfig(raw, os.Getenv(envLegacySSLVerify))
	}
	if raw := strings.TrimSpace(os.Getenv(envLegacySSLVerify)); raw != "" {
		cfg.LegacySSLConfig.VerifyPeer = envBoolDefault(raw, cfg.LegacySSLConfig.VerifyPeer)
	}
	if v := strings.TrimSpace(os.Getenv(envLegacySSLCert)); v != "" {
		cfg.LegacySSLConfig.CertFile = v
	}
	if v := strings.TrimSpace(os.Getenv(envLegacySSLKey)); v != "" {
		cfg.LegacySSLConfig.KeyFile = v
	}
	if v := strings.TrimSpace(os.Getenv(envLegacySSLCA)); v != "" {
		cfg.LegacySSLConfig.CAFile = v
	}
	cfg.APIKeyIdentities = loadAPIKeyIdentities(os.Getenv(envAPIIdentities))
	cfg.APISSLEnabled = envBoolOverride(envAPISSLEnabled, cfg.APISSLEnabled, defaults.APISSLEnabled)
	if raw := strings.TrimSpace(os.Getenv(envAPISSL)); raw != "" {
		cfg.APISSLConfig = loadTLSConfig(raw, os.Getenv(envAPISSLVerify))
	}
	if raw := strings.TrimSpace(os.Getenv(envAPISSLVerify)); raw != "" {
		cfg.APISSLConfig.VerifyPeer = envBoolDefault(raw, cfg.APISSLConfig.VerifyPeer)
	}
	if v := strings.TrimSpace(os.Getenv(envAPISSLCert)); v != "" {
		cfg.APISSLConfig.CertFile = v
	}
	if v := strings.TrimSpace(os.Getenv(envAPISSLKey)); v != "" {
		cfg.APISSLConfig.KeyFile = v
	}
	if v := strings.TrimSpace(os.Getenv(envAPISSLCA)); v != "" {
		cfg.APISSLConfig.CAFile = v
	}
	cfg.APIStatsEnabled = envBoolOverride(envAPIStatsEnable, cfg.APIStatsEnabled, defaults.APIStatsEnabled)
	cfg.APIMessageCacheEnabled = envBoolOverride(envAPICacheEnable, cfg.APIMessageCacheEnabled, defaults.APIMessageCacheEnabled)
	if seconds := envIntDefault(os.Getenv(envAPICacheTTL), 0); seconds > 0 {
		cfg.APIMessageCacheTTL = time.Duration(seconds) * time.Second
	}
	if size := envIntDefault(os.Getenv(envAPICacheSize), 0); size > 0 {
		cfg.APIMessageCacheMaxSize = size
	}
	if seconds := envIntDefault(os.Getenv(envAPICacheClean), 0); seconds > 0 {
		cfg.APIMessageCacheCleanup = time.Duration(seconds) * time.Second
	}
	if size := envInt64Default(os.Getenv(envAPIMaxMessage), 0); size > 0 {
		cfg.APIMaxMessageSize = size
	}
	for key := range cfg.APIKeyIdentities {
		if !containsString(cfg.APIKeys, key) {
			cfg.APIKeys = append(cfg.APIKeys, key)
		}
	}
}

func envStringOverride(envKey, current, defaultValue string) string {
	if current != defaultValue {
		return current
	}
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	return current
}

func envIntOverride(envKey string, current, defaultValue int) int {
	if current != defaultValue {
		return current
	}
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return current
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return current
	}
	return value
}

func envBoolOverride(envKey string, current, defaultValue bool) bool {
	if current != defaultValue {
		return current
	}
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return current
	}
	return envBoolDefault(raw, current)
}

func envListOverride(envKey string, current []string) []string {
	if len(current) > 0 {
		return current
	}
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return current
	}
	return splitEnvList(raw)
}

func splitEnvList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func normalizeStringList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if v := strings.TrimSpace(value); v != "" {
			out = append(out, v)
		}
	}
	return out
}
