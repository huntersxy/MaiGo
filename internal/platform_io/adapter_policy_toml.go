package platformio

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// 策略文件字段名，与 MaiBot adapter_policy.toml 保持一致
const (
	policyDefaultsKey      = "defaults"
	policyAdaptersKey      = "adapters"
	policyAdapterIDKey     = "adapter_id"
	policyPluginIDKey      = "plugin_id"
	policyGatewayNameKey   = "gateway_name"
	policyPlatformKey      = "platform"
	policyAccountIDKey     = "account_id"
	policyScopeKey         = "scope"
	policyDisabledKey      = "disabled"
	policyAllowIDsKey      = "allow_ids"
	policyDenyIDsKey       = "deny_ids"
	policyListTypeKey      = "list_type"
	policyIDsKey           = "ids"
	policyDefaultActionKey = "default_action"
	policyGroupsKey        = "policy_groups"
	policyActiveGroupKey   = "active_group"
	policyGroupIDKey       = "id"
	policyGroupNameKey     = "name"
)

// 默认动作与名单类型取值，与 MaiBot adapter_policy 常量一致
const (
	defaultActionAllow   = "allow"
	defaultActionBlock   = "block"
	defaultActionInherit = "inherit"

	listTypeOverride  = "override"
	listTypeDefault   = "default"
	listTypeWhitelist = "whitelist"
	listTypeBlacklist = "blacklist"
)

// 策略求值原因，与 MaiBot AdapterPolicyResult.reason 一致
const (
	reasonAdapterDisabled      = "adapter_disabled"
	reasonMatchedDenyOverride  = "matched_deny_override"
	reasonMatchedAllowOverride = "matched_allow_override"
	reasonMatchedWhitelist     = "matched_whitelist"
	reasonNotInWhitelist       = "not_in_whitelist"
	reasonMatchedBlacklist     = "matched_blacklist"
	reasonNotInBlacklist       = "not_in_blacklist"
)

// policyIdentityKeys 策略条目的身份字段
var policyIdentityKeys = []string{
	policyAdapterIDKey,
	policyPluginIDKey,
	policyGatewayNameKey,
	policyPlatformKey,
	policyAccountIDKey,
	policyScopeKey,
}

// policyStringValue 对齐 Python str(value or "").strip()
func policyStringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if !typed {
			return ""
		}
		// Python str(True) 为 True，调用方统一走 lower() 后再比较
		return "True"
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		if typed == 0 {
			return ""
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case []any:
		if len(typed) == 0 {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(typed))
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

// policyBoolValue 对齐 Python bool(policy.get(key))
func policyBoolValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// hasPolicyKey 判断策略表是否显式声明了某个键
func hasPolicyKey(policy map[string]any, key string) bool {
	_, ok := policy[key]
	return ok
}

// normalizeIDList 规范化策略文件中的 ID 名单
func normalizeIDList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	normalizedIDs := make([]string, 0, len(items))
	for _, item := range items {
		normalizedItem := policyStringValue(item)
		if normalizedItem == "" || containsString(normalizedIDs, normalizedItem) {
			continue
		}
		normalizedIDs = append(normalizedIDs, normalizedItem)
	}
	return normalizedIDs
}

// normalizeStringIDList 规范化调用方直接传入的字符串名单
func normalizeStringIDList(values []string) []string {
	normalizedIDs := make([]string, 0, len(values))
	for _, value := range values {
		normalizedItem := strings.TrimSpace(value)
		if normalizedItem == "" || containsString(normalizedIDs, normalizedItem) {
			continue
		}
		normalizedIDs = append(normalizedIDs, normalizedItem)
	}
	return normalizedIDs
}

// toAnyList 将字符串名单转换为策略文档中的数组
func toAnyList(values []string) []any {
	items := make([]any, len(values))
	for index, value := range values {
		items[index] = value
	}
	return items
}

// copyAnyList 复制策略文档中的原始数组
func copyAnyList(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return []any{}
	}
	copied := make([]any, len(items))
	copy(copied, items)
	return copied
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isSupportedDefaultAction(action string) bool {
	return action == defaultActionAllow || action == defaultActionBlock
}

func isSupportedOverrideAction(action string) bool {
	return action == defaultActionAllow || action == defaultActionBlock || action == defaultActionInherit
}

// loadPolicyData 读取策略 TOML；不存在时返回空策略
func (m *AdapterPolicyManager) loadPolicyData() (map[string]any, error) {
	return m.loadPolicyDoc()
}

// loadPolicyDoc 读取可写 TOML 文档；不存在时返回空文档
func (m *AdapterPolicyManager) loadPolicyDoc() (map[string]any, error) {
	if m == nil || strings.TrimSpace(m.policyPath) == "" {
		return nil, errEmptyPolicyPath
	}
	data, err := os.ReadFile(m.policyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("读取适配器策略文件失败: %w", err)
	}
	policyDoc := map[string]any{}
	if len(data) == 0 {
		return policyDoc, nil
	}
	if err := toml.Unmarshal(data, &policyDoc); err != nil {
		return nil, fmt.Errorf("解析适配器策略文件失败: %w", err)
	}
	return policyDoc, nil
}

// writePolicyDoc 原子写入策略文档，避免进程中断留下半份配置
// 与 Python tomlkit 不同，go-toml 不保留注释与键顺序
func (m *AdapterPolicyManager) writePolicyDoc(policyDoc map[string]any) error {
	if m == nil || strings.TrimSpace(m.policyPath) == "" {
		return errEmptyPolicyPath
	}
	if err := os.MkdirAll(filepath.Dir(m.policyPath), 0o755); err != nil {
		return fmt.Errorf("创建适配器策略目录失败: %w", err)
	}
	data, err := toml.Marshal(policyDoc)
	if err != nil {
		return fmt.Errorf("序列化适配器策略失败: %w", err)
	}
	temporaryPath := m.policyPath + ".tmp"
	if err := os.WriteFile(temporaryPath, data, 0o644); err != nil {
		return fmt.Errorf("写入适配器策略临时文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, m.policyPath); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("替换适配器策略文件失败: %w", err)
	}
	return nil
}

// ensureAdapterTables 取出或创建 adapters 数组
func ensureAdapterTables(policyDoc map[string]any) []any {
	if adapters, ok := policyDoc[policyAdaptersKey].([]any); ok {
		return adapters
	}
	adapters := []any{}
	policyDoc[policyAdaptersKey] = adapters
	return adapters
}

// ensureChatPolicyTable 取出或创建某个聊天类型的策略表
func ensureChatPolicyTable(policy map[string]any, chatType ChatType) map[string]any {
	if existing, ok := policy[string(chatType)].(map[string]any); ok {
		return existing
	}
	chatPolicy := map[string]any{}
	policy[string(chatType)] = chatPolicy
	return chatPolicy
}

// setOrRemoveIDList 写入或删除名单字段
func setOrRemoveIDList(policy map[string]any, key string, ids []string) {
	if len(ids) > 0 {
		policy[key] = toAnyList(ids)
		return
	}
	delete(policy, key)
}

// pruneEmptyUIPolicy 删除没有任何规则字段的聊天类型表
func pruneEmptyUIPolicy(adapterPolicy map[string]any, chatType ChatType) {
	chatPolicy, ok := adapterPolicy[string(chatType)].(map[string]any)
	if !ok {
		return
	}
	for _, key := range []string{
		policyAllowIDsKey,
		policyDenyIDsKey,
		policyListTypeKey,
		policyIDsKey,
		policyDisabledKey,
		policyDefaultActionKey,
	} {
		if hasPolicyKey(chatPolicy, key) {
			return
		}
	}
	delete(adapterPolicy, string(chatType))
}

// pruneEmptyAdapterPolicy 删除只剩下身份字段的策略条目
func pruneEmptyAdapterPolicy(adapters []any, target map[string]any) []any {
	for key := range target {
		if !containsString(policyIdentityKeys, key) {
			return adapters
		}
	}
	for index, item := range adapters {
		itemPolicy, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// 策略条目以身份字段唯一标识，按身份比较等价于 Python 的对象身份移除
		if policyIdentityEqual(itemPolicy, target) {
			return append(adapters[:index], adapters[index+1:]...)
		}
	}
	return adapters
}

// policyIdentityEqual 判断两个策略条目的身份字段是否一致
func policyIdentityEqual(left, right map[string]any) bool {
	for _, key := range policyIdentityKeys {
		if policyStringValue(left[key]) != policyStringValue(right[key]) {
			return false
		}
	}
	return true
}

// syncActiveGroup 保持当前分组快照与聊天流覆盖接口写入的实际规则一致
func syncActiveGroup(adapterPolicy map[string]any) error {
	groupsValue, ok := adapterPolicy[policyGroupsKey]
	if !ok || groupsValue == nil {
		return nil
	}
	groups, ok := groupsValue.([]any)
	if !ok {
		return errInvalidPolicyGroups
	}
	activeGroup := strings.TrimSpace(policyStringValue(adapterPolicy[policyActiveGroupKey]))
	var selected map[string]any
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(policyStringValue(group[policyGroupIDKey])) == activeGroup {
			selected = group
			break
		}
	}
	if selected == nil {
		return errPolicyGroupMissing
	}
	for _, chatType := range supportedChatTypes {
		policy, _ := adapterPolicy[string(chatType)].(map[string]any)
		selected[string(chatType)] = map[string]any{
			policyDefaultActionKey: defaultActionOrInherit(policy),
			policyAllowIDsKey:      copyAnyList(policy[policyAllowIDsKey]),
			policyDenyIDsKey:       copyAnyList(policy[policyDenyIDsKey]),
		}
	}
	return nil
}

// defaultActionOrInherit 读取策略表中的默认动作
func defaultActionOrInherit(policy map[string]any) any {
	if policy == nil {
		return defaultActionInherit
	}
	value, ok := policy[policyDefaultActionKey]
	if !ok {
		return defaultActionInherit
	}
	return value
}

// findDisplayAdapterPolicy 为面板读取挑选应展示的规则条目
func findDisplayAdapterPolicy(adapters []any, identity AdapterIdentity) map[string]any {
	if len(adapters) == 0 {
		return nil
	}
	matched := make([]map[string]any, 0, len(adapters))
	for _, rawItem := range adapters {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if adapterPolicyMatches(item, identity) {
			matched = append(matched, item)
		}
	}
	if len(matched) == 0 && isPluginOnlyIdentity(identity) {
		pluginID := strings.TrimSpace(identity.PluginID)
		for _, rawItem := range adapters {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(policyStringValue(item[policyPluginIDKey])) == pluginID {
				matched = append(matched, item)
			}
		}
	}
	if len(matched) == 0 {
		return nil
	}
	best := matched[0]
	bestSpecificity := adapterPolicySpecificity(best)
	for _, item := range matched[1:] {
		if specificity := adapterPolicySpecificity(item); specificity > bestSpecificity {
			best = item
			bestSpecificity = specificity
		}
	}
	return best
}

// isPluginOnlyIdentity 判断身份是否只带了 plugin_id
func isPluginOnlyIdentity(identity AdapterIdentity) bool {
	policyMatch := identityToPolicyMatch(identity)
	return len(policyMatch) == 1 && strings.TrimSpace(identity.PluginID) != ""
}

// findOrCreateAdapterPolicy 按运行时求值同一标准定位规则条目，找不到时按身份新建
func findOrCreateAdapterPolicy(adapters []any, identity AdapterIdentity) (map[string]any, []any) {
	var best map[string]any
	bestSpecificity := -1
	for _, rawItem := range adapters {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if !adapterPolicyMatches(item, identity) {
			continue
		}
		if specificity := adapterPolicySpecificity(item); specificity > bestSpecificity {
			best = item
			bestSpecificity = specificity
		}
	}
	if best != nil {
		return best, adapters
	}
	created := map[string]any{}
	for key, value := range identityToPolicyMatch(identity) {
		created[key] = value
	}
	adapters = append(adapters, created)
	return created, adapters
}

// policyMatchesChatScope 判断适配器策略是否可能作用于指定聊天路由
func policyMatchesChatScope(policy map[string]any, platform, accountID, scope string) bool {
	policyPlatform := strings.TrimSpace(policyStringValue(policy[policyPlatformKey]))
	policyAccountID := strings.TrimSpace(policyStringValue(policy[policyAccountIDKey]))
	policyScope := strings.TrimSpace(policyStringValue(policy[policyScopeKey]))
	if policyPlatform != "" && policyPlatform != platform {
		return false
	}
	if policyAccountID != "" && accountID != "" && policyAccountID != accountID {
		return false
	}
	if policyScope != "" && scope != "" && policyScope != scope {
		return false
	}
	return true
}
