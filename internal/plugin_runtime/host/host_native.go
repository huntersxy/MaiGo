package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/qq_official"
	"MaiGo/internal/platform_io/drivers/telegram"
	"MaiGo/internal/platform_io/drivers/weixin_clawbot"
)

// KnownNativeAdapterIDs 进程内 Go 化适配器 ID 集合（脚手架期固定）
var KnownNativeAdapterIDs = map[string]struct{}{
	"maibot-team.snowluma-adapter":       {},
	"maibot-team.telegram-adapter":       {},
	"maibot-team.weixin-clawbot-adapter": {},
	"Mai-with-u.qq-adapter":              {},
}

// IsNativeAdapter 判断插件是否由进程内 NativeRuntime 承接（已 Register 或已知 ID）
func IsNativeAdapter(pluginID string) bool {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return false
	}
	if platformio.DefaultNativeRuntime().IsRegistered(pluginID) {
		return true
	}
	_, known := KnownNativeAdapterIDs[pluginID]
	return known
}

func platformioNativeGet(pluginID string) (platformio.NativeRegistration, bool) {
	return platformio.DefaultNativeRuntime().Get(pluginID)
}

// NativeTeardown 完整卸载原生适配器：驱动 + API + 组件 + StopRuntime
func (h *Host) NativeTeardown(ctx context.Context, pluginID string) error {
	if h == nil {
		return nil
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return nil
	}
	var firstErr error
	if err := h.UnregisterPluginGatewayDrivers(ctx, pluginID); err != nil {
		firstErr = err
	}
	if h.apis != nil {
		h.apis.RemoveAPIsByPlugin(pluginID)
	}
	if h.components != nil {
		h.components.RemoveComponentsByPlugin(pluginID)
	}
	if h.hooks != nil {
		h.hooks.RemovePlugin(pluginID)
	}
	if pluginID == qq_official.PluginID {
		_ = h.ApplyWebUIExtension(pluginID, nil)
	}
	platformio.DefaultNativeRuntime().StopRuntime(pluginID)
	h.ClearNativeInProcessReason(pluginID)
	h.rememberNativeLoadStatus(pluginID, "inactive")
	return firstErr
}

// RegisterNativeGatewayDriver 注册原生 Adapter：storeMessageGatewayState + 端口 Bind 路由
func (h *Host) RegisterNativeGatewayDriver(ctx context.Context, spec NativeGatewayDriverSpec) error {
	if h == nil {
		return fmt.Errorf("Host 未初始化")
	}
	spec.PluginID = strings.TrimSpace(spec.PluginID)
	spec.GatewayName = strings.TrimSpace(spec.GatewayName)
	if spec.PluginID == "" || spec.GatewayName == "" {
		return fmt.Errorf("原生网关 plugin_id/gateway_name 不能为空")
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]any{}
	}
	spec.Metadata["plugin_type"] = "adapter"
	h.EnsureNativeMessageGateways()
	h.gatewayOpsMu.Lock()
	defer h.gatewayOpsMu.Unlock()
	driverPort, err := h.gatewayDriverPort()
	if err != nil {
		return err
	}
	if err := driverPort.RegisterNativeGatewayDriver(ctx, spec); err != nil {
		return err
	}
	state := MessageGatewayRuntimeState{
		Ready:     true,
		Platform:  strings.TrimSpace(spec.Platform),
		AccountID: strings.TrimSpace(spec.AccountID),
		Scope:     strings.TrimSpace(spec.Scope),
		Metadata:  cloneAnyMap(spec.Metadata),
	}
	h.storeMessageGatewayState(spec.PluginID, spec.GatewayName, state)
	return nil
}

// ResolveNativePluginConfigPath 导出原生配置路径解析
func (h *Host) ResolveNativePluginConfigPath(pluginID string) (string, error) {
	return h.resolveNativePluginConfigPath(pluginID)
}

// resolveNativePluginConfigPath 解析原生适配器 config.toml；无 Runner 时也能落盘
func (h *Host) resolveNativePluginConfigPath(pluginID string) (string, error) {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", fmt.Errorf("plugin_id 不能为空")
	}
	if reg, ok := platformio.DefaultNativeRuntime().Get(pluginID); ok {
		if root := strings.TrimSpace(reg.ConfigRoot); root != "" {
			// ConfigRoot 已是配置目录（含 config.toml 的父目录）时直接拼接
			if strings.HasSuffix(filepath.Clean(root), string(filepath.Separator)+"config.toml") {
				return root, nil
			}
			if info, err := os.Stat(filepath.Join(root, "config.toml")); err == nil && !info.IsDir() {
				return filepath.Join(root, "config.toml"), nil
			}
			// 实例 builtin_plugins/<dir> 或第三方插件目录
			if strings.Contains(filepath.ToSlash(root), "/config/builtin_plugins/") {
				return filepath.Join(root, "config.toml"), nil
			}
			return ResolvePluginConfigPath(root), nil
		}
		if dir := strings.TrimSpace(reg.DirectoryName); dir != "" {
			if instanceRoot := ResolveInstanceRoot(); instanceRoot != "" {
				return filepath.Join(instanceRoot, "config", "builtin_plugins", dir, "config.toml"), nil
			}
		}
	}
	if path, err := h.pluginConfigPath(pluginID); err == nil && strings.TrimSpace(path) != "" {
		return path, nil
	}
	switch pluginID {
	case "maibot-team.snowluma-adapter":
		third := DefaultThirdPartyPluginDir(ResolveInstanceRoot())
		if third == "" {
			third = filepath.Join(ResolveInstanceRoot(), "plugins")
		}
		return ResolvePluginConfigPath(filepath.Join(third, "maibot-team_snowluma-adapter")), nil
	case "maibot-team.telegram-adapter":
		return filepath.Join(ResolveInstanceRoot(), "config", "builtin_plugins", "telegram_adapter", "config.toml"), nil
	case "maibot-team.weixin-clawbot-adapter":
		return filepath.Join(ResolveInstanceRoot(), "config", "builtin_plugins", "weixin_clawbot_adapter", "config.toml"), nil
	case "Mai-with-u.qq-adapter":
		return filepath.Join(ResolveInstanceRoot(), "config", "builtin_plugins", "qq_official_adapter", "config.toml"), nil
	default:
		return "", fmt.Errorf("未找到原生插件配置路径: %s", pluginID)
	}
}

// readNativePluginEnabled 读取原生适配器启用门闩；缺文件/缺键=false
func readNativePluginEnabled(configPath string) (bool, error) {
	raw, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return readNativePluginEnabledValue(string(raw))
}

func readNativePluginEnabledValue(raw string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return false, nil
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(raw), &document); err != nil {
		return false, err
	}
	section, ok := document["plugin"].(map[string]any)
	if !ok {
		return false, nil
	}
	value, ok := section["enabled"]
	if !ok {
		return false, nil
	}
	enabled, err := platformio.ParseNativeConfigBool(value)
	if err != nil {
		return false, fmt.Errorf("plugin.enabled: %w", err)
	}
	return enabled, nil
}

// StartNativeRuntimeIfEnabled 按本地配置决定是否 StartRuntime
// enabled=false：不 Construct、不拨号；snowluma/telegram enabled=true：注入 HostHooks 后 Construct+Start
func (h *Host) StartNativeRuntimeIfEnabled(ctx context.Context, pluginID string) error {
	if h == nil {
		return nil
	}
	pluginID = strings.TrimSpace(pluginID)
	if !platformio.DefaultNativeRuntime().IsRegistered(pluginID) {
		return fmt.Errorf("未登记的原生适配器: %s", pluginID)
	}
	path, err := h.resolveNativePluginConfigPath(pluginID)
	if err != nil {
		return err
	}
	enabled, err := readNativePluginEnabled(path)
	if err != nil {
		return err
	}
	if !enabled {
		if pluginID == "maibot-team.snowluma-adapter" {
			h.EnsureSnowlumaLocalAPIs()
		}
		return nil
	}
	if platformio.DefaultNativeRuntime().IsStarted(pluginID) {
		return nil
	}
	snapshot, err := h.InspectPluginConfig(ctx, pluginID, nil, false)
	if err != nil {
		return err
	}
	switch pluginID {
	case "maibot-team.snowluma-adapter":
		return h.startSnowlumaIfEnabled(ctx, snapshot.NormalizedConfig)
	case telegram.PluginID:
		return h.startTelegramIfEnabled(ctx, snapshot.NormalizedConfig)
	case weixin_clawbot.PluginID:
		return h.startWeixinIfEnabled(ctx, snapshot.NormalizedConfig)
	case qq_official.PluginID:
		return h.startQQOfficialIfEnabled(ctx, snapshot.NormalizedConfig)
	}
	adapter, err := platformio.DefaultNativeRuntime().StartRuntime(ctx, pluginID, snapshot.NormalizedConfig)
	if err != nil {
		return err
	}
	if err := adapter.Start(ctx); err != nil {
		platformio.DefaultNativeRuntime().StopRuntime(pluginID)
		return err
	}
	zap.L().Info("原生适配器运行时已启动", zap.String("plugin_id", pluginID))
	return nil
}
