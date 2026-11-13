// Package webuiapi 提供与 MaiBot Python WebUI（FastAPI）对齐的 HTTP API 骨架，
// 供 maibot-dashboard React 前端逐步对接 maibot-go。
//
// 主前缀：/api/webui（见 MaiBot src/webui/routes.py）
// 兼容前缀：/api/config、/api/chat、/api/*（memory compat，见 routers/__init__.py）
package webuiapi
