package runtime

import (
	"context"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// mcpBuiltinToolNames 收集 Agent 内置工具名（不含 MCP 与插件工具），用于 MCP 裸名避让
// 与 buildToolLists 使用同一组构造函数，避免维护第二份名单
func mcpBuiltinToolNames(ctx context.Context) []string {
	if ctx == nil {
		ctx = context.Background()
	}
	seen := make(map[string]struct{})
	builders := append(groupToolBuilders(), privateToolBuilders()...)
	for _, build := range builders {
		item, err := build()
		if err != nil {
			zap.L().Warn("收集 MCP 保留工具名时构建内置工具失败", zap.Error(err))
			continue
		}
		if item == nil {
			continue
		}
		info, err := item.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
