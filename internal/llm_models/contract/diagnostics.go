package contract

// Provider 请求编号提取
// 供应商与中转网关的请求编号落在不同响应头，失败日志与快照只保留编号本身，
// 用于向供应商对账；取不到时为空，不影响错误分类。

import (
	"net/http"
	"strings"
	"unicode"
)

// providerRequestIDHeaders 常见上游请求编号响应头，按优先级取第一个非空值
var providerRequestIDHeaders = []string{
	"x-request-id",
	"request-id",
	"x-oneapi-request-id",
	"x-new-api-request-id",
	"x-amzn-requestid",
	"x-ds-trace-id",
	"cf-ray",
	"x-trace-id",
	"x-correlation-id",
}

// providerRequestIDMaxLen 请求编号保留长度上限，避免异常头撑大日志与快照
const providerRequestIDMaxLen = 128

// ProviderRequestID 从 Provider 响应头提取请求编号；缺失时返回空串
func ProviderRequestID(header http.Header) string {
	for _, name := range providerRequestIDHeaders {
		if value := sanitizeRequestID(header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func sanitizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		builder.WriteRune(r)
		if builder.Len() >= providerRequestIDMaxLen {
			break
		}
	}
	return builder.String()
}
