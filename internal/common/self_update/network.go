package selfupdate

import (
	"strings"

	"MaiGo/internal/common/network_security"
)

// validatePublicPrefix 校验自定义更新源前缀；requirePublicNetwork 为 false 时只做基础 URL 校验
func validatePublicPrefix(value string, requirePublicNetwork bool) (string, error) {
	normalized, err := networksecurity.ValidatePublicURL(strings.TrimSpace(value), networksecurity.Options{
		RequirePublicNetwork: requirePublicNetwork,
		AllowFakeIP:          true,
	})
	if err != nil {
		return "", errorf("invalid_source", "%s", err.Error())
	}
	return strings.TrimRight(normalized, "/"), nil
}
