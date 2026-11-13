package webuiapi

import "github.com/go-chi/chi/v5"

// mountDataTransferRoutes 注册 /api/webui/data-transfer 下的导入导出端点
func mountDataTransferRoutes(dr chi.Router, d Deps) {
	dr.Post("/export", d.handleDataTransferExport)
	dr.Get("/jobs/{job_id}", d.handleDataTransferJob)
	dr.Delete("/jobs/{job_id}", d.handleDataTransferDelete)
	dr.Get("/exports", d.handleDataTransferExports)
	dr.Get("/export/{job_id}/download", d.handleDataTransferDownload)
	dr.Post("/export/{job_id}/cancel", d.handleDataTransferCancel)
	dr.Post("/import", d.handleDataTransferImport)
}
