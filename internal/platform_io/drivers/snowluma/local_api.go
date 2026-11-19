package snowluma

import (
	"context"
	"fmt"
)

// LocalAPIBinder Host API 本地短路注册面
type LocalAPIBinder interface {
	SetAPILocalHandler(name, pluginID string, metadata map[string]any, handler func(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error)) bool
	RemoveAPIsByPlugin(pluginID string) int
}

// RegisterLocalAPIs 注册 identity_observations / identity_only_lease
// handler 始终通过 ActiveAdapter 解析，停用时返回空结果而不是打到 Python Runner
func RegisterLocalAPIs(binder LocalAPIBinder, adapter *Adapter) {
	if binder == nil {
		return
	}
	if adapter != nil {
		setActiveAdapter(adapter)
	}
	binder.SetAPILocalHandler("identity_observations", PluginID, map[string]any{
		"description": "查询指定账号和群的短期消息指纹及用户名称",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, handleIdentityObservationsAPI)
	binder.SetAPILocalHandler("identity_only_lease", PluginID, map[string]any{
		"description": "续期或释放短时身份查询模式，超时恢复普通收发路由",
		"version":     "1",
		"public":      true,
		"enabled":     true,
	}, handleIdentityOnlyLeaseAPI)
}

func handleIdentityObservationsAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error) {
	return ActiveAdapter().handleIdentityObservations(ctx, pluginID, componentName, args)
}

func handleIdentityOnlyLeaseAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error) {
	return ActiveAdapter().handleIdentityOnlyLease(ctx, pluginID, componentName, args)
}

func (a *Adapter) handleIdentityObservations(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error) {
	_ = ctx
	_ = pluginID
	_ = componentName
	accountID := asString(args["account_id"])
	groupIDs := asStringSlice(args["group_ids"])
	if a == nil || a.router == nil || a.runtime == nil || a.runtime.AccountID() != accountID {
		return map[string]any{"protocol": identityFingerprintV1, "observations": []any{}}, nil
	}
	return a.router.IdentityObservations().Snapshot(accountID, groupIDs)
}

func (a *Adapter) handleIdentityOnlyLease(ctx context.Context, pluginID, componentName string, args map[string]any) (map[string]any, error) {
	_ = pluginID
	_ = componentName
	accountID := asString(args["account_id"])
	owner := asString(args["owner"])
	enabled := asBool(args["enabled"], true)
	if a == nil || a.router == nil || a.runtime == nil || a.runtime.AccountID() != accountID {
		return map[string]any{"active": false, "ttl": 0}, nil
	}
	result, err := a.router.IdentityOnlyLease().Update(accountID, owner, enabled)
	if err != nil {
		return nil, err
	}
	if err := a.syncIdentityMode(ctx); err != nil {
		return nil, err
	}
	result["active"] = a.router.IdentityOnlyLease().Active(accountID)
	return result, nil
}

func (a *Adapter) syncIdentityMode(ctx context.Context) error {
	if a == nil || a.runtime == nil || a.router == nil {
		return nil
	}
	accountID := a.runtime.AccountID()
	if accountID == "" {
		return nil
	}
	active := a.router.IdentityOnlyLease().Active(accountID)
	a.runtime.SetRoutingEnabled(!active)
	if a.hostHooks == nil {
		return nil
	}
	if err := a.hostHooks.ReportGatewayState(ctx, a.runtime); err != nil {
		return fmt.Errorf("Host 未接受 QQ 连接器收发路由切换: %w", err)
	}
	return nil
}
