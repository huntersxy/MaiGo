package mcp

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// defaultConfigWatchInterval 主动热重载轮询周期，作为 Python config_manager 回调在 Go 侧的等价实现
const defaultConfigWatchInterval = 10 * time.Second

// RuntimeConfig 一次热重载所需的完整运行时配置
type RuntimeConfig struct {
	Servers []ServerConfig
	Client  ClientConfig
	Host    HostCallbacks
}

// RuntimeConfigLoader 从配置源读取当前 MCP 配置；返回错误时保留上一代连接
type RuntimeConfigLoader func(ctx context.Context) (RuntimeConfig, error)

// WatchConfig 按周期重新读取配置并热重载；签名未变化时 Reload 不会重建连接
func (s *Service) WatchConfig(ctx context.Context, interval time.Duration, loader RuntimeConfigLoader) {
	if s == nil || loader == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = defaultConfigWatchInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				next, err := loader(ctx)
				if err != nil {
					zap.L().Warn("MCP 配置主动热重载读取失败，保留旧连接", zap.Error(err))
					continue
				}
				s.SetClientConfig(next.Client)
				s.SetHostCallbacks(next.Host)
				if err := s.EnsureInitialized(ctx, next.Servers); err != nil {
					zap.L().Warn("MCP 配置主动热重载失败，保留旧连接", zap.Error(err))
				}
			}
		}
	}()
}
