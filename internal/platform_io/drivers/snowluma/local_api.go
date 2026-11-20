package snowluma

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// LocalAPIBinder Host API 本地短路注册面
type LocalAPIBinder interface {
	SetAPILocalHandler(name, pluginID string, metadata map[string]any, handler func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error)) bool
	RemoveAPIsByPlugin(pluginID string) int
}

var officialOpenIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

// RegisterLocalAPIs 注册 identity + 全量 adapter.napcat.*（含 snowluma/onebot 别名）
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

	registerNamed := func(name, description string, public bool, handler func(context.Context, string, string, map[string]any) (any, error)) {
		binder.SetAPILocalHandler(name, PluginID, map[string]any{
			"description": description,
			"version":     "1",
			"public":      public,
			"enabled":     true,
		}, handler)
	}

	registerNamed("adapter.avatar.get", "获取独立用户或群头像资源", true, handleAvatarGetAPI)
	registerNamed("adapter.files.resolve", "为主程序解析已登记附件的下载地址", false, handleFilesResolveAPI)
	registerNamed("adapter.napcat.action.call", "调用任意 OneBot 动作", true, handleActionCallAPI)
	registerNamed("adapter.napcat.action.call_data", "调用任意 OneBot 动作并返回 data 字段", true, handleActionCallDataAPI)

	registerTypedLocalAPIs(registerNamed)

	for apiName, action := range napcatRawActions {
		actionName := action
		registerNamed(apiName, "调用对端的 "+actionName+" 动作", true, makeRawActionHandler(actionName))
	}

	registerAliasFamily(binder, "adapter.snowluma.")
	registerAliasFamily(binder, "adapter.onebot.")
}

func registerAliasFamily(binder LocalAPIBinder, prefix string) {
	if binder == nil {
		return
	}
	aliasTargets := []string{
		"adapter.napcat.action.call",
		"adapter.napcat.action.call_data",
	}
	for name := range napcatRawActions {
		aliasTargets = append(aliasTargets, name)
	}
	for _, typed := range typedNapcatAPINames {
		aliasTargets = append(aliasTargets, typed)
	}
	for _, target := range aliasTargets {
		if !strings.HasPrefix(target, "adapter.napcat.") {
			continue
		}
		alias := prefix + strings.TrimPrefix(target, "adapter.napcat.")
		captured := target
		binder.SetAPILocalHandler(alias, PluginID, map[string]any{
			"description": "兼容别名: " + captured,
			"version":     "1",
			"public":      true,
			"enabled":     true,
		}, makeAliasHandler(captured))
	}
}

func makeAliasHandler(target string) func(context.Context, string, string, map[string]any) (any, error) {
	return func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = pluginID
		_ = componentName
		return dispatchNapcatAPI(ctx, target, args)
	}
}

func makeRawActionHandler(action string) func(context.Context, string, string, map[string]any) (any, error) {
	return func(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
		_ = pluginID
		_ = componentName
		return handleRawAction(ctx, action, args)
	}
}

func handleIdentityObservationsAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	return ActiveAdapter().handleIdentityObservations(ctx, pluginID, componentName, args)
}

func handleIdentityOnlyLeaseAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
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

func requireActiveServices() (*ActionService, *QueryService, error) {
	adapter := ActiveAdapter()
	if adapter == nil || adapter.actions == nil || adapter.query == nil {
		return nil, nil, ErrNotConnected
	}
	return adapter.actions, adapter.query, nil
}

func handleRawAction(ctx context.Context, action string, args map[string]any) (any, error) {
	actions, _, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	params := extractParams(args)
	return actions.Call(ctx, action, params)
}

func handleActionCallAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	actionName := strings.TrimSpace(asString(args["action_name"]))
	if actionName == "" {
		return nil, fmt.Errorf("action_name 不能为空")
	}
	return handleRawAction(ctx, actionName, args)
}

func handleActionCallDataAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	actions, _, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	actionName := strings.TrimSpace(asString(args["action_name"]))
	if actionName == "" {
		return nil, fmt.Errorf("action_name 不能为空")
	}
	return actions.CallData(ctx, actionName, extractParams(args))
}

func handleAvatarGetAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = ctx
	_ = pluginID
	_ = componentName
	platform := asString(args["platform"])
	if platform != "qq" {
		return map[string]any{"status": "unsupported"}, nil
	}
	targetType := firstNonEmpty(asString(args["target_type"]), "user")
	if targetType != "user" && targetType != "group" {
		return nil, fmt.Errorf("target_type 必须为 user 或 group")
	}
	targetID := strings.TrimSpace(asString(args["target_id"]))
	if officialOpenIDPattern.MatchString(targetID) && !isDecimal(targetID) {
		return map[string]any{"status": "unsupported"}, nil
	}
	normalizedID, err := normalizePositiveInt(args["target_id"], "target_id")
	if err != nil {
		return nil, err
	}
	var url string
	if targetType == "group" {
		url = fmt.Sprintf("https://p.qlogo.cn/gh/%d/%d/640", normalizedID, normalizedID)
	} else {
		url = fmt.Sprintf("https://q1.qlogo.cn/g?b=qq&nk=%d&s=640", normalizedID)
	}
	return map[string]any{"status": "available", "url": url, "expires_in": 86400}, nil
}

func handleFilesResolveAPI(ctx context.Context, pluginID, componentName string, args map[string]any) (any, error) {
	_ = pluginID
	_ = componentName
	actions, _, err := requireActiveServices()
	if err != nil {
		return nil, err
	}
	source := asMap(args["source"])
	if source == nil || asString(source["platform"]) != "qq" || asString(source["file_id"]) == "" {
		return nil, fmt.Errorf("文件来源无效")
	}
	params := map[string]any{"file_id": asString(source["file_id"])}
	var action string
	if asString(source["group_id"]) != "" {
		groupID, err := normalizePositiveInt(source["group_id"], "group_id")
		if err != nil {
			return nil, err
		}
		params["group_id"] = groupID
		busid := asInt(source["busid"], 0)
		if busid != 0 {
			n, err := normalizePositiveInt(busid, "busid")
			if err != nil {
				return nil, err
			}
			params["busid"] = n
		}
		action = "get_group_file_url"
	} else {
		userID, err := normalizePositiveInt(source["user_id"], "user_id")
		if err != nil {
			return nil, err
		}
		params["user_id"] = userID
		action = "get_private_file_url"
	}
	data, err := actions.CallData(ctx, action, params)
	if err != nil {
		return nil, err
	}
	result := asMap(data)
	if result == nil || asString(result["url"]) == "" {
		return nil, fmt.Errorf("平台未提供文件下载 URL")
	}
	return map[string]any{"url": asString(result["url"])}, nil
}

func extractParams(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	if params := asMap(args["params"]); params != nil {
		out := make(map[string]any, len(params))
		for key, value := range params {
			out[key] = value
		}
		return out
	}
	out := make(map[string]any, len(args))
	for key, value := range args {
		if key == "action_name" {
			continue
		}
		out[key] = value
	}
	return out
}

func dispatchNapcatAPI(ctx context.Context, name string, args map[string]any) (any, error) {
	if handler, ok := typedNapcatHandlers[name]; ok {
		return handler(ctx, PluginID, name, args)
	}
	if action, ok := napcatRawActions[name]; ok {
		return handleRawAction(ctx, action, args)
	}
	switch name {
	case "adapter.napcat.action.call":
		return handleActionCallAPI(ctx, PluginID, name, args)
	case "adapter.napcat.action.call_data":
		return handleActionCallDataAPI(ctx, PluginID, name, args)
	default:
		return nil, fmt.Errorf("未找到 API: %s", name)
	}
}
