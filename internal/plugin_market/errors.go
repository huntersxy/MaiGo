package pluginmarket

import (
	"errors"
	"fmt"
)

// MarketError 对齐 plugin_market.errors.MarketError：业务错误自带 HTTP 状态码
type MarketError struct {
	StatusCode int
	Message    string
}

// Error 输出业务错误文案
func (e *MarketError) Error() string { return e.Message }

// NewMarketError 构造业务错误
func NewMarketError(statusCode int, message string) error {
	return &MarketError{StatusCode: statusCode, Message: message}
}

// AsMarketError 提取业务错误，WebUI 层据此映射 HTTP 状态码
func AsMarketError(err error) (*MarketError, bool) {
	var target *MarketError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// HTTPStatusError 对齐 urllib.error.HTTPError：保留状态码供调用方判定 404
type HTTPStatusError struct {
	Code   int
	URL    string
	Reason string
}

// Error 输出与 urllib 一致的错误文案
func (e *HTTPStatusError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = httpStatusText(e.Code)
	}
	return fmt.Sprintf("HTTP Error %d: %s", e.Code, reason)
}

// AsHTTPStatusError 提取 HTTP 状态错误
func AsHTTPStatusError(err error) (*HTTPStatusError, bool) {
	var target *HTTPStatusError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// ErrInvalidDownloadURL 下载地址无效
var ErrInvalidDownloadURL = errors.New("pluginmarket: 无效的下载地址")

// ErrPluginNotFound 当前市场快照中找不到插件
var ErrPluginNotFound = errors.New("pluginmarket: 未找到插件")
