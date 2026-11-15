package tools

import (
	"context"
	"strings"

	"MaiGo/internal/computer"

	"github.com/cloudwego/eino/components/tool"
)

// ToolCatalogEntry 当前会话 Generation 已注册的工具名与描述
type ToolCatalogEntry struct {
	Name        string
	Description string
	Deferred    bool
}

// BuildToolCatalog 从 ADK 工具列表提取名称与描述，computer_* 在本地模式标记为 deferred，插件工具按自身可见性标记
func BuildToolCatalog(ctx context.Context, chatTools []tool.BaseTool) []ToolCatalogEntry {
	if len(chatTools) == 0 {
		return nil
	}
	out := make([]ToolCatalogEntry, 0, len(chatTools))
	seen := map[string]struct{}{}
	for _, item := range chatTools {
		if item == nil {
			continue
		}
		info, err := item.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		if !DesktopPetToolVisible(ctx, name) {
			continue
		}
		seen[name] = struct{}{}
		desc := strings.TrimSpace(info.Desc)
		deferred := isDeferredToolName(name)
		if marker, ok := item.(deferredToolMarker); ok && marker.IsDeferredTool() {
			deferred = true
		}
		out = append(out, ToolCatalogEntry{
			Name:        name,
			Description: desc,
			Deferred:    deferred,
		})
	}
	return out
}

func isDeferredToolName(name string) bool {
	switch name {
	case string(computer.ToolDesktop), string(computer.ToolObserve),
		string(computer.ToolPoll), string(computer.ToolCancel):
		return computer.ToolsEnabled()
	default:
		return false
	}
}

// SearchToolCatalog 按 MaiBot tool_search 同款打分规则在目录中检索
func SearchToolCatalog(catalog []ToolCatalogEntry, query string, limit int) []ToolCatalogEntry {
	normalizedQuery := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(query))), " ")
	if normalizedQuery == "" || len(catalog) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	queryTerms := strings.Fields(strings.NewReplacer("_", " ", "-", " ").Replace(normalizedQuery))

	type scored struct {
		score int
		name  string
		entry ToolCatalogEntry
	}
	var matches []scored
	for _, entry := range catalog {
		lowerName := strings.ToLower(entry.Name)
		lowerDesc := strings.ToLower(entry.Description)
		score := 0
		if normalizedQuery == lowerName {
			score += 1000
		}
		if strings.HasPrefix(lowerName, normalizedQuery) {
			score += 300
		}
		if strings.Contains(lowerName, normalizedQuery) {
			score += 200
		}
		if strings.Contains(lowerDesc, normalizedQuery) {
			score += 100
		}
		for _, term := range queryTerms {
			if strings.Contains(lowerName, term) {
				score += 25
			}
			if strings.Contains(lowerDesc, term) {
				score += 10
			}
		}
		if score <= 0 {
			continue
		}
		matches = append(matches, scored{score: score, name: entry.Name, entry: entry})
	}
	if len(matches) == 0 {
		return nil
	}
	for i := 0; i < len(matches); i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score > matches[i].score ||
				(matches[j].score == matches[i].score && matches[j].name < matches[i].name) {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]ToolCatalogEntry, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.entry)
	}
	return out
}

// DiscoverDeferredTools 将 deferred 工具标记为已发现，返回本次新发现名称
func (tc *ToolContext) DiscoverDeferredTools(names []string) []string {
	if tc == nil {
		return nil
	}
	deferred := map[string]struct{}{}
	for _, entry := range tc.ToolCatalog {
		if entry.Deferred {
			deferred[entry.Name] = struct{}{}
		}
	}
	if len(deferred) == 0 {
		return nil
	}
	tc.discoveredMu.Lock()
	defer tc.discoveredMu.Unlock()
	if tc.DiscoveredTools == nil {
		tc.DiscoveredTools = map[string]struct{}{}
	}
	var newly []string
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if _, ok := deferred[name]; !ok {
			continue
		}
		if _, seen := tc.DiscoveredTools[name]; seen {
			continue
		}
		tc.DiscoveredTools[name] = struct{}{}
		newly = append(newly, name)
	}
	return newly
}
