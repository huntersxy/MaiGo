package weixin_clawbot

import (
	"context"
	"fmt"
	"strings"
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
		slot, _ := asInt(args["slot"])
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
		slot, _ := asInt(args["slot"])
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
		slot, _ := asInt(args["slot"])
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
	for slot, conn := range o.slots {
		if conn == nil || !conn.Store.HasIdentity() {
			continue
		}
		state := conn.Store.State()
		remark := state.Remark
		if remark == "" {
			remark = fmt.Sprintf("微信账号 %d", slot+1)
		}
		accounts = append(accounts, map[string]any{
			"slot":       slot,
			"remark":     remark,
			"account_id": state.AccountID,
			"status":     conn.Status(),
			"ready":      conn.Ready(),
			"qr":         conn.QR(),
			"busy":       false,
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
	defer o.mu.Unlock()
	for slot := 0; slot < MaxAccounts; slot++ {
		conn := o.slots[slot]
		if conn == nil {
			continue
		}
		if !conn.Store.HasIdentity() {
			conn.Store.SetRemark(remark)
			_ = conn.Store.Save()
			conn.Start(true)
			return map[string]any{"slot": slot}, nil
		}
	}
	return nil, fmt.Errorf("最多同时绑定 %d 个账号", MaxAccounts)
}

func (o *Orchestrator) handleRenameAccount(slot int, remark string) (any, error) {
	remark, err := validateRemark(remark)
	if err != nil {
		return nil, err
	}
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	conn.Store.SetRemark(remark)
	if err := conn.Store.Save(); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (o *Orchestrator) handleLoginAccount(slot int) (any, error) {
	if o == nil || !o.settings.Plugin.Enabled {
		return nil, fmt.Errorf("请先启用微信适配器")
	}
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	conn.Start(true)
	return map[string]any{"success": true}, nil
}

func (o *Orchestrator) handleRemoveAccount(slot int) (any, error) {
	conn, _, ok := o.Slot(slot)
	if !ok || conn == nil {
		return nil, fmt.Errorf("账号不存在，请刷新页面")
	}
	_ = conn.Stop(context.Background())
	_ = conn.Store.Clear()
	return map[string]any{"success": true}, nil
}
