package websearch

import (
	"context"
	"sync"
)

// MCPToolInvoker 通过已加载的 MCP 管理器调用指定服务器上的工具
type MCPToolInvoker interface {
	CallTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (map[string]any, error)
}

var (
	mcpInvokerMu sync.RWMutex
	mcpInvoker   MCPToolInvoker
)

// SetMCPToolInvoker 注入进程内 MCP 调用方；generation 重建或关闭时应传入 nil
func SetMCPToolInvoker(invoker MCPToolInvoker) {
	mcpInvokerMu.Lock()
	mcpInvoker = invoker
	mcpInvokerMu.Unlock()
}

func currentMCPToolInvoker() MCPToolInvoker {
	mcpInvokerMu.RLock()
	defer mcpInvokerMu.RUnlock()
	return mcpInvoker
}
