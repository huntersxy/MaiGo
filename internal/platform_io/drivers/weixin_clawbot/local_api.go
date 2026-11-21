package weixin_clawbot

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// RegisterLocalAPIs 注册账号管理 LocalAPI（P0 可读列表；写操作预留）
func RegisterLocalAPIs(binder LocalAPIBinder, orch *Orchestrator) {
	if binder == nil {
		return
	}
	binder.SetAPILocalHandler("accounts", PluginID, map[string]any{
		"description": "读取微信账号列表与扫码状态",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = ctx
		_ = pluginID
		_ = componentName
		_ = args
		return orch.handleAccounts()
	})
	binder.SetAPILocalHandler("add_account", PluginID, map[string]any{
		"description": "添加微信账号并开始扫码",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = ctx
		_ = pluginID
		_ = componentName
		return orch.handleAddAccount(asString(args["remark"]))
	})
	binder.SetAPILocalHandler("rename_account", PluginID, map[string]any{
		"description": "设置微信账号备注名",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = ctx
		_ = pluginID
		_ = componentName
		slot, ok := asInt(args["slot"])
		if !ok || slot < 0 || slot >= MaxAccounts {
			return nil, fmt.Errorf("微信账号槽位无效")
		}
		return orch.handleRenameAccount(slot, asString(args["remark"]))
	})
	binder.SetAPILocalHandler("login_account", PluginID, map[string]any{
		"description": "重新扫码绑定原微信账号",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = ctx
		_ = pluginID
		_ = componentName
		slot, ok := asInt(args["slot"])
		if !ok || slot < 0 || slot >= MaxAccounts {
			return nil, fmt.Errorf("微信账号槽位无效")
		}
		return orch.handleLoginAccount(slot)
	})
	binder.SetAPILocalHandler("remove_account", PluginID, map[string]any{
		"description": "移除单个微信账号的本地登录态",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = ctx
		_ = pluginID
		_ = componentName
		slot, ok := asInt(args["slot"])
		if !ok || slot < 0 || slot >= MaxAccounts {
			return nil, fmt.Errorf("微信账号槽位无效")
		}
		return orch.handleRemoveAccount(slot)
	})
}

func (o *Orchestrator) handleAccounts() (any, error) {
	if o == nil {
		return map[string]any{"enabled": false, "limit": MaxAccounts, "accounts": []any{}}, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	accounts := make([]map[string]any, 0)
	for slot := 0; slot < MaxAccounts; slot++ {
		conn := o.slots[slot]
		if conn == nil || !conn.Store.HasIdentity() {
			continue
		}
		state := conn.Store.State()
		remark := state.Remark
		if remark == "" {
			remark = fmt.Sprintf("微信账号 %d", slot+1)
		}
		conn.mu.Lock()
		var qr any
		if conn.qr != "" {
			qr = conn.qr
		}
		status, ready, busy := conn.status, conn.ready, conn.busy
		conn.mu.Unlock()
		accounts = append(accounts, map[string]any{
			"slot":       slot,
			"remark":     remark,
			"account_id": state.AccountID,
			"status":     status,
			"ready":      ready,
			"qr":         qr,
			"busy":       busy,
		})
	}
	return map[string]any{
		"enabled":  o.settings.Plugin.Enabled,
		"limit":    MaxAccounts,
		"accounts": accounts,
	}, nil
}

func validateRemark(remark string) (string, error) {
	remark = strings.TrimSpace(remark)
	if remark == "" || len([]rune(remark)) > 80 {
		return "", fmt.Errorf("备注名不能为空，且最多 80 个字符")
	}
	return remark, nil
}

func (o *Orchestrator) handleAddAccount(remark string) (any, error) {
	remark, err := validateRemark(remark)
	if err != nil {
		return nil, err
	}
	if o == nil || !o.settings.Plugin.Enabled {
		return nil, fmt.Errorf("请先启用微信适配器")
	}
	o.mu.Lock()
	for slot := 0; slot < MaxAccounts; slot++ {
		conn := o.slots[slot]
		if conn == nil || conn.Store.HasIdentity() {
			continue
		}
		err := conn.Store.updateAndSave(func(state *AccountState) { state.Remark = remark })
		o.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err := o.scheduleAccountAction(conn, false); err != nil {
			return nil, err
		}
		return map[string]any{"slot": slot}, nil
	}
	o.mu.Unlock()
	return nil, fmt.Errorf("最多同时绑定 %d 个账号", MaxAccounts)
}

func (o *Orchestrator) handleRenameAccount(slot int, remark string) (any, error) {
	remark, err := validateRemark(remark)
	if err != nil {
		return nil, err
	}
	o.lifecycle.Lock()
	defer o.lifecycle.Unlock()
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil || !conn.Store.HasIdentity() {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	if err := conn.Store.updateAndSave(func(state *AccountState) { state.Remark = remark }); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (o *Orchestrator) handleLoginAccount(slot int) (any, error) {
	if o == nil || !o.settings.Plugin.Enabled {
		return nil, fmt.Errorf("请先启用微信适配器")
	}
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil || !conn.Store.HasIdentity() {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	if err := o.scheduleAccountAction(conn, false); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (o *Orchestrator) handleRemoveAccount(slot int) (any, error) {
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil || !conn.Store.HasIdentity() {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	if err := o.scheduleAccountAction(conn, true); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (o *Orchestrator) scheduleAccountAction(conn *AccountConnection, remove bool) error {
	conn.mu.Lock()
	if conn.busy {
		conn.mu.Unlock()
		return fmt.Errorf("该账号操作仍在进行，请稍后再试")
	}
	conn.busy = true
	conn.status = "正在获取二维码"
	if remove {
		conn.status = "正在移除账号"
	}
	conn.mu.Unlock()
	go func() {
		o.lifecycle.Lock()
		defer o.lifecycle.Unlock()
		defer func() {
			conn.mu.Lock()
			conn.busy = false
			conn.mu.Unlock()
		}()
		o.mu.Lock()
		active := o.started && o.slots[conn.slot] == conn
		o.mu.Unlock()
		if !active {
			return
		}
		err := conn.Stop(context.Background())
		if err == nil {
			if remove {
				err = conn.Store.Clear()
			} else {
				conn.Start(true)
			}
		}
		if err != nil {
			conn.setStatus("账号操作失败，请查看日志后重试")
			zap.L().Error("微信账号操作失败", zap.Int("slot", conn.slot), zap.Error(err))
		}
	}()
	return nil
}
