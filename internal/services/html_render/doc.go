// Package htmlrender 对齐 MaiBot src/services/html_render_service（HTML→PNG）。
//
// 渲染统一委托给 Python 原版 Playwright 侧车（MAIBOT_HTMLRENDER_URL）；
// Go 侧只负责请求规范化、并发限制与 HTTP 调用，不再维护 CDP 浏览器控制实现。
// 未配置侧车时返回真实错误，不静默降级到独立实现。
package htmlrender
