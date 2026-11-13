package htmlrender

import "errors"

// ErrNotConfigured 未配置 MAIBOT_HTMLRENDER_URL 侧车
var ErrNotConfigured = errors.New("htmlrender: 未配置 MAIBOT_HTMLRENDER_URL 渲染侧车")
