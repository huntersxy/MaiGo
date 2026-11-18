package bootstrap

// 推理预览目录名按 chat_sessions 解析为平台会话身份，对齐 Python build_preview_chat_dir_name。

import (
	"context"
	"strings"
	"time"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/maisaka/display"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// wireReasoningPreviewSessionLookup 注入预览目录的会话解析器，并归一历史哈希目录
func wireReasoningPreviewSessionLookup(db *gorm.DB) {
	if db == nil {
		return
	}
	sessions := chatsession.NewManager(db)
	display.SetPreviewSessionLookup(func(sessionID string) (display.PreviewSessionInfo, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		record, err := sessions.GetExistingBySessionID(ctx, sessionID)
		if err != nil || record == nil {
			return display.PreviewSessionInfo{}, false
		}
		info := display.PreviewSessionInfo{Platform: strings.TrimSpace(record.Platform)}
		if record.GroupID != nil {
			if groupID := strings.TrimSpace(*record.GroupID); groupID != "" {
				info.IsGroup = true
				info.GroupID = groupID
			}
		}
		if !info.IsGroup && record.UserID != nil {
			info.UserID = strings.TrimSpace(*record.UserID)
		}
		if info.Platform == "" || (!info.IsGroup && info.UserID == "") {
			return display.PreviewSessionInfo{}, false
		}
		return info, true
	})
	migrated, err := display.DefaultPromptPreviewLogger().MigrateLegacySessionDirs()
	if err != nil {
		zap.L().Warn("迁移推理预览会话目录失败", zap.Int("migrated", migrated), zap.Error(err))
		return
	}
	if migrated > 0 {
		zap.L().Info("推理预览会话目录已按平台命名", zap.Int("migrated", migrated))
	}
}
