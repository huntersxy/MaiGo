package platformio

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ChatPolicyEditor 单个聊天类型的可编辑规则
type ChatPolicyEditor struct {
	DefaultAction string   `json:"default_action"`
	DisabledIDs   []string `json:"disabled_ids,omitempty"`
	AllowIDs      []string `json:"allow_ids"`
	DenyIDs       []string `json:"deny_ids"`
}

// PolicyGroupEditor 策略分组及其规则快照
type PolicyGroupEditor struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Group   ChatPolicyEditor `json:"group"`
	Private ChatPolicyEditor `json:"private"`
}

// AdapterPolicyEditor 一个适配器身份在面板上可编辑的完整规则
type AdapterPolicyEditor struct {
	Group        ChatPolicyEditor    `json:"group"`
	Private      ChatPolicyEditor    `json:"private"`
	ActiveGroup  string              `json:"active_group"`
	PolicyGroups []PolicyGroupEditor `json:"policy_groups,omitempty"`
}

// PluginChatRule 某插件条目在某一聊天类型上的规则摘要
type PluginChatRule struct {
	DefaultAction string   `json:"default_action"`
	DisabledIDs   []string `json:"disabled_ids"`
	AllowIDs      []string `json:"allow_ids"`
	DenyIDs       []string `json:"deny_ids"`
}

// PluginPolicyEntry 某插件名下的规则条目身份摘要
type PluginPolicyEntry struct {
	AdapterID   string                      `json:"adapter_id"`
	AccountID   string                      `json:"account_id"`
	Platform    string                      `json:"platform"`
	GatewayName string                      `json:"gateway_name"`
	Scope       string                      `json:"scope"`
	Rules       map[ChatType]PluginChatRule `json:"rules"`
}

// GetAdapterPolicy 返回一个适配器身份可由 WebUI 编辑的主程序规则
// 与 Evaluate 使用同一匹配标准：在条目指定的身份字段与 identity 一致的
// 现有条目中取 specificity 最高者，保证面板读到运行时真正生效的那条
func (m *AdapterPolicyManager) GetAdapterPolicy(identity AdapterIdentity) (AdapterPolicyEditor, error) {
	if len(identityToPolicyMatch(identity)) == 0 {
		return AdapterPolicyEditor{}, errEmptyAdapterIdentity
	}
	policyData, err := m.loadPolicyData()
	if err != nil {
		return AdapterPolicyEditor{}, err
	}
	adapters, _ := policyData[policyAdaptersKey].([]any)
	bestPolicy := findDisplayAdapterPolicy(adapters, identity)

	result := AdapterPolicyEditor{}
	for _, chatType := range supportedChatTypes {
		editor := ChatPolicyEditor{
			DefaultAction: defaultActionInherit,
			AllowIDs:      []string{},
			DenyIDs:       []string{},
		}
		var typedPolicy map[string]any
		if bestPolicy != nil {
			typedPolicy = resolveTypedPolicy(bestPolicy, chatType)
		}
		if typedPolicy != nil {
			applyEditorPolicy(&editor, typedPolicy)
		}
		switch chatType {
		case ChatTypeGroup:
			result.Group = editor
		case ChatTypePrivate:
			result.Private = editor
		}
	}

	if bestPolicy == nil {
		return result, nil
	}
	groupsValue, hasGroups := bestPolicy[policyGroupsKey]
	if !hasGroups {
		return result, nil
	}
	groups, ok := groupsValue.([]any)
	if !ok || len(groups) == 0 {
		return AdapterPolicyEditor{}, errEmptyPolicyGroupContent
	}
	activeGroup := strings.TrimSpace(policyStringValue(bestPolicy[policyActiveGroupKey]))
	result.ActiveGroup = activeGroup
	result.PolicyGroups = make([]PolicyGroupEditor, 0, len(groups))
	activeFound := false
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			return AdapterPolicyEditor{}, errInvalidPolicyGroups
		}
		groupEditor := PolicyGroupEditor{
			ID:   strings.TrimSpace(policyStringValue(group[policyGroupIDKey])),
			Name: strings.TrimSpace(policyStringValue(group[policyGroupNameKey])),
		}
		if groupEditor.ID == "" || groupEditor.Name == "" {
			return AdapterPolicyEditor{}, errEmptyPolicyGroupField
		}
		groupPolicies, err := chatPoliciesFromDoc(group)
		if err != nil {
			return AdapterPolicyEditor{}, err
		}
		normalizedGroup, err := normalizeEditorPolicies(groupPolicies)
		if err != nil {
			return AdapterPolicyEditor{}, err
		}
		groupEditor.Group = normalizedGroup[ChatTypeGroup]
		groupEditor.Private = normalizedGroup[ChatTypePrivate]
		// 聊天流详情页也能修改当前规则；读取时以实际生效内容更新当前组快照
		if groupEditor.ID == activeGroup {
			groupEditor.Group = result.Group
			groupEditor.Private = result.Private
			activeFound = true
		}
		result.PolicyGroups = append(result.PolicyGroups, groupEditor)
	}
	if !activeFound {
		return AdapterPolicyEditor{}, errPolicyGroupMissing
	}
	return result, nil
}

// applyEditorPolicy 把策略文件中的聊天规则填充到编辑器视图
func applyEditorPolicy(editor *ChatPolicyEditor, typedPolicy map[string]any) {
	if typedPolicy == nil {
		return
	}
	if policyBoolValue(typedPolicy[policyDisabledKey]) {
		editor.DefaultAction = defaultActionBlock
		editor.AllowIDs = []string{}
		editor.DenyIDs = []string{"*"}
		editor.DisabledIDs = nil
		return
	}
	configuredAction := strings.ToLower(policyStringValue(typedPolicy[policyDefaultActionKey]))
	if isSupportedDefaultAction(configuredAction) {
		editor.DefaultAction = configuredAction
	}
	editor.AllowIDs = normalizeIDList(typedPolicy[policyAllowIDsKey])
	editor.DenyIDs = normalizeIDList(typedPolicy[policyDenyIDsKey])
	editor.DisabledIDs = normalizeIDList(typedPolicy[policyDisabledIDsKey])

	// 旧版 list_type/ids 转换成等价的默认动作与覆盖名单，保留运行时显式覆盖优先的语义
	_, hasListType := typedPolicy[policyListTypeKey]
	_, hasIDs := typedPolicy[policyIDsKey]
	if hasListType || hasIDs {
		legacyIDs := normalizeIDList(typedPolicy[policyIDsKey])
		listType := strings.ToLower(policyStringValue(typedPolicy[policyListTypeKey]))
		if listType == "" {
			listType = listTypeWhitelist
		}
		isBlacklist := listType == listTypeBlacklist
		wildcard := slices.Contains(legacyIDs, "*")
		if isBlacklist {
			editor.DefaultAction = defaultActionAllow
			if wildcard {
				editor.DefaultAction = defaultActionBlock
			}
		} else {
			editor.DefaultAction = defaultActionBlock
			if wildcard {
				editor.DefaultAction = defaultActionAllow
			}
		}
		activeOverrides := make(map[string]struct{}, len(editor.AllowIDs)+len(editor.DenyIDs))
		for _, id := range append(append([]string{}, editor.AllowIDs...), editor.DenyIDs...) {
			if !slices.Contains(editor.DisabledIDs, id) {
				activeOverrides[id] = struct{}{}
			}
		}
		// 生效的显式通配符已覆盖所有目标；旧名单通配符则已由默认动作表达
		if _, covered := activeOverrides["*"]; !covered && !wildcard {
			legacyOverrides := make([]string, 0, len(legacyIDs))
			for _, id := range legacyIDs {
				if _, active := activeOverrides[id]; !active {
					legacyOverrides = append(legacyOverrides, id)
				}
			}
			if isBlacklist {
				editor.AllowIDs = excludeStrings(editor.AllowIDs, legacyOverrides)
				editor.DenyIDs = dedupeStrings(append(editor.DenyIDs, legacyOverrides...))
			} else {
				editor.DenyIDs = excludeStrings(editor.DenyIDs, legacyOverrides)
				editor.AllowIDs = dedupeStrings(append(editor.AllowIDs, legacyOverrides...))
			}
			// 停用只屏蔽旧规则中的显式覆盖，不屏蔽其下仍生效的旧名单
			editor.DisabledIDs = excludeStrings(editor.DisabledIDs, legacyOverrides)
		}
		editor.AllowIDs = excludeStrings(editor.AllowIDs, editor.DenyIDs)
	}
}

// HasAdapterPolicyEntry 判断当前身份是否命中已有规则条目
// 供面板区分「专属规则」与「默认继承」
func (m *AdapterPolicyManager) HasAdapterPolicyEntry(identity AdapterIdentity) (bool, error) {
	policyData, err := m.loadPolicyData()
	if err != nil {
		return false, err
	}
	adapters, _ := policyData[policyAdaptersKey].([]any)
	return findDisplayAdapterPolicy(adapters, identity) != nil, nil
}

// ListPluginEntries 列出某插件名下全部规则条目的身份摘要
// 适配器换账号登录后旧账号条目不再生效但保留在配置中
// 面板据此展示各账号的规则归属，避免把旧条目误读为当前生效规则
func (m *AdapterPolicyManager) ListPluginEntries(pluginID string) ([]PluginPolicyEntry, error) {
	normalizedPluginID := strings.TrimSpace(pluginID)
	if normalizedPluginID == "" {
		return []PluginPolicyEntry{}, nil
	}
	policyData, err := m.loadPolicyData()
	if err != nil {
		return nil, err
	}
	adapters, ok := policyData[policyAdaptersKey].([]any)
	if !ok {
		return []PluginPolicyEntry{}, nil
	}
	entries := make([]PluginPolicyEntry, 0, len(adapters))
	for _, rawItem := range adapters {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(policyStringValue(item[policyPluginIDKey])) != normalizedPluginID {
			continue
		}
		rules := make(map[ChatType]PluginChatRule, len(supportedChatTypes))
		for _, chatType := range supportedChatTypes {
			typedPolicy := resolveTypedPolicy(item, chatType)
			if typedPolicy == nil {
				continue
			}
			defaultAction := strings.TrimSpace(policyStringValue(typedPolicy[policyDefaultActionKey]))
			if defaultAction == "" {
				defaultAction = defaultActionInherit
			}
			rules[chatType] = PluginChatRule{
				DefaultAction: defaultAction,
				DisabledIDs:   normalizeIDList(typedPolicy[policyDisabledIDsKey]),
				AllowIDs:      normalizeIDList(typedPolicy[policyAllowIDsKey]),
				DenyIDs:       normalizeIDList(typedPolicy[policyDenyIDsKey]),
			}
		}
		entries = append(entries, PluginPolicyEntry{
			AdapterID:   strings.TrimSpace(policyStringValue(item[policyAdapterIDKey])),
			AccountID:   strings.TrimSpace(policyStringValue(item[policyAccountIDKey])),
			Platform:    strings.TrimSpace(policyStringValue(item[policyPlatformKey])),
			GatewayName: strings.TrimSpace(policyStringValue(item[policyGatewayNameKey])),
			Scope:       strings.TrimSpace(policyStringValue(item[policyScopeKey])),
			Rules:       rules,
		})
	}
	return entries, nil
}

// chatPoliciesFromDoc 从策略条目或分组中取出两类聊天规则
func chatPoliciesFromDoc(table map[string]any) (map[ChatType]ChatPolicyEditor, error) {
	policies := make(map[ChatType]ChatPolicyEditor, len(supportedChatTypes))
	for _, chatType := range supportedChatTypes {
		rawPolicy, ok := table[string(chatType)].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("缺少 %s 放行规则", chatType)
		}
		policies[chatType] = ChatPolicyEditor{
			DefaultAction: policyStringValue(rawPolicy[policyDefaultActionKey]),
			DisabledIDs:   normalizeIDList(rawPolicy[policyDisabledIDsKey]),
			AllowIDs:      normalizeIDList(rawPolicy[policyAllowIDsKey]),
			DenyIDs:       normalizeIDList(rawPolicy[policyDenyIDsKey]),
		}
	}
	return policies, nil
}

// normalizeEditorPolicies 校验并规范化一组群聊、私聊编辑规则
func normalizeEditorPolicies(policies map[ChatType]ChatPolicyEditor) (map[ChatType]ChatPolicyEditor, error) {
	normalized := make(map[ChatType]ChatPolicyEditor, len(supportedChatTypes))
	for _, chatType := range supportedChatTypes {
		rawPolicy, ok := policies[chatType]
		if !ok {
			return nil, fmt.Errorf("缺少 %s 放行规则", chatType)
		}
		defaultAction := strings.ToLower(strings.TrimSpace(rawPolicy.DefaultAction))
		if defaultAction == "" {
			defaultAction = defaultActionInherit
		}
		if !isSupportedOverrideAction(defaultAction) {
			return nil, errInvalidDefaultAction
		}
		allowIDs := normalizeStringIDList(rawPolicy.AllowIDs)
		denyIDs := normalizeStringIDList(rawPolicy.DenyIDs)
		conflictingIDs := conflictingValues(allowIDs, denyIDs)
		if len(conflictingIDs) > 0 {
			return nil, fmt.Errorf("同一 ID 不能同时放行和拒绝: %s", strings.Join(conflictingIDs, ", "))
		}
		disabledIDs := make([]string, 0)
		for _, id := range normalizeStringIDList(rawPolicy.DisabledIDs) {
			if slices.Contains(allowIDs, id) || slices.Contains(denyIDs, id) {
				disabledIDs = append(disabledIDs, id)
			}
		}
		normalized[chatType] = ChatPolicyEditor{
			DefaultAction: defaultAction,
			DisabledIDs:   disabledIDs,
			AllowIDs:      allowIDs,
			DenyIDs:       denyIDs,
		}
	}
	return normalized, nil
}

// dedupeStrings 保留首次出现的顺序去重
func dedupeStrings(values []string) []string {
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		if slices.Contains(deduped, value) {
			continue
		}
		deduped = append(deduped, value)
	}
	return deduped
}

// excludeStrings 从 values 中移除 excluded 中的元素，并保留顺序去重
func excludeStrings(values []string, excluded []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if slices.Contains(excluded, value) || slices.Contains(filtered, value) {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

// conflictingValues 返回两个名单的交集并按字典序排序
func conflictingValues(left, right []string) []string {
	conflicts := make([]string, 0)
	for _, value := range left {
		if slices.Contains(right, value) && !slices.Contains(conflicts, value) {
			conflicts = append(conflicts, value)
		}
	}
	sort.Strings(conflicts)
	return conflicts
}
