package chat

import (
	"net/http"
	"sort"
	"strings"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/platform_io"
)

// chatAdapterPolicyPayload 适配器策略结果，字段顺序对齐 Python AdapterPolicyResult.to_dict
type chatAdapterPolicyPayload struct {
	Allowed    bool     `json:"allowed"`
	Configured bool     `json:"configured"`
	ChatType   string   `json:"chat_type"`
	TargetID   string   `json:"target_id"`
	ListType   string   `json:"list_type"`
	Source     string   `json:"source"`
	Reason     string   `json:"reason"`
	MatchedIDs []string `json:"matched_ids"`
}

// chatAdapterPolicyDetail 单个适配器插件的路由与放行状态，字段顺序对齐 Python _get_adapter_policy_details
type chatAdapterPolicyDetail struct {
	AdapterID    string                   `json:"adapter_id"`
	PluginID     string                   `json:"plugin_id"`
	GatewayName  string                   `json:"gateway_name"`
	Platform     string                   `json:"platform"`
	AccountID    string                   `json:"account_id"`
	Scope        string                   `json:"scope"`
	Protocol     any                      `json:"protocol"`
	RouteType    any                      `json:"route_type"`
	SendBound    bool                     `json:"send_bound"`
	ReceiveBound bool                     `json:"receive_bound"`
	Routed       bool                     `json:"routed"`
	Policy       chatAdapterPolicyPayload `json:"policy"`
}

// chatAdapterAllowance 单个聊天流的适配器放行汇总，字段顺序对齐 Python _summarize_session_adapter_allowance
type chatAdapterAllowance struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// adapterPolicyManager 返回适配器策略管理器，测试可注入独立策略文件
func (d Deps) adapterPolicyManager() (*platformio.AdapterPolicyManager, error) {
	if d.AdapterPolicy != nil {
		return d.AdapterPolicy()
	}
	return platformio.GetAdapterPolicyManager()
}

// adapterIdentityFromDescriptor 对齐 Python _get_driver_adapter_identity
func adapterIdentityFromDescriptor(desc platformio.DriverDescriptor) platformio.AdapterIdentity {
	metadata := desc.Metadata
	return platformio.AdapterIdentity{
		AdapterID:   desc.DriverID,
		PluginID:    desc.PluginID,
		GatewayName: strings.TrimSpace(stringValue(metadata["gateway_name"])),
		Platform:    desc.Platform,
		AccountID:   desc.AccountID,
		Scope:       desc.Scope,
	}
}

func isAdapterPluginDescriptor(desc platformio.DriverDescriptor) bool {
	if desc.Kind != platformio.DriverKindPlugin {
		return false
	}
	pluginType := strings.ToLower(strings.TrimSpace(stringValue(desc.Metadata["plugin_type"])))
	return pluginType == "adapter"
}

// runningAdapterIdentity 按 driver_id 查找运行中适配器身份，对齐 _get_running_adapter_identity
func (d Deps) runningAdapterIdentity(adapterID string) (platformio.AdapterIdentity, string, int) {
	normalized := strings.TrimSpace(adapterID)
	if normalized == "" {
		return platformio.AdapterIdentity{}, "缺少适配器 ID", http.StatusBadRequest
	}
	if d.PlatformAdapters == nil {
		return platformio.AdapterIdentity{}, "适配器当前未运行，无法修改策略", http.StatusNotFound
	}
	driver := d.PlatformAdapters.GetDriver(normalized)
	if driver == nil {
		return platformio.AdapterIdentity{}, "适配器当前未运行，无法修改策略", http.StatusNotFound
	}
	desc := driver.Descriptor()
	if !isAdapterPluginDescriptor(desc) {
		return platformio.AdapterIdentity{}, "目标驱动不是适配器插件", http.StatusBadRequest
	}
	return adapterIdentityFromDescriptor(desc), "", 0
}

// pluginAdapterIdentity 按 plugin_id 查找运行中适配器身份，对齐 _get_plugin_adapter_identity
func (d Deps) pluginAdapterIdentity(pluginID string) (platformio.AdapterIdentity, bool) {
	if d.PlatformAdapters == nil || strings.TrimSpace(pluginID) == "" {
		return platformio.AdapterIdentity{}, false
	}
	for _, desc := range d.PlatformAdapters.AdapterDescriptors() {
		if strings.TrimSpace(desc.PluginID) != pluginID {
			continue
		}
		if !isAdapterPluginDescriptor(desc) {
			continue
		}
		return adapterIdentityFromDescriptor(desc), true
	}
	return platformio.AdapterIdentity{}, false
}

func adapterAccountStatus(
	manager *platformio.AdapterPolicyManager,
	pluginID string,
	identity platformio.AdapterIdentity,
	active *platformio.AdapterIdentity,
) map[string]any {
	var activeJSON any
	if active != nil {
		activeJSON = adapterIdentityJSON(*active)
	}
	entries, _ := manager.ListPluginEntries(pluginID)
	if entries == nil {
		entries = []platformio.PluginPolicyEntry{}
	}
	hasEntry, _ := manager.HasAdapterPolicyEntry(identity)
	return map[string]any{
		"active_identity": activeJSON,
		"has_entry":       hasEntry,
		"account_entries": entries,
	}
}

// chatAdapterDetails 返回当前聊天流在同平台适配器插件中的路由与放行状态
func (d Deps) chatAdapterDetails(rec *chatsession.Record) ([]chatAdapterPolicyDetail, error) {
	details := make([]chatAdapterPolicyDetail, 0)
	if rec == nil || d.PlatformAdapters == nil {
		return details, nil
	}
	platform := strings.TrimSpace(rec.Platform)
	targetID := chatTargetID(rec)
	if platform == "" || targetID == "" {
		return details, nil
	}
	routeKey, err := (platformio.RouteKey{
		Platform:  platform,
		AccountID: pointerString(rec.AccountID),
		Scope:     pointerString(rec.Scope),
	}).Normalize()
	if err != nil {
		return details, nil
	}
	manager, err := d.adapterPolicyManager()
	if err != nil {
		return nil, err
	}
	chatType := chatTypeOf(rec)
	for _, status := range d.PlatformAdapters.AdapterRouteStatuses(routeKey) {
		desc := status.Descriptor
		if desc.Kind != platformio.DriverKindPlugin {
			continue
		}
		metadata := desc.Metadata
		pluginType := strings.ToLower(strings.TrimSpace(stringValue(metadata["plugin_type"])))
		if pluginType != "adapter" {
			continue
		}
		identity := platformio.AdapterIdentity{
			AdapterID:   desc.DriverID,
			PluginID:    desc.PluginID,
			GatewayName: strings.TrimSpace(stringValue(metadata["gateway_name"])),
			Platform:    desc.Platform,
			AccountID:   desc.AccountID,
			Scope:       desc.Scope,
		}
		policy, err := manager.Evaluate(identity, chatType, targetID)
		if err != nil {
			return nil, err
		}
		routed := status.SendBound || status.ReceiveBound
		details = append(details, chatAdapterPolicyDetail{
			AdapterID:    desc.DriverID,
			PluginID:     desc.PluginID,
			GatewayName:  identity.GatewayName,
			Platform:     desc.Platform,
			AccountID:    desc.AccountID,
			Scope:        desc.Scope,
			Protocol:     chatAdapterMetadataValue(metadata, "protocol"),
			RouteType:    chatAdapterMetadataValue(metadata, "route_type"),
			SendBound:    status.SendBound,
			ReceiveBound: status.ReceiveBound,
			Routed:       routed,
			Policy:       chatAdapterPolicyPayloadFromResult(policy),
		})
	}
	// Python 按 (not routed, plugin_id, gateway_name) 排序；追加 adapter_id 保证同网关多驱动时结果稳定
	sort.SliceStable(details, func(i, j int) bool {
		left, right := details[i], details[j]
		if left.Routed != right.Routed {
			return left.Routed
		}
		if left.PluginID != right.PluginID {
			return left.PluginID < right.PluginID
		}
		if left.GatewayName != right.GatewayName {
			return left.GatewayName < right.GatewayName
		}
		return left.AdapterID < right.AdapterID
	})
	return details, nil
}

// chatAdapterAllowance 汇总单个聊天流是否被同平台适配器放行
func (d Deps) chatAdapterAllowance(rec *chatsession.Record) (chatAdapterAllowance, error) {
	unavailable := chatAdapterAllowance{Allowed: true, Reason: "adapter_unavailable"}
	if rec == nil {
		return unavailable, nil
	}
	details, err := d.chatAdapterDetails(rec)
	if err != nil {
		return chatAdapterAllowance{}, err
	}
	platform := strings.ToLower(strings.TrimSpace(rec.Platform))
	platformAdapters := make([]chatAdapterPolicyDetail, 0, len(details))
	for _, detail := range details {
		if strings.ToLower(strings.TrimSpace(detail.Platform)) == platform {
			platformAdapters = append(platformAdapters, detail)
		}
	}
	// 正在承载当前聊天流的适配器优先，没有接入记录时退回同平台全部适配器
	candidates := make([]chatAdapterPolicyDetail, 0, len(platformAdapters))
	for _, detail := range platformAdapters {
		if detail.Routed {
			candidates = append(candidates, detail)
		}
	}
	if len(candidates) == 0 {
		candidates = platformAdapters
	}
	if len(candidates) == 0 {
		return unavailable, nil
	}
	for _, candidate := range candidates {
		if !candidate.Policy.Allowed {
			return chatAdapterAllowance{Allowed: false, Reason: candidate.Policy.Reason}, nil
		}
	}
	return chatAdapterAllowance{Allowed: true, Reason: ""}, nil
}

// chatAdapterPolicyPayloadFromResult 转换策略结果并保证 matched_ids 序列化为数组
func chatAdapterPolicyPayloadFromResult(result platformio.AdapterPolicyResult) chatAdapterPolicyPayload {
	matchedIDs := make([]string, len(result.MatchedIDs))
	copy(matchedIDs, result.MatchedIDs)
	return chatAdapterPolicyPayload{
		Allowed:    result.Allowed,
		Configured: result.Configured,
		ChatType:   string(result.ChatType),
		TargetID:   result.TargetID,
		ListType:   result.ListType,
		Source:     result.Source,
		Reason:     result.Reason,
		MatchedIDs: matchedIDs,
	}
}

// chatAdapterMetadataValue 对齐 Python metadata.get(key) or ""：缺失与空字符串归一为空串
func chatAdapterMetadataValue(metadata map[string]any, key string) any {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return ""
	}
	return value
}
