package memorix

import (
	"strings"

	"go.uber.org/zap"
)

// HostBackend 当前 Registry 注入的后端类型
type HostBackend string

const (
	HostBackendDisabled HostBackend = "disabled"
	HostBackendRemote   HostBackend = "remote"
)

// ConfigureDefaultHost 按环境选择 Host：MAIBOT_MEMORIX_REMOTE_URL 优先 RemoteHost，否则 DisabledHost
func ConfigureDefaultHost() (HostBackend, string) {
	remoteURL := RemoteURLFromEnv()
	if remoteURL != "" {
		host, err := NewRemoteHost(remoteURL, RemoteHostOptionsFromEnv())
		if err != nil {
			zap.L().Warn("Memorix 远程 Host 初始化失败，回退 Disabled",
				zap.String("remote_url", remoteURL),
				zap.Error(err))
		} else {
			DefaultRegistry().SetHost(host)
			return HostBackendRemote, remoteURL
		}
	}
	DefaultRegistry().SetHost(DisabledHost{})
	return HostBackendDisabled, ""
}

// RemoteConfigured 是否设置了远程 URL（不校验可达性）
func RemoteConfigured() bool {
	return strings.TrimSpace(RemoteURLFromEnv()) != ""
}
