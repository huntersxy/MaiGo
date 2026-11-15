package learners

import (
	"context"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/common/database"
)

// LoadJargonEvidenceMessage 按 platform+message_id 读取证据消息正文
// 对齐 _load_evidence_contexts：只取学习用顶层文字，空正文视为不可用
func (s *GORMStore) LoadJargonEvidenceMessage(ctx context.Context, platform, messageID string) (string, bool) {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return "", false
	}
	numericID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil {
		return "", false
	}
	platforms := evidencePlatformAliases(platform)
	var row memory.MessageLog
	query := s.db.WithContext(ctx).Where("message_id = ?", numericID)
	if len(platforms) > 0 {
		query = query.Where("platform IN ?", platforms)
	}
	if err := query.Order("id DESC").First(&row).Error; err != nil {
		return "", false
	}
	text := strings.TrimSpace(row.ProcessedPlainText)
	if text == "" {
		text = strings.TrimSpace(row.ProcessedPlainText)
	}
	// 空正文仍表示消息存在；调用方据此只跳过该引用，保留同组其他有效消息
	return text, true
}

// UpdateJargonEvidenceMessages 写回清理后的证据消息组；nil 表示清空
func (s *GORMStore) UpdateJargonEvidenceMessages(ctx context.Context, id uint, evidence *string) error {
	if id == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&Jargon{}).Where("id = ?", id).Updates(map[string]any{
		"evidence_messages": evidence,
		"updated_timestamp": time.Now(),
	}).Error
}

// mergeJargonEvidenceForUpdate 合并证据组；合并结果为空时保留原值（对齐 Python _update_jargon）
func mergeJargonEvidenceForUpdate(current *string, incoming []JargonEvidenceGroup) *string {
	currentRaw := ""
	if current != nil {
		currentRaw = *current
	}
	merged := MergeJargonEvidenceGroups(ParseJargonEvidenceGroups(currentRaw), incoming)
	if len(merged) == 0 {
		return current
	}
	return EncodeJargonEvidenceGroups(merged)
}

// evidencePlatformAliases 兼容 Go 侧 onebot 与 MaiBot 默认 qq 平台名
func evidencePlatformAliases(platform string) []string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		platform = "qq"
	}
	aliases := []string{platform}
	switch strings.ToLower(platform) {
	case "qq", "onebot":
		for _, alias := range []string{"qq", "onebot"} {
			if alias != platform {
				aliases = append(aliases, alias)
			}
		}
	}
	return aliases
}
