package webuiapi

import "github.com/go-chi/chi/v5"

// mountReasoningRoutes 对齐 ../MaiBot/src/webui/routers/reasoning_process.py 的推理过程浏览端点
func mountReasoningRoutes(r chi.Router, d Deps) {
	r.Get("/stages", d.handleReasoningStages)
	r.Delete("/stages/{stage}", d.handleReasoningClearStage)
	r.Get("/files", d.handleReasoningFiles)
	r.Get("/file", d.handleReasoningFile)
	r.Get("/image", d.handleReasoningImage)
	r.Get("/html", d.handleReasoningHTML)
	r.Get("/tool-call-records", d.handleReasoningToolCallRecords)
	r.Get("/tool-call-source", d.handleReasoningToolCallSource)
	r.Get("/related-records", d.handleReasoningRelatedRecords)
	r.Get("/active-context", d.handleReasoningActiveContext)
	r.Post("/import", d.handleReasoningImport)
	r.Post("/replay", d.handleReasoningReplay)
}
