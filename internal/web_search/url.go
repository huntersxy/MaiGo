package websearch

import (
	"net/url"
	"strings"
)

// ValidateURL 只接受无嵌入凭据的 HTTP(S) URL
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 8192 {
		return "", NewError("url 必须是非空字符串且不超过 8192 个字符")
	}
	parts, err := url.Parse(raw)
	if err != nil {
		return "", NewError("url 格式无效")
	}
	// 对齐 Python service.py:validate_url：协议、主机名与凭据合并为同一条校验与文案
	if (parts.Scheme != "http" && parts.Scheme != "https") || parts.Hostname() == "" || parts.User != nil {
		return "", NewError("url 必须是无用户名和密码的 HTTP(S) 地址")
	}
	return raw, nil
}
