package mcp

import (
	"fmt"
	"strings"
)

const toolNamePrefix = "mcp-"

// ExposedToolName 生成旧版 Agent/LLM 侧工具名 mcp-{server}-{tool}
//
// MaiBot 默认使用裸 tool.name，仅重名时加前缀；该函数保留给旧前缀模式与别名解析
func ExposedToolName(serverName, originalToolName string) string {
	return prefixedToolName(serverName, originalToolName)
}

// IsMCPToolName 判断是否为旧版 mcp- 前缀暴露名
func IsMCPToolName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), toolNamePrefix)
}

// MaiBotCollisionName 复现 MaiBot ToolRegistry 在重名时的候选前缀规则（不含摘要后缀）
func MaiBotCollisionName(providerType, serverName, toolName string) string {
	source := strings.TrimSpace(serverName)
	if source == "" {
		source = strings.TrimSpace(providerType)
	}
	prefix := source
	if source != providerType {
		prefix = fmt.Sprintf("%s_%s", providerType, source)
	}
	return sanitizeMaiBotCandidate(prefix + "_" + strings.TrimSpace(toolName))
}

// mcpToolName 旧版 mcp-服务名-工具名 生成，保留给兼容代码
func mcpToolName(server string, toolName string) string {
	return prefixedToolName(server, toolName)
}

func sanitizeMCPName(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	trimmed := strings.Trim(b.String(), "-")
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}
