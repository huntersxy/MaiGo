// Package htmlrender 对齐 MaiBot src/services/html_render_service（HTML→PNG）。
//
// 两种后端按配置选择：宿主浏览器（CDP 驱动 Chrome/Chromium/Edge，纯 Go）或
// HTTP 侧车（MAIBOT_HTMLRENDER_URL 指向 Playwright 包装服务）。渲染时拦截
// 非 about/blob/data/file 的请求，对齐 Python 默认离线策略。
//
// 配置项与环境变量见 host_render 相关常量；未安装浏览器且未配置侧车时返回真实错误。
package htmlrender
