package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ToolExposureMode 工具暴露命名策略
type ToolExposureMode string

const (
	// ToolExposureBare MaiBot 默认：优先裸 tool.name，跨服务器重名时才加来源前缀
	ToolExposureBare ToolExposureMode = "bare"
	// ToolExposurePrefixed 固定前缀命名：统一 mcp-{server}-{tool}
	ToolExposurePrefixed ToolExposureMode = "prefixed"
)

const (
	maiBotProviderName     = "mcp"
	maiBotToolNameMaxBytes = 64
	maiBotDigestLength     = 12
	maiBotDigestPrefixLen  = 51
)

// maiBotBuiltinToolNames 对齐 MaiBot tooling.BUILTIN_TOOL_NAMES，避免 MCP 裸名抢占内置工具
var maiBotBuiltinToolNames = []string{
	"reply",
	"no_action",
	"stop",
	"create_table",
	"list_tables",
	"view_table",
}

// ToolRoute 暴露名到服务器原始工具名的路由
type ToolRoute struct {
	Exposed     string `json:"exposed"`
	Original    string `json:"original"`
	ServerName  string `json:"server_name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// buildBareToolRoutes 复现 MaiBot ToolRegistry._list_routes 的名称分配规则
//
// 首个同名工具保留裸名；后续重名工具改写为 {provider_type}_{server}_{name}，
// 非法字符替换为 _，超过 64 字节或与保留名冲突时追加 sha256 摘要
func buildBareToolRoutes(entries []ToolRoute, reserved map[string]struct{}) ([]ToolRoute, error) {
	taken := make(map[string]struct{}, len(entries)+len(reserved))
	for name := range reserved {
		taken[name] = struct{}{}
	}
	routes := make([]ToolRoute, 0, len(entries))
	for _, entry := range entries {
		original := strings.TrimSpace(entry.Original)
		if original == "" {
			return nil, fmt.Errorf("MCP 服务器 %q 返回了空工具名", entry.ServerName)
		}
		entry.Original = original
		exposed := original
		if _, exists := taken[exposed]; exists {
			source := strings.TrimSpace(entry.ServerName)
			if source == "" {
				source = maiBotProviderName
			}
			prefix := source
			if source != maiBotProviderName {
				prefix = maiBotProviderName + "_" + source
			}
			candidate := sanitizeMaiBotCandidate(prefix + "_" + original)
			if len(candidate) > maiBotToolNameMaxBytes || existsInSet(taken, candidate) {
				digest := maiBotRouteDigest(source, original)
				candidate = candidate[:maiBotDigestPrefixLen] + "_" + digest
				if existsInSet(taken, candidate) {
					return nil, fmt.Errorf("MCP 工具来源重复，无法分配唯一名称：%s/%s",
						entry.ServerName, original)
				}
			}
			exposed = candidate
		}
		taken[exposed] = struct{}{}
		entry.Exposed = exposed
		routes = append(routes, entry)
	}
	return routes, nil
}

func existsInSet(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func sanitizeMaiBotCandidate(raw string) string {
	var builder strings.Builder
	for _, char := range raw {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9', char == '_', char == '-':
			builder.WriteRune(char)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func maiBotRouteDigest(source, toolName string) string {
	sum := sha256.Sum256([]byte(maiBotProviderName + ":" + source + ":" + toolName))
	return hex.EncodeToString(sum[:])[:maiBotDigestLength]
}

// prefixedToolName 固定前缀暴露名：mcp-{server}-{tool}
func prefixedToolName(serverName, originalToolName string) string {
	return "mcp-" + sanitizeMCPName(serverName) + "-" + sanitizeMCPName(originalToolName)
}

// resolveRoute 按暴露名查找路由，兼容裸名、MaiBot 冲突前缀名与 mcp- 前缀名
func resolveRoute(routes []ToolRoute, name string) (ToolRoute, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ToolRoute{}, false
	}
	for _, route := range routes {
		if route.Exposed == trimmed {
			return route, true
		}
		if prefixedToolName(route.ServerName, route.Original) == trimmed {
			return route, true
		}
	}
	return ToolRoute{}, false
}

// routeForServerTool 查找指定服务器与原始工具名的路由
func routeForServerTool(routes []ToolRoute, serverName, originalToolName string) (ToolRoute, bool) {
	server := strings.TrimSpace(serverName)
	toolName := strings.TrimSpace(originalToolName)
	for _, route := range routes {
		if route.ServerName != server {
			continue
		}
		if route.Original == toolName || route.Exposed == toolName {
			return route, true
		}
	}
	return ToolRoute{}, false
}
