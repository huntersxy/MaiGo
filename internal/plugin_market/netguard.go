package pluginmarket

import "maibot-go/internal/common/network_security"

// urlGuardOptions 保留插件市场内部名称，实际实现位于公共出站校验包
type urlGuardOptions = networksecurity.Options

// nonPublicAddressError 保留插件市场内部名称，便于错误分支判定
type nonPublicAddressError = networksecurity.NonPublicAddressError

// validatePublicURL 对齐 common.network_security.validate_public_url
func validatePublicURL(rawURL string, options urlGuardOptions) (string, error) {
	return networksecurity.ValidatePublicURL(rawURL, options)
}
