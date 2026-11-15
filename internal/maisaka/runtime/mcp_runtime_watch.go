package runtime

import (
	"context"
	"sync"
	"time"

	"MaiGo/internal/mcp_module"
)

const (
	// mcpRuntimeWatchInterval 主动重读 MCP 配置的周期，覆盖 WebUI 之外的 bot_config 热重载
	mcpRuntimeWatchInterval = 10 * time.Second
	// mcpConnectionProbeInterval 主动探活周期，覆盖 stdio/streamable_http 无断连回调的传输
	mcpConnectionProbeInterval = 30 * time.Second
)

// mcpRuntimeWatched 保证每个 Agent 只启动一组后台 watcher；Agent.Stop 时随 ctx 退出
var mcpRuntimeWatched sync.Map

// startMCPRuntimeWatcher 启动 MCP 配置热重载与连接主动探测
func (a *Agent) startMCPRuntimeWatcher() {
	if a == nil || a.ctx == nil {
		return
	}
	if _, loaded := mcpRuntimeWatched.LoadOrStore(a, struct{}{}); loaded {
		return
	}
	service := mcp.DefaultService()
	service.WatchConfig(a.ctx, mcpRuntimeWatchInterval, a.loadMCPRuntimeConfig)
	service.WatchConnections(a.ctx, mcpConnectionProbeInterval)
	go func() {
		<-a.ctx.Done()
		mcpRuntimeWatched.Delete(a)
	}()
}

// loadMCPRuntimeConfig 读取当前有效 MCP 配置与宿主回调，供 Service 热重载
func (a *Agent) loadMCPRuntimeConfig(ctx context.Context) (mcp.RuntimeConfig, error) {
	servers, clientCfg, err := a.mcpRuntimeConfig(ctx)
	if err != nil {
		return mcp.RuntimeConfig{}, err
	}
	host := mcp.HostCallbacks{}
	if generation := a.currentGeneration(); generation != nil {
		host = a.mcpHostCallbacks(
			generation.model, generation.modelName,
			generation.plannerModel, generation.plannerModelName,
			generation.utilsModel, generation.utilsModelName,
		)
	}
	return mcp.RuntimeConfig{Servers: servers, Client: clientCfg, Host: host}, nil
}
