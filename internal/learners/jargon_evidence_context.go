package learners

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// evidenceMessageLoader 由 GORMStore 可选实现；未实现时无法还原证据上下文
type evidenceMessageLoader interface {
	LoadJargonEvidenceMessage(ctx context.Context, platform, messageID string) (string, bool)
	UpdateJargonEvidenceMessages(ctx context.Context, id uint, evidence *string) error
}

// loadEvidenceContexts 对齐 _load_evidence_contexts：任何一条缺失即整组丢弃，清理后写回
func loadEvidenceContexts(ctx context.Context, svc *JargonService, rec *JargonRecord) (contexts []string, cleaned *string, changed bool) {
	if rec == nil {
		return nil, nil, false
	}
	original := rec.EvidenceMessages
	groups := ParseJargonEvidenceGroups(rec.EvidenceMessages)
	if len(groups) == 0 {
		return nil, nil, false
	}
	loader, ok := svc.reg.Store().(evidenceMessageLoader)
	if !ok {
		return nil, nil, false
	}
	validGroups := make([]JargonEvidenceGroup, 0, len(groups))
	removed := 0
	for _, group := range groups {
		validGroup := make(JargonEvidenceGroup, 0, len(group))
		lines := make([]string, 0, len(group))
		missing := false
		for _, ref := range group {
			text, ok := loader.LoadJargonEvidenceMessage(ctx, ref.Platform, ref.MessageID)
			if !ok {
				missing = true
				break
			}
			// 消息存在但无学习正文时只跳过该引用，组内后续有效消息仍保留
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			validGroup = append(validGroup, ref)
			lines = append(lines, "["+jargonItoa(len(lines)+1)+"] "+text)
		}
		if missing {
			removed++
			continue
		}
		contextText := strings.Join(lines, "\n")
		if contextText == "" {
			removed++
			continue
		}
		contexts = append(contexts, contextText)
		validGroups = append(validGroups, validGroup)
	}
	cleaned = EncodeJargonEvidenceGroups(validGroups)
	cleanedValue := ""
	if cleaned != nil {
		cleanedValue = *cleaned
	}
	changed = removed > 0 || cleanedValue != original
	return contexts, cleaned, changed
}

// cleanupEvidenceMessages 写回清理后的证据组；nil 表示清空
func cleanupEvidenceMessages(ctx context.Context, svc *JargonService, rec *JargonRecord, cleaned *string) {
	if rec == nil || rec.ID == 0 {
		return
	}
	loader, ok := svc.reg.Store().(evidenceMessageLoader)
	if !ok {
		return
	}
	if err := loader.UpdateJargonEvidenceMessages(ctx, rec.ID, cleaned); err != nil {
		zap.L().Warn("黑话证据消息清理失败",
			zap.Uint("jargon_id", rec.ID),
			zap.Error(err))
		return
	}
	rec.EvidenceMessages = ""
	if cleaned != nil {
		rec.EvidenceMessages = *cleaned
	}
}
