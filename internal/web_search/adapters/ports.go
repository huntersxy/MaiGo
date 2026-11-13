package adapters

// 本文件定义 adapter 子包对外部世界的端口，用于让各 adapter 自足。
//
// Python 侧 adapters/*.py 可以直接 `from ..config import WebServiceConfig`，
// 因为 Python 没有包级循环限制；Go 里 adapters 若反向 import 父包 websearch
// 就会构成循环引用。因此这里只声明 adapter 真正需要的最小接口与配置视图，
// 由父包在构造时转换后传入。

import (
	"context"
	"time"
)

// DefaultWebTimeout 默认联网超时，对齐父包 websearch.DefaultWebTimeout
const DefaultWebTimeout = 60 * time.Second

// FetchProvider 抓取网页正文的 provider
type FetchProvider interface {
	Name() string
	Fetch(ctx context.Context, pageURL string) (*PageResult, error)
}

// MCPToolInvoker 进程内 MCP 工具调用方
type MCPToolInvoker interface {
	CallTool(ctx context.Context, serverName, originalToolName string, arguments map[string]any) (map[string]any, error)
}

// StepSearchConfig StepSearch 适配器所需的配置视图
//
// 只保留该 adapter 关心的字段，父包 RuntimeConfig 在构造时投影到这个结构，
// 避免整包依赖。
type StepSearchConfig struct {
	MCPServer  string
	SearchTool string
	FetchTool  string
	Category   string
}

// NewStepSearchProviderForConfig 按配置视图构造 StepSearch 搜索 provider
func NewStepSearchProviderForConfig(cfg StepSearchConfig, invoker MCPToolInvoker) (*StepSearchProvider, error) {
	return newStepSearchProvider(cfg, invoker)
}

// NewStepSearchFetchProviderForConfig 按配置视图构造 StepSearch 抓取 provider
func NewStepSearchFetchProviderForConfig(cfg StepSearchConfig, invoker MCPToolInvoker) (FetchProvider, error) {
	return newStepSearchFetchProvider(cfg, invoker)
}
