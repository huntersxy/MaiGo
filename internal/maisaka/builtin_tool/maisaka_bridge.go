package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/common/database"
	"MaiGo/internal/person_info"
)

func memorixHostReady(ctx context.Context) (memorix.Host, bool) {
	host := memorix.DefaultRegistry().Host()
	if host == nil {
		return nil, false
	}
	health, err := host.Health(ctx)
	if err != nil {
		return nil, false
	}
	// Python memory_service.search 无条件调用 Host：未启用（_disabled_response）与
	// 初始化失败（_unavailable_response，success=false+error）都由 Host 返回，
	// 不会回退 PG 直查；只有完全没有 Host 时才允许 searchMemoryV2 直查
	if health.Backend == "none" && !health.Disabled && health.StartupState != "failed" {
		return nil, false
	}
	if health.Disabled || health.StartupState == "failed" || health.MemoryEnabled {
		return host, true
	}
	return nil, false
}

func buildMemorixSearchRequest(
	ctx context.Context,
	query string,
	limit int,
	mode string,
	personID string,
	timeStart string,
	timeEnd string,
	respectFilter bool,
) memorix.SearchRequest {
	req := memorix.SearchRequest{
		Query:         query,
		Limit:         limit,
		Mode:          mode,
		PersonID:      personID,
		RespectFilter: respectFilter,
	}
	if timeStart != "" {
		req.TimeStart = timeStart
	}
	if timeEnd != "" {
		req.TimeEnd = timeEnd
	}
	tc := GetToolContext(ctx)
	if tc == nil {
		return req
	}
	switch tc.ConversationKind {
	case memory.ConversationKindGroup:
		req.GroupID = strconv.FormatInt(tc.TargetID, 10)
	case memory.ConversationKindPrivate:
		req.UserID = strconv.FormatInt(tc.TargetID, 10)
	}
	return req
}

func normalizePersonProfileLimit(raw int) int {
	if raw <= 0 {
		return 8
	}
	if raw > 20 {
		return 20
	}
	return raw
}

// personPlatform 把当前会话平台映射到人物画像平台，缺省回退 QQ
func personPlatform(platform string) string {
	platform = strings.TrimSpace(platform)
	if platform == "" {
		return personinfo.PlatformQQ
	}
	return platform
}

func formatPersonProfileSummary(prof *personinfo.Profile) (summary string, traits []string) {
	if prof == nil {
		return "未找到可用的人物画像", nil
	}
	var b strings.Builder
	name := strings.TrimSpace(prof.PersonName)
	if name == "" {
		name = strings.TrimSpace(prof.Nickname)
	}
	if name != "" {
		b.WriteString("称呼：")
		b.WriteString(name)
		b.WriteByte('\n')
	}
	if nick := strings.TrimSpace(prof.Nickname); nick != "" && nick != name {
		b.WriteString("昵称：")
		b.WriteString(nick)
		b.WriteByte('\n')
	}
	b.WriteString(fmt.Sprintf("是否已知：%v；认识次数：%d\n", prof.IsKnown, prof.KnowCounts))
	if len(prof.GroupCards) > 0 {
		b.WriteString("群名片：")
		for i, card := range prof.GroupCards {
			if i > 0 {
				b.WriteString("；")
			}
			b.WriteString(card.GroupID)
			b.WriteString("=")
			b.WriteString(card.GroupCardname)
		}
		b.WriteByte('\n')
	}
	summary = strings.TrimSpace(b.String())
	if summary == "" {
		summary = "未找到可用的人物画像"
	}
	return summary, profileTraitsFromSummary(summary)
}

// profileTraitsFromSummary 对齐 Python _extract_traits：逐行去空白与首尾 "-"，最多 8 行
func profileTraitsFromSummary(summary string) []string {
	traits := make([]string, 0, 8)
	for _, line := range strings.Split(summary, "\n") {
		clean := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "-"))
		if clean != "" {
			traits = append(traits, clean)
		}
		if len(traits) >= 8 {
			break
		}
	}
	return traits
}

func tokenizeMemoryQuery(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	fields := strings.Fields(query)
	if len(fields) <= 1 {
		return []string{query}
	}
	return fields
}
