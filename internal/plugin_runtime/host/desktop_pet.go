package host

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"maibot-go/internal/plugin_runtime/protocol"
)

// 桌宠按钮超时与上限，对齐 supervisor.desktop_pet_buttons / desktop_pet_buttons.py
const (
	defaultDesktopPetButtonStatesTimeoutMS = 4000
	defaultDesktopPetButtonClickTimeoutMS  = 10000
	maxDesktopPetButtons                   = 10
)

var desktopPetButtonIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

var desktopPetButtonIcons = map[string]struct{}{
	"puzzle":   {},
	"volume":   {},
	"settings": {},
	"refresh":  {},
	"star":     {},
	"heart":    {},
	"book":     {},
	"bell":     {},
}

// NormalizeDesktopPetButtons 规范化注册载荷中的桌宠按钮声明。
// 对齐 desktop_pet_buttons.DesktopPetButton（str_strip_whitespace + 字段约束）与
// RegisterPluginPayload.validate_desktop_pet_buttons 的重复 ID / 配置错误守卫。
func NormalizeDesktopPetButtons(buttons []protocol.DesktopPetButton, configError string) ([]protocol.DesktopPetButton, error) {
	if len(buttons) > maxDesktopPetButtons {
		return nil, fmt.Errorf("每个插件最多声明 %d 个桌宠按钮，且必须使用列表", maxDesktopPetButtons)
	}
	out := make([]protocol.DesktopPetButton, 0, len(buttons))
	seen := make(map[string]struct{}, len(buttons))
	for _, button := range buttons {
		button.ID = strings.TrimSpace(button.ID)
		button.Label = strings.TrimSpace(button.Label)
		button.Icon = strings.TrimSpace(button.Icon)
		if button.Icon == "" {
			button.Icon = "puzzle"
		}
		if button.ID == "" || len(button.ID) > 64 || !desktopPetButtonIDPattern.MatchString(button.ID) {
			return nil, fmt.Errorf("桌宠按钮 ID 不合法: %q", button.ID)
		}
		if button.Label == "" || len([]rune(button.Label)) > 32 {
			return nil, fmt.Errorf("桌宠按钮标签长度必须在 1-32 之间: %q", button.Label)
		}
		if _, ok := desktopPetButtonIcons[button.Icon]; !ok {
			return nil, fmt.Errorf("桌宠按钮图标不合法: %s", button.Icon)
		}
		if button.Order < -10000 || button.Order > 10000 {
			return nil, fmt.Errorf("桌宠按钮排序必须在 -10000 到 10000 之间: %d", button.Order)
		}
		if _, dup := seen[button.ID]; dup {
			return nil, fmt.Errorf("桌宠按钮 ID 重复")
		}
		seen[button.ID] = struct{}{}
		out = append(out, button)
	}
	if strings.TrimSpace(configError) != "" && len(out) > 0 {
		return nil, fmt.Errorf("配置错误的插件不能注册桌宠按钮")
	}
	return out, nil
}

// DesktopPetButtonContribution 声明了桌宠按钮的插件贡献
type DesktopPetButtonContribution struct {
	Name    string
	Buttons []protocol.DesktopPetButton
}

// DesktopPetButtonContributions 返回按钮贡献的类型化快照，对齐 get_desktop_pet_button_plugins
func (h *Host) DesktopPetButtonContributions() map[string]DesktopPetButtonContribution {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]DesktopPetButtonContribution)
	for pluginID, rec := range h.plugins {
		if len(rec.DesktopPetButtons) == 0 || strings.TrimSpace(rec.ConfigError) != "" {
			continue
		}
		out[pluginID] = DesktopPetButtonContribution{
			Name:    rec.DisplayName,
			Buttons: append([]protocol.DesktopPetButton(nil), rec.DesktopPetButtons...),
		}
	}
	return out
}

// DesktopPetButtonPlugins 返回声明了桌宠按钮的插件贡献，对齐 get_desktop_pet_button_plugins
func (h *Host) DesktopPetButtonPlugins() map[string]any {
	contributions := h.DesktopPetButtonContributions()
	out := make(map[string]any, len(contributions))
	for pluginID, contribution := range contributions {
		buttons := make([]any, 0, len(contribution.Buttons))
		for _, button := range contribution.Buttons {
			buttons = append(buttons, button)
		}
		out[pluginID] = map[string]any{
			"name":    contribution.Name,
			"buttons": buttons,
		}
	}
	return out
}

// DesktopPetButtons 请求 Runner 读取桌宠按钮快照（buttonID 非空时先触发点击再读回），
// 对齐 supervisor.desktop_pet_buttons 的全部守卫。
func (h *Host) DesktopPetButtons(ctx context.Context, pluginID, buttonID string) (protocol.DesktopPetButtonSnapshot, error) {
	pluginID = strings.TrimSpace(pluginID)
	buttonID = strings.TrimSpace(buttonID)
	if pluginID == "" {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("plugin_id 不能为空")
	}

	h.mu.RLock()
	rec, ok := h.plugins[pluginID]
	h.mu.RUnlock()
	if !ok || len(rec.DesktopPetButtons) == 0 {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("插件未注册桌宠按钮")
	}
	if buttonID != "" && !desktopPetButtonDeclared(rec.DesktopPetButtons, buttonID) {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("未声明的桌宠按钮")
	}

	server := h.rpcServerForGroup(rec.RunnerGroup)
	if server == nil {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("Runner 未连接")
	}
	timeoutMS := int64(defaultDesktopPetButtonStatesTimeoutMS)
	payload := map[string]any{"button_id": nil}
	if buttonID != "" {
		timeoutMS = int64(defaultDesktopPetButtonClickTimeoutMS)
		payload["button_id"] = buttonID
	}

	resp, err := server.SendRequest(ctx, protocol.MethodPluginDesktopPetButtons, pluginID, payload, timeoutMS)
	if err != nil {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("桌宠按钮请求失败: %w", err)
	}
	if message := envelopeErrorMessage(resp.Error, ""); message != "" {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("%s", message)
	}

	h.mu.RLock()
	current, stillRegistered := h.plugins[pluginID]
	h.mu.RUnlock()
	if !stillRegistered || current.Revision != rec.Revision {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("插件已发生卸载或重载，请重试")
	}

	snapshot, err := decodeDesktopPetButtonSnapshot(resp.Payload)
	if err != nil {
		return protocol.DesktopPetButtonSnapshot{}, err
	}
	if !equalDesktopPetButtons(snapshot.Buttons, rec.DesktopPetButtons) {
		return protocol.DesktopPetButtonSnapshot{}, fmt.Errorf("桌宠按钮声明发生变化，请重载插件")
	}
	return snapshot, nil
}

func decodeDesktopPetButtonSnapshot(payload map[string]any) (protocol.DesktopPetButtonSnapshot, error) {
	snapshot := protocol.DesktopPetButtonSnapshot{States: map[string]protocol.DesktopPetButtonState{}}
	buttons := payloadSliceValue(payload["buttons"])
	if len(buttons) > maxDesktopPetButtons {
		return snapshot, fmt.Errorf("每个插件最多声明 %d 个桌宠按钮，且必须使用列表", maxDesktopPetButtons)
	}
	ids := make(map[string]struct{}, len(buttons))
	for _, raw := range buttons {
		entry := anyMap(raw)
		button := protocol.DesktopPetButton{
			ID:    strings.TrimSpace(payloadText(entry, "id")),
			Label: strings.TrimSpace(payloadText(entry, "label")),
			Icon:  strings.TrimSpace(payloadText(entry, "icon")),
			Order: int(payloadInt64(entry, "order")),
		}
		if button.Icon == "" {
			button.Icon = "puzzle"
		}
		if _, dup := ids[button.ID]; dup {
			return snapshot, fmt.Errorf("桌宠按钮 ID 重复")
		}
		ids[button.ID] = struct{}{}
		snapshot.Buttons = append(snapshot.Buttons, button)
	}
	states := payloadMap(payload, "states")
	if len(states) != len(ids) {
		return snapshot, fmt.Errorf("必须为每个桌宠按钮提供状态，且不能包含未声明的按钮")
	}
	for id, raw := range states {
		if _, ok := ids[id]; !ok {
			return snapshot, fmt.Errorf("必须为每个桌宠按钮提供状态，且不能包含未声明的按钮")
		}
		entry := anyMap(raw)
		snapshot.States[id] = protocol.DesktopPetButtonState{
			Visible:  payloadBool(entry, "visible", true),
			Disabled: payloadBool(entry, "disabled", false),
			Text:     payloadText(entry, "text"),
		}
	}
	return snapshot, nil
}

func anyMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok && typed != nil {
		return typed
	}
	return map[string]any{}
}

func payloadSliceValue(value any) []any {
	switch typed := value.(type) {
	case nil:
		return nil
	case []any:
		return typed
	case []protocol.DesktopPetButton:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

func desktopPetButtonDeclared(buttons []protocol.DesktopPetButton, buttonID string) bool {
	for _, button := range buttons {
		if button.ID == buttonID {
			return true
		}
	}
	return false
}

func equalDesktopPetButtons(left, right []protocol.DesktopPetButton) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].Label != right[i].Label || left[i].Icon != right[i].Icon || left[i].Order != right[i].Order {
			return false
		}
	}
	return true
}

// rpcServerForGroup 返回指定 Runner 分组的 RPC 服务器，未找到时回退到任一可用分组。
func (h *Host) rpcServerForGroup(group RunnerGroup) *RPCServer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if state, ok := h.runners[group]; ok && state != nil && state.rpc != nil {
		return state.rpc
	}
	for _, state := range h.runners {
		if state != nil && state.rpc != nil {
			return state.rpc
		}
	}
	return nil
}
