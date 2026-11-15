package webuiapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"MaiGo/internal/learners"
)

type legacyExpressionImportMapping struct {
	OldChatID     string   `json:"old_chat_id"`
	TargetChatID  *string  `json:"target_chat_id"`
	TargetChatIDs []string `json:"target_chat_ids"`
}

type legacyExpressionImportRequest struct {
	DBPath   string                          `json:"db_path"`
	Mappings []legacyExpressionImportMapping `json:"mappings"`
}

func (d Deps) requireExistingLegacyChatID(ctx context.Context, store learners.WebUIStore, raw string) (string, error) {
	chatID := strings.TrimSpace(raw)
	if chatID == "" {
		return "", legacyFail(http.StatusBadRequest, "缺少聊天流 ID")
	}
	if !store.ExistingSession(ctx, chatID) {
		return "", legacyFail(http.StatusBadRequest, "聊天流不存在: "+chatID)
	}
	return chatID, nil
}

func (d Deps) cleanupLegacyUpload(dbPath string) {
	uploadDir := d.legacyUploadDir()
	if uploadDir == "" || strings.TrimSpace(dbPath) == "" {
		return
	}
	uploadAbs, err := filepath.Abs(uploadDir)
	if err != nil {
		return
	}
	pathAbs, err := filepath.Abs(resolveLegacyDatabasePath(dbPath))
	if err != nil {
		return
	}
	rel, err := filepath.Rel(uploadAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return
	}
	if info, err := os.Stat(pathAbs); err == nil && info.Mode().IsRegular() {
		_ = os.Remove(pathAbs)
	}
}

func legacyExistingPairs(ctx context.Context, store learners.WebUIStore, chatIDs []string) (map[string]map[[2]string]struct{}, error) {
	out := make(map[string]map[[2]string]struct{}, len(chatIDs))
	for _, chatID := range chatIDs {
		rows, err := store.ExportExpressions(ctx, learners.ExpressionExportQuery{ChatID: chatID, HasChatID: true})
		if err != nil {
			return nil, err
		}
		pairs := make(map[[2]string]struct{}, len(rows))
		for _, row := range rows {
			pairs[[2]string{strings.TrimSpace(row.Situation), strings.TrimSpace(row.Style)}] = struct{}{}
		}
		out[chatID] = pairs
	}
	return out, nil
}

func (d Deps) handleLegacyExpressionImport(w http.ResponseWriter, r *http.Request) {
	const path = "/api/webui/expression/legacy-import/import"
	if !d.requireAuth(w, r) {
		return
	}
	store, ok := d.requireLearnerStore(w, path, "记忆管理器未初始化")
	if !ok {
		return
	}
	var request legacyExpressionImportRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeValidationError(w, "请求格式错误")
		return
	}
	mappingByOldChatID := map[string][]string{}
	for _, mapping := range request.Mappings {
		oldChatID := strings.TrimSpace(mapping.OldChatID)
		if oldChatID == "" {
			continue
		}
		targets := make([]string, 0, len(mapping.TargetChatIDs)+1)
		if len(mapping.TargetChatIDs) > 0 {
			targets = append(targets, mapping.TargetChatIDs...)
		} else if mapping.TargetChatID != nil {
			targets = append(targets, *mapping.TargetChatID)
		}
		validTargets := make([]string, 0, len(targets))
		seen := map[string]struct{}{}
		for _, target := range targets {
			validChatID, err := d.requireExistingLegacyChatID(r.Context(), store, target)
			if err != nil {
				writeLegacyError(w, err, "导入旧版表达方式失败: ")
				return
			}
			if _, exists := seen[validChatID]; exists {
				continue
			}
			seen[validChatID] = struct{}{}
			validTargets = append(validTargets, validChatID)
		}
		if len(validTargets) > 0 {
			mappingByOldChatID[oldChatID] = validTargets
		}
	}
	if len(mappingByOldChatID) == 0 {
		writeAPIError(w, http.StatusBadRequest, "没有可导入的聊天映射")
		return
	}
	db, err := openLegacySQLite(request.DBPath)
	if err != nil {
		writeLegacyError(w, err, "读取旧版表达方式数据库失败: ")
		return
	}
	defer db.Close()
	rows, columns, err := loadLegacyExpressions(r.Context(), db)
	if err != nil {
		writeLegacyError(w, err, "读取旧版表达方式数据库失败: ")
		return
	}
	targetIDs := make([]string, 0)
	seenTargets := map[string]struct{}{}
	for _, targets := range mappingByOldChatID {
		for _, target := range targets {
			if _, exists := seenTargets[target]; exists {
				continue
			}
			seenTargets[target] = struct{}{}
			targetIDs = append(targetIDs, target)
		}
	}
	existingPairs, err := legacyExistingPairs(r.Context(), store, targetIDs)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "导入旧版表达方式失败: "+err.Error())
		return
	}
	importedCount := 0
	skippedCount := 0
	failedCount := 0
	ignoredOldChatIDs := map[string]struct{}{}
	for _, row := range rows {
		oldChatID := legacyRowChatID(row, columns)
		targets := mappingByOldChatID[oldChatID]
		if len(targets) == 0 {
			if oldChatID != "" {
				ignoredOldChatIDs[oldChatID] = struct{}{}
			}
			continue
		}
		situation := strings.TrimSpace(legacySQLString(row["situation"]))
		style := strings.TrimSpace(legacySQLString(row["style"]))
		if situation == "" || style == "" {
			failedCount++
			continue
		}
		legacyChecked := legacySQLBool(row["checked"], false)
		legacyRejected := legacySQLBool(row["rejected"], false)
		if legacyChecked && legacyRejected {
			skippedCount++
			continue
		}
		item := learners.ExpressionImportItem{
			Situation:   situation,
			Style:       style,
			ContentList: legacyContentList(row["content_list"]),
			Count:       legacySQLInt(row["count"], 0),
			LastActiveTime: func() *time.Time {
				value := legacySQLTime(row["last_active_time"])
				return &value
			}(),
			CreateTime: func() *time.Time {
				value := legacySQLTime(row["create_date"])
				return &value
			}(),
			Checked:    legacyChecked,
			ModifiedBy: legacyModifiedBy(row["modified_by"]),
		}
		// create_date 字段不存在时 time.Time 仍为当前时间，与 Python 的 fallback_now 一致
		key := [2]string{situation, style}
		for _, target := range targets {
			if _, exists := existingPairs[target][key]; exists {
				skippedCount++
				continue
			}
			result, err := store.ImportExpressions(r.Context(), &target, []learners.ExpressionImportItem{item})
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "导入旧版表达方式失败: "+err.Error())
				return
			}
			if result.Failed > 0 {
				failedCount += result.Failed
				continue
			}
			if result.Skipped > 0 {
				skippedCount += result.Skipped
				continue
			}
			importedCount += result.Imported
			existingPairs[target][key] = struct{}{}
		}
	}
	message := fmt.Sprintf(
		"旧版导入完成：成功 %d 个，跳过 %d 个，失败 %d 个，未导入分组 %d 个",
		importedCount, skippedCount, failedCount, len(ignoredOldChatIDs),
	)
	d.cleanupLegacyUpload(request.DBPath)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":             true,
		"message":             message,
		"imported_count":      importedCount,
		"skipped_count":       skippedCount,
		"failed_count":        failedCount,
		"ignored_group_count": len(ignoredOldChatIDs),
	})
}
