package platformio

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"maibot-go/internal/core/localoperator"
)

const (
	// implicitDefaultAction 未命中任何策略条目时的隐式默认动作
	implicitDefaultAction = defaultActionAllow
)

// supportedChatTypes 策略文件中的聊天类型，按字典序固定遍历顺序
var supportedChatTypes = []ChatType{ChatTypeGroup, ChatTypePrivate}

// internalPlatforms 内置聊天室平台，不属于外部适配器
var internalPlatforms = map[string]struct{}{
	"internal": {},
	"webui":    {},
}

// AdapterIdentity 适配器实例身份，对齐 MaiBot AdapterIdentity
type AdapterIdentity struct {
	AdapterID   string
	PluginID    string
	GatewayName string
	Platform    string
	AccountID   string
	Scope       string
}

// AdapterPolicyResult 适配器名单策略判断结果
type AdapterPolicyResult struct {
	Allowed    bool
	Configured bool
	ChatType   ChatType
	TargetID   string
	ListType   string
	Source     string
	Reason     string
	MatchedIDs []string
}

// ToMap 转换为 API/SDK 友好的字典
func (r AdapterPolicyResult) ToMap() map[string]any {
	matchedIDs := make([]string, len(r.MatchedIDs))
	copy(matchedIDs, r.MatchedIDs)
	return map[string]any{
		"allowed":     r.Allowed,
		"configured":  r.Configured,
		"chat_type":   string(r.ChatType),
		"target_id":   r.TargetID,
		"list_type":   r.ListType,
		"source":      r.Source,
		"reason":      r.Reason,
		"matched_ids": matchedIDs,
	}
}

// AdapterPolicyManager 读取并执行主程序统一适配器黑白名单策略
type AdapterPolicyManager struct {
	policyPath string
}

// NewAdapterPolicyManager 创建策略管理器；policyPath 为空时使用实例默认路径
func NewAdapterPolicyManager(policyPath string) (*AdapterPolicyManager, error) {
	path := strings.TrimSpace(policyPath)
	if path == "" {
		defaultPath, err := DefaultAdapterPolicyPath()
		if err != nil {
			return nil, err
		}
		path = defaultPath
	}
	return &AdapterPolicyManager{policyPath: path}, nil
}

// DefaultAdapterPolicyPath 返回实例目录下的 config/adapter_policy.toml
// 对齐 MaiBot get_instance_root()/config/adapter_policy.toml
func DefaultAdapterPolicyPath() (string, error) {
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		workingDir, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("解析实例根目录失败: %w", err)
		}
		root = workingDir
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("解析实例根目录失败: %w", err)
	}
	return filepath.Join(absoluteRoot, "config", "adapter_policy.toml"), nil
}

// PolicyPath 返回策略文件路径
func (m *AdapterPolicyManager) PolicyPath() string {
	if m == nil {
		return ""
	}
	return m.policyPath
}

// NormalizeChatType 规范化聊天类型，仅支持 group 与 private
func NormalizeChatType(raw string) (ChatType, error) {
	normalized := ChatType(strings.ToLower(strings.TrimSpace(raw)))
	switch normalized {
	case ChatTypeGroup, ChatTypePrivate:
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: %s", errUnsupportedChatType, raw)
	}
}

// IsInternalPlatform 判断平台是否属于内置聊天室或本地控制台
// 这类平台不能被适配器名单禁用
func IsInternalPlatform(platform string) bool {
	normalized := strings.TrimSpace(platform)
	if normalized == "" {
		return false
	}
	if _, ok := internalPlatforms[normalized]; ok {
		return true
	}
	_, ok := localoperator.LocalPlatformBotIDs[normalized]
	return ok
}

// Evaluate 判断某个聊天目标是否允许通过指定适配器
func (m *AdapterPolicyManager) Evaluate(identity AdapterIdentity, chatType string, targetID string) (AdapterPolicyResult, error) {
	normalizedChatType, err := NormalizeChatType(chatType)
	if err != nil {
		return AdapterPolicyResult{}, err
	}
	normalizedTargetID := strings.TrimSpace(targetID)
	if normalizedTargetID == "" {
		return AdapterPolicyResult{
			Allowed:    false,
			Configured: true,
			ChatType:   normalizedChatType,
			TargetID:   normalizedTargetID,
			Reason:     "missing_target_id",
		}, nil
	}

	// 内置聊天室与本地控制台不属于外部适配器，不能被适配器名单禁用
	if IsInternalPlatform(identity.Platform) {
		return AdapterPolicyResult{
			Allowed:    true,
			Configured: false,
			ChatType:   normalizedChatType,
			TargetID:   normalizedTargetID,
			Source:     "internal_platform",
			Reason:     "internal_platform_exempt",
		}, nil
	}

	policyData, err := m.loadPolicyData()
	if err != nil {
		return AdapterPolicyResult{}, err
	}
	for _, candidate := range m.resolvedPolicies(policyData, identity, normalizedChatType) {
		policyResult, matched := evaluatePolicy(candidate.policy, candidate.source, normalizedChatType, normalizedTargetID)
		if matched {
			return policyResult, nil
		}
	}

	return AdapterPolicyResult{
		Allowed:    implicitDefaultAction == defaultActionAllow,
		Configured: false,
		ChatType:   normalizedChatType,
		TargetID:   normalizedTargetID,
		Source:     "implicit_default",
		Reason:     "default_" + implicitDefaultAction,
	}, nil
}

// GetDefaultActions 返回群聊和私聊当前生效的全局默认动作
func (m *AdapterPolicyManager) GetDefaultActions() (map[ChatType]string, error) {
	policyData, err := m.loadPolicyData()
	if err != nil {
		return nil, err
	}
	defaults, _ := policyData[policyDefaultsKey].(map[string]any)
	actions := make(map[ChatType]string, len(supportedChatTypes))
	for _, chatType := range supportedChatTypes {
		action := implicitDefaultAction
		if defaults != nil {
			if typedPolicy := resolveTypedPolicy(defaults, chatType); typedPolicy != nil {
				configuredAction := strings.ToLower(policyStringValue(typedPolicy[policyDefaultActionKey]))
				if isSupportedDefaultAction(configuredAction) {
					action = configuredAction
				}
			}
		}
		actions[chatType] = action
	}
	return actions, nil
}

// policyCandidate 一条待求值的策略及其来源
type policyCandidate struct {
	policy map[string]any
	source string
}

// resolvedPolicies 按适配器实例到默认模板的顺序解析候选策略
func (m *AdapterPolicyManager) resolvedPolicies(policyData map[string]any, identity AdapterIdentity, chatType ChatType) []policyCandidate {
	candidates := adapterPolicyCandidates(policyData, identity, chatType)
	defaults, _ := policyData[policyDefaultsKey].(map[string]any)
	if defaults != nil {
		if defaultPolicy := resolveTypedPolicy(defaults, chatType); defaultPolicy != nil {
			candidates = append(candidates, policyCandidate{policy: defaultPolicy, source: "defaults"})
		}
	}
	return candidates
}

// adapterPolicyCandidates 按 specificity 从高到低取命中的适配器条目
func adapterPolicyCandidates(policyData map[string]any, identity AdapterIdentity, chatType ChatType) []policyCandidate {
	adapters, ok := policyData[policyAdaptersKey].([]any)
	if !ok {
		return nil
	}

	type scoredPolicy struct {
		specificity int
		policy      map[string]any
	}
	matched := make([]scoredPolicy, 0, len(adapters))
	for _, rawItem := range adapters {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		if !adapterPolicyMatches(item, identity) {
			continue
		}
		if policyBoolValue(item[policyDisabledKey]) {
			matched = append(matched, scoredPolicy{
				specificity: adapterPolicySpecificity(item),
				policy:      map[string]any{policyDisabledKey: true},
			})
			continue
		}
		if typedPolicy := resolveTypedPolicy(item, chatType); typedPolicy != nil {
			matched = append(matched, scoredPolicy{
				specificity: adapterPolicySpecificity(item),
				policy:      typedPolicy,
			})
		}
	}
	// 同等 specificity 保持策略文件中的原始顺序
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].specificity > matched[j].specificity
	})

	candidates := make([]policyCandidate, 0, len(matched))
	for _, item := range matched {
		candidates = append(candidates, policyCandidate{policy: item.policy, source: "adapter"})
	}
	return candidates
}

// evaluatePolicy 求值单条策略；返回 false 表示该条策略对本聊天类型不生效
func evaluatePolicy(policy map[string]any, source string, chatType ChatType, targetID string) (AdapterPolicyResult, bool) {
	if policyBoolValue(policy[policyDisabledKey]) {
		return AdapterPolicyResult{
			Allowed:    false,
			Configured: true,
			ChatType:   chatType,
			TargetID:   targetID,
			Source:     source,
			Reason:     reasonAdapterDisabled,
		}, true
	}

	denyIDs := normalizeIDList(policy[policyDenyIDsKey])
	allowIDs := normalizeIDList(policy[policyAllowIDsKey])
	if wildcard := slices.Contains(denyIDs, "*"); slices.Contains(denyIDs, targetID) || wildcard {
		return AdapterPolicyResult{
			Allowed:    false,
			Configured: true,
			ChatType:   chatType,
			TargetID:   targetID,
			ListType:   listTypeOverride,
			Source:     source,
			Reason:     reasonMatchedDenyOverride,
			MatchedIDs: matchedIDs(denyIDs, targetID),
		}, true
	}
	if wildcard := slices.Contains(allowIDs, "*"); slices.Contains(allowIDs, targetID) || wildcard {
		return AdapterPolicyResult{
			Allowed:    true,
			Configured: true,
			ChatType:   chatType,
			TargetID:   targetID,
			ListType:   listTypeOverride,
			Source:     source,
			Reason:     reasonMatchedAllowOverride,
			MatchedIDs: matchedIDs(allowIDs, targetID),
		}, true
	}

	if !hasPolicyKey(policy, policyListTypeKey) && !hasPolicyKey(policy, policyIDsKey) {
		defaultAction := strings.ToLower(policyStringValue(policy[policyDefaultActionKey]))
		if !isSupportedDefaultAction(defaultAction) {
			return AdapterPolicyResult{}, false
		}
		return AdapterPolicyResult{
			Allowed:    defaultAction == defaultActionAllow,
			Configured: true,
			ChatType:   chatType,
			TargetID:   targetID,
			ListType:   listTypeDefault,
			Source:     source,
			Reason:     "default_" + defaultAction,
		}, true
	}

	listType := strings.ToLower(policyStringValue(policy[policyListTypeKey]))
	if listType == "" {
		listType = listTypeWhitelist
	}
	if listType != listTypeWhitelist && listType != listTypeBlacklist {
		listType = listTypeWhitelist
	}

	ids := normalizeIDList(policy[policyIDsKey])
	wildcardMatched := slices.Contains(ids, "*")
	targetMatched := wildcardMatched || slices.Contains(ids, targetID)

	var allowed bool
	var reason string
	if listType == listTypeWhitelist {
		allowed = targetMatched
		reason = reasonNotInWhitelist
		if allowed {
			reason = reasonMatchedWhitelist
		}
	} else {
		allowed = !targetMatched
		reason = reasonNotInBlacklist
		if !allowed {
			reason = reasonMatchedBlacklist
		}
	}

	return AdapterPolicyResult{
		Allowed:    allowed,
		Configured: true,
		ChatType:   chatType,
		TargetID:   targetID,
		ListType:   listType,
		Source:     source,
		Reason:     reason,
		MatchedIDs: matchedIDs(ids, targetID),
	}, true
}

// matchedIDs 返回命中的名单项，通配符优先
func matchedIDs(ids []string, targetID string) []string {
	if slices.Contains(ids, "*") {
		return []string{"*"}
	}
	if slices.Contains(ids, targetID) {
		return []string{targetID}
	}
	return []string{}
}

// adapterPolicyMatches 判断策略条目是否可能命中指定身份
func adapterPolicyMatches(policy map[string]any, identity AdapterIdentity) bool {
	adapterID := strings.TrimSpace(policyStringValue(policy[policyAdapterIDKey]))
	pluginID := strings.TrimSpace(policyStringValue(policy[policyPluginIDKey]))
	gatewayName := strings.TrimSpace(policyStringValue(policy[policyGatewayNameKey]))
	platform := strings.TrimSpace(policyStringValue(policy[policyPlatformKey]))
	accountID := strings.TrimSpace(policyStringValue(policy[policyAccountIDKey]))
	scope := strings.TrimSpace(policyStringValue(policy[policyScopeKey]))

	if adapterID != "" && adapterID != identity.AdapterID {
		return false
	}
	if pluginID != "" && pluginID != identity.PluginID {
		return false
	}
	if gatewayName != "" && gatewayName != identity.GatewayName {
		return false
	}
	if platform != "" && platform != identity.Platform {
		return false
	}
	if accountID != "" && accountID != identity.AccountID {
		return false
	}
	if scope != "" && scope != identity.Scope {
		return false
	}
	return adapterID != "" || pluginID != "" || gatewayName != "" || platform != "" || accountID != "" || scope != ""
}

// adapterPolicySpecificity 计算策略条目的身份字段命中数量
func adapterPolicySpecificity(policy map[string]any) int {
	score := 0
	for _, key := range policyIdentityKeys {
		if strings.TrimSpace(policyStringValue(policy[key])) != "" {
			score++
		}
	}
	return score
}

// identityToPolicyMatch 将身份转换为可写入策略文件的身份字段
func identityToPolicyMatch(identity AdapterIdentity) map[string]string {
	candidates := map[string]string{
		policyAdapterIDKey:   identity.AdapterID,
		policyPluginIDKey:    identity.PluginID,
		policyGatewayNameKey: identity.GatewayName,
		policyPlatformKey:    identity.Platform,
		policyAccountIDKey:   identity.AccountID,
		policyScopeKey:       identity.Scope,
	}
	policyMatch := make(map[string]string, len(candidates))
	for key, value := range candidates {
		normalized := strings.TrimSpace(value)
		if normalized != "" {
			policyMatch[key] = normalized
		}
	}
	return policyMatch
}

// resolveTypedPolicy 取出某聊天类型的策略表副本
func resolveTypedPolicy(rawPolicy map[string]any, chatType ChatType) map[string]any {
	chatPolicy, ok := rawPolicy[string(chatType)].(map[string]any)
	if !ok {
		return nil
	}
	typedPolicy := make(map[string]any, len(chatPolicy))
	for key, value := range chatPolicy {
		typedPolicy[key] = value
	}
	return typedPolicy
}

var (
	adapterPolicyOnce    sync.Once
	adapterPolicyManager *AdapterPolicyManager
	adapterPolicyErr     error
)

// GetAdapterPolicyManager 返回全局适配器策略管理器
func GetAdapterPolicyManager() (*AdapterPolicyManager, error) {
	adapterPolicyOnce.Do(func() {
		adapterPolicyManager, adapterPolicyErr = NewAdapterPolicyManager("")
	})
	return adapterPolicyManager, adapterPolicyErr
}
