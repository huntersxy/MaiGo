package plugin

import (
	"strings"

	"maibot-go/internal/common/network_security"
)

// validateFetchURL 校验出站 URL 是否允许访问，返回规范化后的地址
//
// 插件图标下载走同一套出站策略：必须命中公网校验（或显式允许私网）。
// 与父包 handlers_models.go 的同名方法语义一致。
func (d Deps) validateFetchURL(raw string, allowPrivate bool) (string, error) {
	normalized := strings.TrimSpace(raw)
	validated, err := networksecurity.ValidatePublicURL(normalized, d.networkSecurityOptions(allowPrivate, false))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(validated, "/"), nil
}
