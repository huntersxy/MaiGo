package platformio

import (
	"slices"
	"strings"
)

// SetAdapterPolicy 原子更新一个精确适配器身份的群聊与私聊规则
// policyGroups 为 nil 表示不修改分组；activeGroup 仅在提供分组时生效
func (m *AdapterPolicyManager) SetAdapterPolicy(
	identity AdapterIdentity,
	policies map[ChatType]ChatPolicyEditor,
	policyGroups []PolicyGroupEditor,
	activeGroup string,
) error {
	if len(identityToPolicyMatch(identity)) == 0 {
		return errEmptyAdapterIdentity
	}

	normalizedPolicies, err := normalizeEditorPolicies(policies)
	if err != nil {
		return err
	}

	var normalizedGroups []PolicyGroupEditor
	if policyGroups != nil {
		normalizedGroups, err = normalizePolicyGroups(policyGroups, activeGroup, normalizedPolicies)
		if err != nil {
			return err
		}
	} else if strings.TrimSpace(activeGroup) != "" {
		return errMissingPolicyGroupsForEdit
	}

	policyDoc, err := m.loadPolicyDoc()
	if err != nil {
		return err
	}
	adapters := ensureAdapterTables(policyDoc)
	adapterPolicy, adapters := findOrCreateAdapterPolicy(adapters, identity)
	if normalizedGroups != nil {
		adapterPolicy[policyGroupsKey] = groupsToDoc(normalizedGroups)
		adapterPolicy[policyActiveGroupKey] = strings.TrimSpace(activeGroup)
	}

	for _, chatType := range supportedChatTypes {
		normalizedPolicy := normalizedPolicies[chatType]
		chatPolicy := ensureChatPolicyTable(adapterPolicy, chatType)
		if normalizedGroups != nil {
			// 分组保存使用完整的新规则，清除旧格式字段，避免旧名单继续影响新组
			for _, key := range []string{policyListTypeKey, policyIDsKey, policyDisabledKey} {
				delete(chatPolicy, key)
			}
		}
		if normalizedPolicy.DefaultAction == defaultActionInherit {
			delete(chatPolicy, policyDefaultActionKey)
		} else {
			chatPolicy[policyDefaultActionKey] = normalizedPolicy.DefaultAction
		}
		setOrRemoveIDList(chatPolicy, policyAllowIDsKey, normalizedPolicy.AllowIDs)
		setOrRemoveIDList(chatPolicy, policyDenyIDsKey, normalizedPolicy.DenyIDs)
		setOrRemoveIDList(chatPolicy, policyDisabledIDsKey, normalizedPolicy.DisabledIDs)
		pruneEmptyUIPolicy(adapterPolicy, chatType)
	}

	adapters = pruneEmptyAdapterPolicy(adapters, adapterPolicy)
	policyDoc[policyAdaptersKey] = adapters
	if err := syncActiveGroup(adapterPolicy); err != nil {
		return err
	}
	return m.writePolicyDoc(policyDoc)
}

// SetDefaultAction 设置某类聊天在没有命中具体规则时的全局默认动作
func (m *AdapterPolicyManager) SetDefaultAction(chatType string, action string) error {
	normalizedChatType, err := NormalizeChatType(chatType)
	if err != nil {
		return err
	}
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	if !isSupportedDefaultAction(normalizedAction) {
		return errInvalidGlobalAction
	}

	policyDoc, err := m.loadPolicyDoc()
	if err != nil {
		return err
	}
	defaults, ok := policyDoc[policyDefaultsKey].(map[string]any)
	if !ok {
		defaults = map[string]any{}
		policyDoc[policyDefaultsKey] = defaults
	}
	chatPolicy := ensureChatPolicyTable(defaults, normalizedChatType)
	chatPolicy[policyDefaultActionKey] = normalizedAction
	return m.writePolicyDoc(policyDoc)
}

// SetChatOverride 为某个适配器和聊天流写入显式放行/阻止覆盖规则
func (m *AdapterPolicyManager) SetChatOverride(
	identity AdapterIdentity,
	chatType string,
	targetID string,
	action string,
) error {
	normalizedChatType, err := NormalizeChatType(chatType)
	if err != nil {
		return err
	}
	normalizedTargetID := strings.TrimSpace(targetID)
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	if normalizedTargetID == "" {
		return errEmptyOverrideTargetID
	}
	if !isSupportedOverrideAction(normalizedAction) {
		return errInvalidOverrideAction
	}

	policyDoc, err := m.loadPolicyDoc()
	if err != nil {
		return err
	}
	adapters := ensureAdapterTables(policyDoc)
	adapterPolicy, adapters := findOrCreateAdapterPolicy(adapters, identity)
	chatPolicy := ensureChatPolicyTable(adapterPolicy, normalizedChatType)

	allowIDs := normalizeIDList(chatPolicy[policyAllowIDsKey])
	denyIDs := normalizeIDList(chatPolicy[policyDenyIDsKey])
	switch normalizedAction {
	case defaultActionAllow:
		allowIDs = appendUniqueString(allowIDs, normalizedTargetID)
		denyIDs = removeString(denyIDs, normalizedTargetID)
	case defaultActionBlock:
		denyIDs = appendUniqueString(denyIDs, normalizedTargetID)
		allowIDs = removeString(allowIDs, normalizedTargetID)
	default:
		allowIDs = removeString(allowIDs, normalizedTargetID)
		denyIDs = removeString(denyIDs, normalizedTargetID)
	}

	// 显式修改聊天覆盖规则时恢复该条目，避免停用状态屏蔽用户的新选择
	disabledIDs := removeString(normalizeIDList(chatPolicy[policyDisabledIDsKey]), normalizedTargetID)
	setOrRemoveIDList(chatPolicy, policyDisabledIDsKey, disabledIDs)
	setOrRemoveIDList(chatPolicy, policyAllowIDsKey, allowIDs)
	setOrRemoveIDList(chatPolicy, policyDenyIDsKey, denyIDs)
	pruneEmptyUIPolicy(adapterPolicy, normalizedChatType)
	adapters = pruneEmptyAdapterPolicy(adapters, adapterPolicy)
	policyDoc[policyAdaptersKey] = adapters
	if err := syncActiveGroup(adapterPolicy); err != nil {
		return err
	}
	return m.writePolicyDoc(policyDoc)
}

// RemoveChatOverrides 删除指定聊天目标在匹配适配器策略中的显式覆盖规则
func (m *AdapterPolicyManager) RemoveChatOverrides(
	chatType string,
	targetID string,
	platform string,
	accountID string,
	scope string,
) (int, error) {
	normalizedChatType, err := NormalizeChatType(chatType)
	if err != nil {
		return 0, err
	}
	normalizedTargetID := strings.TrimSpace(targetID)
	normalizedPlatform := strings.TrimSpace(platform)
	normalizedAccountID := strings.TrimSpace(accountID)
	normalizedScope := strings.TrimSpace(scope)
	if normalizedTargetID == "" {
		return 0, errEmptyOverrideTargetID
	}
	if normalizedPlatform == "" {
		return 0, errEmptyOverridePlatform
	}

	policyDoc, err := m.loadPolicyDoc()
	if err != nil {
		return 0, err
	}
	adapters, ok := policyDoc[policyAdaptersKey].([]any)
	if !ok {
		return 0, nil
	}

	removedCount := 0
	// 遍历快照，删除条目时只改动 adapters 本身
	snapshot := make([]any, len(adapters))
	copy(snapshot, adapters)
	for _, rawAdapter := range snapshot {
		adapterPolicy, ok := rawAdapter.(map[string]any)
		if !ok {
			continue
		}
		if !policyMatchesChatScope(adapterPolicy, normalizedPlatform, normalizedAccountID, normalizedScope) {
			continue
		}

		// 同时清理未启用分组，避免删除聊天流后切换策略又恢复其旧覆盖规则
		activeGroup := strings.TrimSpace(policyStringValue(adapterPolicy[policyActiveGroupKey]))
		policySources := []map[string]any{adapterPolicy}
		if groups, ok := adapterPolicy[policyGroupsKey].([]any); ok {
			for _, rawGroup := range groups {
				group, ok := rawGroup.(map[string]any)
				if !ok {
					continue
				}
				if strings.TrimSpace(policyStringValue(group[policyGroupIDKey])) == activeGroup {
					continue
				}
				policySources = append(policySources, group)
			}
		}

		for sourceIndex, policySource := range policySources {
			chatPolicy, ok := policySource[string(normalizedChatType)].(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{policyAllowIDsKey, policyDenyIDsKey} {
				ids := normalizeIDList(chatPolicy[key])
				nextIDs := removeString(ids, normalizedTargetID)
				if len(nextIDs) == len(ids) {
					continue
				}
				removedCount++
				if sourceIndex == 0 {
					setOrRemoveIDList(chatPolicy, key, nextIDs)
					continue
				}
				chatPolicy[key] = toAnyList(nextIDs)
			}
		}

		pruneEmptyUIPolicy(adapterPolicy, normalizedChatType)
		adapters = pruneEmptyAdapterPolicy(adapters, adapterPolicy)
		if err := syncActiveGroup(adapterPolicy); err != nil {
			return removedCount, err
		}
	}

	policyDoc[policyAdaptersKey] = adapters
	if removedCount == 0 {
		return 0, nil
	}
	if err := m.writePolicyDoc(policyDoc); err != nil {
		return removedCount, err
	}
	return removedCount, nil
}

// normalizePolicyGroups 校验并规范化策略分组，当前生效分组必须与生效规则一致
func normalizePolicyGroups(
	policyGroups []PolicyGroupEditor,
	activeGroup string,
	normalizedPolicies map[ChatType]ChatPolicyEditor,
) ([]PolicyGroupEditor, error) {
	if len(policyGroups) == 0 {
		return nil, errEmptyPolicyGroups
	}
	normalizedGroups := make([]PolicyGroupEditor, 0, len(policyGroups))
	groupIDs := make(map[string]struct{}, len(policyGroups))
	groupNames := make(map[string]struct{}, len(policyGroups))
	for _, group := range policyGroups {
		groupID := strings.TrimSpace(group.ID)
		name := strings.TrimSpace(group.Name)
		if groupID == "" || name == "" {
			return nil, errEmptyPolicyGroupField
		}
		if _, ok := groupIDs[groupID]; ok {
			return nil, errDuplicatePolicyGroup
		}
		if _, ok := groupNames[name]; ok {
			return nil, errDuplicatePolicyGroup
		}
		groupPolicies, err := normalizeEditorPolicies(map[ChatType]ChatPolicyEditor{
			ChatTypeGroup:   group.Group,
			ChatTypePrivate: group.Private,
		})
		if err != nil {
			return nil, err
		}
		groupIDs[groupID] = struct{}{}
		groupNames[name] = struct{}{}
		normalizedGroups = append(normalizedGroups, PolicyGroupEditor{
			ID:      groupID,
			Name:    name,
			Group:   groupPolicies[ChatTypeGroup],
			Private: groupPolicies[ChatTypePrivate],
		})
	}

	normalizedActiveGroup := strings.TrimSpace(activeGroup)
	if _, ok := groupIDs[normalizedActiveGroup]; !ok {
		return nil, errPolicyGroupMissing
	}
	for _, group := range normalizedGroups {
		if group.ID != normalizedActiveGroup {
			continue
		}
		for _, chatType := range supportedChatTypes {
			selected := group.Group
			if chatType == ChatTypePrivate {
				selected = group.Private
			}
			if !chatPolicyEditorEqual(selected, normalizedPolicies[chatType]) {
				return nil, errPolicyGroupMismatch
			}
		}
	}
	return normalizedGroups, nil
}

// groupsToDoc 将策略分组转换为策略文档结构
func groupsToDoc(groups []PolicyGroupEditor) []any {
	items := make([]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, map[string]any{
			policyGroupIDKey:        group.ID,
			policyGroupNameKey:      group.Name,
			string(ChatTypeGroup):   chatPolicyToDoc(group.Group),
			string(ChatTypePrivate): chatPolicyToDoc(group.Private),
		})
	}
	return items
}

// chatPolicyToDoc 将编辑规则转换为策略文档结构，空名单也保留以对齐分组快照
func chatPolicyToDoc(policy ChatPolicyEditor) map[string]any {
	return map[string]any{
		policyDefaultActionKey: policy.DefaultAction,
		policyAllowIDsKey:      toAnyList(policy.AllowIDs),
		policyDenyIDsKey:       toAnyList(policy.DenyIDs),
	}
}

// chatPolicyEditorEqual 判断两份编辑规则是否完全一致
func chatPolicyEditorEqual(left, right ChatPolicyEditor) bool {
	if left.DefaultAction != right.DefaultAction {
		return false
	}
	if len(left.AllowIDs) != len(right.AllowIDs) || len(left.DenyIDs) != len(right.DenyIDs) {
		return false
	}
	for index := range left.AllowIDs {
		if left.AllowIDs[index] != right.AllowIDs[index] {
			return false
		}
	}
	for index := range left.DenyIDs {
		if left.DenyIDs[index] != right.DenyIDs[index] {
			return false
		}
	}
	return true
}

// appendUniqueString 追加不重复的 ID
func appendUniqueString(values []string, target string) []string {
	if slices.Contains(values, target) {
		return values
	}
	return append(values, target)
}

// removeString 移除全部匹配的 ID
func removeString(values []string, target string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value == target {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
