package focus

import (
	"fmt"
	"strings"
)

// MemberAttention 单个 Focus 成员在 Planner 中的「注意信息」
type MemberAttention struct {
	SessionID    string
	SessionName  string
	Instructions string
}

// BuildFocusRoster 对齐 FocusGroupRuntime._roster，供共享 Planner 注入
func BuildFocusRoster(members []MemberAttention) string {
	if len(members) == 0 {
		return ""
	}
	lines := make([]string, 0, len(members))
	for _, m := range members {
		if m.SessionID == "" {
			continue
		}
		name := strings.TrimSpace(m.SessionName)
		if name == "" {
			name = m.SessionID
		}
		extra := strings.TrimSpace(m.Instructions)
		if extra == "" {
			extra = "无额外注意信息"
		}
		lines = append(lines, fmt.Sprintf("在 %s(%s) 中的注意信息：%s", name, m.SessionID, extra))
	}
	return strings.Join(lines, "\n")
}
