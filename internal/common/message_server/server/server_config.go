package server

import (
	"slices"
	"strings"

	"go.uber.org/zap"
)

// config 返回当前配置快照；切片与 map 只在 ApplyConfig 内整体替换，旧快照可安全读取
func (s *Server) config() Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// Config 返回当前配置快照
func (s *Server) Config() Config {
	return s.config()
}

// ApplyConfig 应用热重载配置并返回必须重启才能生效的字段
// 鉴权列表、API 用户与消息大小上限即时生效；监听地址与 TLS 等保持旧监听
func (s *Server) ApplyConfig(next Config) []string {
	next.Tokens = normalizeStringList(next.Tokens)
	next.APIKeys = normalizeStringList(next.APIKeys)
	next.APIKeyIdentities = cloneAPIKeyIdentities(next.APIKeyIdentities)
	next.APIUserID = strings.TrimSpace(next.APIUserID)
	if next.APIUserID == "" {
		next.APIUserID = defaultAPIUserID
	}
	if next.APIMaxMessageSize <= 0 {
		next.APIMaxMessageSize = defaultAPIMaxMessage
	}
	for key := range next.APIKeyIdentities {
		if !slices.Contains(next.APIKeys, key) {
			next.APIKeys = append(next.APIKeys, key)
		}
	}

	s.cfgMu.Lock()
	prev := s.cfg
	s.cfg.Tokens = next.Tokens
	s.cfg.APIKeys = next.APIKeys
	s.cfg.APIKeyIdentities = next.APIKeyIdentities
	s.cfg.APIUserID = next.APIUserID
	s.cfg.APIMaxMessageSize = next.APIMaxMessageSize
	s.cfgMu.Unlock()

	ignored := listenerConfigChanges(prev, next)
	if len(ignored) > 0 {
		zap.L().Warn("maim_message 配置变更需要重启后生效",
			zap.Strings("fields", ignored))
	}
	return ignored
}

func cloneAPIKeyIdentities(source map[string]APIKeyIdentity) map[string]APIKeyIdentity {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]APIKeyIdentity, len(source))
	for key, identity := range source {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		cloned[key] = identity
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func listenerConfigChanges(prev, next Config) []string {
	fields := make([]string, 0, 16)
	if prev.Enabled != next.Enabled {
		fields = append(fields, "enabled")
	}
	if prev.Host != next.Host {
		fields = append(fields, "ws_server_host")
	}
	if prev.Port != next.Port {
		fields = append(fields, "ws_server_port")
	}
	if prev.LegacySSLEnabled != next.LegacySSLEnabled {
		fields = append(fields, "legacy_ssl_enabled")
	}
	if prev.LegacySSLConfig != next.LegacySSLConfig {
		fields = append(fields, "legacy_ssl")
	}
	if prev.APIEnabled != next.APIEnabled {
		fields = append(fields, "enable_api_server")
	}
	if prev.APIHost != next.APIHost {
		fields = append(fields, "api_server_host")
	}
	if prev.APIPort != next.APIPort {
		fields = append(fields, "api_server_port")
	}
	if prev.APIPath != next.APIPath {
		fields = append(fields, "api_server_path")
	}
	if prev.APISSLEnabled != next.APISSLEnabled {
		fields = append(fields, "api_server_use_wss")
	}
	if prev.APISSLConfig != next.APISSLConfig {
		fields = append(fields, "api_server_ssl")
	}
	if prev.APIStatsEnabled != next.APIStatsEnabled {
		fields = append(fields, "api_stats_enabled")
	}
	if prev.APIMessageCacheEnabled != next.APIMessageCacheEnabled {
		fields = append(fields, "api_message_cache_enabled")
	}
	if prev.APIMessageCacheTTL != next.APIMessageCacheTTL {
		fields = append(fields, "api_message_cache_ttl")
	}
	if prev.APIMessageCacheMaxSize != next.APIMessageCacheMaxSize {
		fields = append(fields, "api_message_cache_max_size")
	}
	if prev.APIMessageCacheCleanup != next.APIMessageCacheCleanup {
		fields = append(fields, "api_message_cache_cleanup_interval")
	}
	return fields
}
