package chatwiring

import (
	"context"
	"strings"

	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/core/localoperator"
)

// CommandPolicyPort 把命令权限判定接到 localoperator
// 对齐 Python bot.py:305/448 的 has_command_permission 调用
type CommandPolicyPort struct {
	// Settings 返回当前 operator 权限列表与命令权限规则
	Settings func() (operatorPermissions []string, commandPermissions map[string]localoperator.CommandPermissionRule)
	// LocalOperator 当前用户是否为本地操作员
	LocalOperator func(platform, userID string) bool
}

// HasCommandPermission 判断用户是否有权执行受保护命令
func (p *CommandPolicyPort) HasCommandPermission(_ context.Context, commandID string, message *messagereceive.SessionMessage) bool {
	if p == nil || p.Settings == nil || message == nil {
		return false
	}
	operatorPermissions, commandPermissions := p.Settings()
	platform := strings.TrimSpace(message.Platform)
	userID := message.UserID()
	sessionID := strings.TrimSpace(message.SessionID)
	localOperator := false
	if p.LocalOperator != nil {
		localOperator = p.LocalOperator(platform, userID)
	}
	return localoperator.HasCommandPermission(commandID, platform, userID, sessionID,
		operatorPermissions, commandPermissions, localOperator, false)
}

// HasPluginManagementPermission 判断用户是否有插件管理权限
func (p *CommandPolicyPort) HasPluginManagementPermission(_ context.Context, message *messagereceive.SessionMessage) bool {
	if p == nil || p.Settings == nil || message == nil {
		return false
	}
	operatorPermissions, _ := p.Settings()
	platform := strings.TrimSpace(message.Platform)
	userID := message.UserID()
	localOperator := false
	if p.LocalOperator != nil {
		localOperator = p.LocalOperator(platform, userID)
	}
	return localoperator.HasPluginManagementPermission(platform, userID, operatorPermissions, localOperator, false)
}

// AdapterLifecyclePort 把适配器上下线接到 Platform IO 管理器
// 对齐 Python integration.py:1319-1400 的 take_adapters_offline/bring_adapters_online
type AdapterLifecyclePort struct {
	// TakeOffline 让所有适配器下线
	TakeOffline func(ctx context.Context) (messagereceive.AdapterLifecycleResult, error)
	// BringOnline 让所有适配器上线
	BringOnline func(ctx context.Context) (messagereceive.AdapterLifecycleResult, error)
}

// Remote 表示适配器是否运行在远端；Go 侧适配器均为进程内，恒为 false
func (p *AdapterLifecyclePort) Remote() bool {
	return false
}

// TakeAdaptersOffline 让所有适配器下线
func (p *AdapterLifecyclePort) TakeAdaptersOffline(ctx context.Context) (messagereceive.AdapterLifecycleResult, error) {
	if p == nil || p.TakeOffline == nil {
		return messagereceive.AdapterLifecycleResult{}, nil
	}
	return p.TakeOffline(ctx)
}

// BringAdaptersOnline 让所有适配器上线
func (p *AdapterLifecyclePort) BringAdaptersOnline(ctx context.Context) (messagereceive.AdapterLifecycleResult, error) {
	if p == nil || p.BringOnline == nil {
		return messagereceive.AdapterLifecycleResult{}, nil
	}
	return p.BringOnline(ctx)
}
