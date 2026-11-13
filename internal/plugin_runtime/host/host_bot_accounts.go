package host

import (
	"context"
	"strings"
)

const (
	// BotAccountSourceReady 消息网关就绪时上报账号，对齐 Python BOT_ACCOUNT_SOURCE_READY
	BotAccountSourceReady = "ready"
	// BotAccountSourceInbound 消息网关收到入站消息时上报账号
	BotAccountSourceInbound = "inbound"
)

// PluginBotAccountIdentity 插件网关上报道 Bot 账号时的来源快照
//
// 字段对齐 Python AdapterAccountIdentity；AdapterID 为 gateway:<plugin_id>:<gateway_name>
type PluginBotAccountIdentity struct {
	Platform    string
	AccountID   string
	AdapterID   string
	PluginID    string
	GatewayName string
}

// PluginBotAccountPort 由主程序的 Bot 账号服务实现
//
// 端口只承担身份记录，不负责禁用状态与配置兜底，这些语义保持在 services.BotAccountService
type PluginBotAccountPort interface {
	RecordAdapterAccount(ctx context.Context, identity PluginBotAccountIdentity, source string) error
}

// SetBotAccountPort 注入 Bot 账号上报端口
func (h *Host) SetBotAccountPort(port PluginBotAccountPort) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.botAccounts = port
	h.mu.Unlock()
}

// BotAccountPort 返回已注入的 Bot 账号上报端口
func (h *Host) BotAccountPort() PluginBotAccountPort {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.botAccounts
}

// recordGatewayBotAccount 上报消息网关当前使用的 Bot 账号
//
// 无账号或端口未接线时跳过，避免写入猜测身份；端口返回错误时原样上抛，由调用方决定是否阻断
func (h *Host) recordGatewayBotAccount(ctx context.Context, pluginID string, entry *ComponentEntry, state MessageGatewayRuntimeState, source string) error {
	if h == nil || entry == nil {
		return nil
	}
	platform := strings.TrimSpace(state.Platform)
	accountID := strings.TrimSpace(state.AccountID)
	if platform == "" || accountID == "" {
		return nil
	}
	port := h.BotAccountPort()
	if port == nil {
		return nil
	}
	return port.RecordAdapterAccount(ctx, PluginBotAccountIdentity{
		Platform:    platform,
		AccountID:   accountID,
		AdapterID:   pluginGatewayAdapterID(pluginID, entry.Name),
		PluginID:    strings.TrimSpace(pluginID),
		GatewayName: entry.Name,
	}, source)
}

// pluginGatewayAdapterID 构造插件消息网关驱动 ID，与 platformio.BuildPluginGatewayDriverID 保持一致
func pluginGatewayAdapterID(pluginID, gatewayName string) string {
	return "gateway:" + strings.TrimSpace(pluginID) + ":" + strings.TrimSpace(gatewayName)
}
