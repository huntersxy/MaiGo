package memorix

import (
	"context"
	"strings"

	maibotcfg "MaiGo/internal/config/maibot"
)

// SharedMemorySessionResolver 按配置解析共享长期记忆的会话集合
// 对齐 Python src/common/utils/utils_config.py:623-640 get_shared_memory_session_ids
type SharedMemorySessionResolver struct {
	// Groups 共享记忆分组配置
	Groups []maibotcfg.ChatStreamGroup
	// ResolveSessionIDs 按平台/目标/类型解析已知真实会话 ID
	ResolveSessionIDs func(ctx context.Context, platform, targetID, chatType string) ([]string, error)
}

// Resolve 返回与当前会话共享长期记忆检索范围的会话集合
// 未命中任何分组时回退为仅当前会话，对齐 Python 的 resolved_session_ids or {clean_session_id}
func (r SharedMemorySessionResolver) Resolve(ctx context.Context, sessionID string) []string {
	cleaned := strings.TrimSpace(sessionID)
	if cleaned == "" {
		return nil
	}
	if r.ResolveSessionIDs == nil || len(r.Groups) == 0 {
		return []string{cleaned}
	}
	resolved := make(map[string]struct{})
	for _, group := range r.Groups {
		groupIDs := make(map[string]struct{})
		for _, target := range group.Targets {
			platform := strings.TrimSpace(target.Platform)
			itemID := strings.TrimSpace(target.ItemId)
			ruleType := strings.TrimSpace(target.RuleType)
			if platform == "" || itemID == "" || ruleType == "" {
				continue
			}
			ids, err := r.ResolveSessionIDs(ctx, platform, itemID, ruleType)
			if err != nil {
				continue
			}
			for _, id := range ids {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					groupIDs[trimmed] = struct{}{}
				}
			}
		}
		if _, hit := groupIDs[cleaned]; !hit {
			continue
		}
		for id := range groupIDs {
			resolved[id] = struct{}{}
		}
	}
	if len(resolved) == 0 {
		return []string{cleaned}
	}
	out := make([]string, 0, len(resolved))
	for id := range resolved {
		out = append(out, id)
	}
	return out
}
