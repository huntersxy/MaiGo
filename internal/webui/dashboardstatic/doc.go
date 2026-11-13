// Package dashboardstatic 在检测到 maibot-dashboard 构建产物时提供原版 SPA 静态服务。
//
// 对齐 Python src/webui/app.py _setup_static_files：根路径 / 直接返回 dist/index.html，
// 未命中的 GET 先找静态文件、找不到回落 index.html（/api 前缀除外）；/assets/* 由
// Dashboard dist 与 templ 后台资源合并提供。历史入口 /dashboard/ 保留为别名。
//
// 目录解析顺序见 ResolveRoot；显式设置 MAIBOT_DASHBOARD_DIR 时仅用该路径。
package dashboardstatic
