package webuiapi

import "github.com/go-chi/chi/v5"

// mountSystemRoutes 注册 /api/webui/system 下与 MaiBot system.py 对等的全部路由
func mountSystemRoutes(sr chi.Router, d Deps) {
	sr.Get("/status", d.handleSystemStatus)
	sr.Post("/request-restart", d.handleSystemRequestRestart)
	sr.Post("/restart", d.handleSystemRestart)

	sr.Get("/update-notice", d.handleSystemUpdateNotice)
	sr.Post("/update-notice/ack", d.handleSystemUpdateNoticeAck)
	sr.Get("/update-history", d.handleSystemUpdateHistory)

	sr.Get("/local-cache", d.handleSystemLocalCache)
	sr.Get("/local-cache/database", d.handleSystemDatabaseStats)
	sr.Post("/local-cache/database/vacuum", d.handleSystemDatabaseVacuum)
	sr.Get("/local-cache/data-entries", d.handleSystemDataEntries)
	sr.Delete("/local-cache/data-entries", d.handleSystemDataEntryDelete)
	sr.Get("/local-cache/images", d.handleSystemCacheImages)
	sr.Get("/local-cache/images/preview", d.handleSystemCacheImagePreview)
	sr.Get("/maisaka-monitor/media/{media_kind}/{media_hash}", d.handleSystemMaisakaMonitorMedia)
	sr.Delete("/local-cache/images", d.handleSystemCacheImageDelete)
	sr.Delete("/local-cache/images/bulk", d.handleSystemCacheImagesBulkDelete)
	sr.Get("/local-cache/log-directories", d.handleSystemLogDirectories)
	sr.Delete("/local-cache/log-directories", d.handleSystemLogDirectoryDelete)
	sr.Post("/local-cache/cleanup", d.handleSystemCacheCleanup)
	sr.Post("/reload-config", d.handleSystemReloadConfig)
}
