package webuiapi

// 推理过程记录导入，对齐 MaiBot reasoning_process.py 的 /import

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// handleReasoningImport 将单条结构化推理记录保存到独立的导入目录
func (d Deps) handleReasoningImport(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeReasoningFailure(w, http.StatusBadRequest, "文件不是有效的推理记录 JSON")
		return
	}
	valid := false
	for _, key := range []string{"request_items", "output_items", "messages"} {
		if _, ok := payload[key].([]any); ok {
			valid = true
			break
		}
	}
	if !valid {
		writeReasoningFailure(w, http.StatusBadRequest, "文件不是有效的推理记录 JSON")
		return
	}
	root, ok := d.reasoningPromptLogRoot()
	if !ok {
		writeInstanceDirMissing(w, "reasoning-process")
		return
	}
	importDir := filepath.Join(root, "imported", "导入")
	if err := os.MkdirAll(importDir, 0o777); err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "保存推理记录失败: "+err.Error())
		return
	}
	stem := fmt.Sprintf("%d-%s", time.Now().UnixMilli(), uuid.NewString())
	file, err := os.Create(filepath.Join(importDir, stem+".json"))
	if err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "保存推理记录失败: "+err.Error())
		return
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		writeReasoningFailure(w, http.StatusInternalServerError, "保存推理记录失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stage": "imported", "session": "导入", "stem": stem})
}
