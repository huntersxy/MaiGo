package focus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/maisaka"
)

// ToolRoute 同名工具在各会话上的来源与参数变体
type ToolRoute struct {
	// Spec 合并后的路由声明，模型看到的名字与描述
	Spec maisaka.ToolSpec
	// Variants 每个会话上的原始声明
	Variants map[string]maisaka.ToolSpec
	// Identity 来源身份，用于识别同名不同源
	Identity string
}

// FocusToolRouter 合并各会话工具，保留权限差异与同名工具的来源身份
type FocusToolRouter struct {
	group  *GroupRuntime
	routes map[string]*ToolRoute
}

// NewFocusToolRouter 构造组级工具路由
func NewFocusToolRouter(group *GroupRuntime) *FocusToolRouter {
	return &FocusToolRouter{group: group, routes: map[string]*ToolRoute{}}
}

// Routes 当前路由表，监控与测试用
func (r *FocusToolRouter) Routes() map[string]*ToolRoute {
	if r == nil {
		return nil
	}
	return r.routes
}

// toolIdentity 工具来源身份，避免同名工具被静默覆盖
func toolIdentity(spec maisaka.ToolSpec) string {
	original := spec.Name
	pluginID := ""
	serverName := ""
	if spec.Metadata != nil {
		if value, ok := spec.Metadata["registry_original_name"].(string); ok && value != "" {
			original = value
		}
		if value, ok := spec.Metadata["plugin_id"].(string); ok {
			pluginID = value
		}
		if value, ok := spec.Metadata["server_name"].(string); ok {
			serverName = value
		}
	}
	parts := []string{spec.Provider, original, pluginID, serverName}
	return strings.Join(parts, "\x00")
}

// Refresh 合并各会话的工具
func (r *FocusToolRouter) Refresh(ctx context.Context) ([]maisaka.ToolSpec, error) {
	if r == nil || r.group == nil {
		return nil, nil
	}
	identities := map[string]string{}
	routes := map[string]*ToolRoute{}
	members := r.group.ValidMembers()
	for _, sessionID := range sortedMemberIDs(members) {
		member := members[sessionID]
		specs, err := member.ListTools(ctx)
		if err != nil {
			return nil, err
		}
		for _, original := range specs {
			if original.Provider == maisaka.BuiltinProviderName &&
				(!maisaka.IsBuiltinToolInActionStage(original, false) || maisaka.GetBuiltinToolVisibility(original) == maisaka.BuiltinHidden) {
				continue
			}
			identity := toolIdentity(original)
			name, ok := identities[identity]
			if !ok {
				name = original.Name
				if _, exists := routes[name]; exists {
					sum := sha256.Sum256([]byte(identity))
					suffix := hex.EncodeToString(sum[:])[:12]
					if len(name) > 51 {
						name = name[:51]
					}
					name = name + "_" + suffix
				}
				identities[identity] = name
				spec := original
				spec.Name = name
				routes[name] = &ToolRoute{Spec: spec, Variants: map[string]maisaka.ToolSpec{}, Identity: identity}
			}
			routes[name].Variants[sessionID] = original
		}
	}
	r.routes = routes
	for _, route := range routes {
		r.mergeVariants(route)
	}
	for sessionID, member := range members {
		deferred := make([]maisaka.ToolSpec, 0)
		for _, route := range routes {
			if _, ok := route.Variants[sessionID]; !ok {
				continue
			}
			if !r.IsDefaultVisible(route.Spec) {
				deferred = append(deferred, route.Spec)
			}
		}
		member.UpdateDeferredToolSpecs(deferred)
	}
	specs := make([]maisaka.ToolSpec, 0, len(routes))
	for _, route := range routes {
		specs = append(specs, route.Spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

// mergeVariants 生成模型看到的参数并集，执行时仍按目标会话严格校验
func (r *FocusToolRouter) mergeVariants(route *ToolRoute) {
	sessionIDs := make([]string, 0, len(route.Variants))
	for sessionID := range route.Variants {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	schemas := make([]map[string]any, 0, len(sessionIDs))
	identical := true
	var first map[string]any
	for index, sessionID := range sessionIDs {
		schema := routedSchema(route.Variants[sessionID], sessionID)
		schemas = append(schemas, schema)
		if index == 0 {
			first = schema
			continue
		}
		if !schemasEqual(first, schema) {
			identical = false
		}
	}
	if identical && len(schemas) == 1 {
		route.Spec.Parameters = schemas[0]
		setSchemaSessionEnum(route.Spec.Parameters, sessionIDs)
		return
	}
	if identical {
		route.Spec.Parameters = first
		setSchemaSessionEnum(route.Spec.Parameters, sessionIDs)
		return
	}
	properties := map[string][]map[string]any{}
	definitions := map[string]any{}
	var requiredUnion []string
	for index, schema := range schemas {
		name := fmt.Sprintf("session_variant_%d", index)
		scoped := prefixLocalRefs(schema, "#/$defs/"+name)
		definitions[name] = scoped
		scopedMap, _ := scoped.(map[string]any)
		scopedProps, _ := scopedMap["properties"].(map[string]any)
		for key, value := range scopedProps {
			entry, _ := value.(map[string]any)
			if entry == nil {
				continue
			}
			exists := false
			for _, existing := range properties[key] {
				if schemasEqual(existing, entry) {
					exists = true
					break
				}
			}
			if !exists {
				properties[key] = append(properties[key], entry)
			}
		}
		if index == 0 {
			if required, ok := schema["required"].([]any); ok {
				for _, item := range required {
					if text, ok := item.(string); ok {
						requiredUnion = append(requiredUnion, text)
					}
				}
			}
		}
	}
	merged := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"$defs":      definitions,
	}
	mergedProps := merged["properties"].(map[string]any)
	for key, values := range properties {
		if len(values) == 1 {
			mergedProps[key] = values[0]
			continue
		}
		anyOf := make([]any, 0, len(values))
		for _, value := range values {
			anyOf = append(anyOf, value)
		}
		mergedProps[key] = map[string]any{"anyOf": anyOf}
	}
	required := make([]any, 0, len(requiredUnion))
	for _, name := range requiredUnion {
		presentEverywhere := true
		for _, schema := range schemas {
			if !schemaRequires(schema, name) {
				presentEverywhere = false
				break
			}
		}
		if presentEverywhere {
			required = append(required, name)
		}
	}
	merged["required"] = required
	mergedProps["session_id"] = map[string]any{
		"type":        "string",
		"enum":        stringSliceToAny(sessionIDs),
		"description": "目标聊天的真实 session_id；必须从共享组会话列表或消息来源中选择。",
	}
	route.Spec.Parameters = merged
}

// IsDefaultVisible 工具是否默认暴露给模型
func (r *FocusToolRouter) IsDefaultVisible(spec maisaka.ToolSpec) bool {
	if spec.Provider == maisaka.BuiltinProviderName {
		return maisaka.IsBuiltinToolInActionStage(spec, false) && maisaka.GetBuiltinToolVisibility(spec) == maisaka.BuiltinVisible
	}
	if spec.Metadata != nil {
		if visibility, ok := spec.Metadata["visibility"].(string); ok {
			return visibility == "visible"
		}
	}
	return true
}

// Definitions 模型可见的工具定义
func (r *FocusToolRouter) Definitions() []map[string]any {
	if r == nil || r.group == nil {
		return nil
	}
	names := make([]string, 0, len(r.routes))
	for name := range r.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	definitions := make([]map[string]any, 0, len(names))
	for _, name := range names {
		route := r.routes[name]
		visible := r.IsDefaultVisible(route.Spec)
		if !visible {
			for sessionID := range route.Variants {
				member, ok := r.group.Members()[sessionID]
				if !ok {
					continue
				}
				if _, discovered := member.DiscoveredToolNames()[name]; discovered {
					visible = true
					break
				}
			}
		}
		if visible {
			definitions = append(definitions, route.Spec.ToLLMDefinition())
		}
	}
	return definitions
}

// DeferredNotice 未默认暴露工具的可发现提示
func (r *FocusToolRouter) DeferredNotice() string {
	if r == nil || r.group == nil {
		return ""
	}
	sessionIDs := map[string]struct{}{}
	for sessionID, member := range r.group.Members() {
		if r.group.IsMemberValid(member) {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	names := make([]string, 0, len(r.routes))
	for name := range r.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		route := r.routes[name]
		if r.IsDefaultVisible(route.Spec) {
			continue
		}
		line := "- " + name + ": " + route.Spec.Description
		if !sameSessionSet(route.Variants, sessionIDs) {
			line += " (session_ids=" + strings.Join(sortedKeys(route.Variants), ",") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<deferred_tools>\n" + strings.Join(lines, "\n") + "\n</deferred_tools>"
}

// Invoke 按 session_id 路由到目标会话的原始工具
func (r *FocusToolRouter) Invoke(
	ctx context.Context,
	toolName string,
	arguments map[string]any,
	callID string,
	reasoning string,
) (maisaka.ToolInvocation, maisaka.ToolExecutionResult, Member, *maisaka.ToolSpec) {
	sessionID, _ := arguments["session_id"].(string)
	invocation := maisaka.ToolInvocation{ToolName: toolName, Arguments: cloneMap(arguments), CallID: callID, LatestThought: reasoning}
	failure := func(reason string) (maisaka.ToolInvocation, maisaka.ToolExecutionResult, Member, *maisaka.ToolSpec) {
		return invocation, maisaka.ToolExecutionResult{Success: false, Content: reason, Message: reason}, nil, nil
	}
	if strings.TrimSpace(sessionID) == "" {
		return failure("每个 Focus 工具调用必须填写非空 session_id。")
	}
	member, ok := r.group.Members()[sessionID]
	if !ok || !r.group.IsMemberValid(member) {
		return failure(fmt.Sprintf("session_id=%s 不属于当前有效共享组，不能执行工具。", sessionID))
	}
	route, ok := r.routes[toolName]
	if !ok {
		return failure(fmt.Sprintf("工具 %s 在目标聊天不可用，请先使用 tool_search 查询。", toolName))
	}
	original, ok := route.Variants[sessionID]
	if !ok {
		return failure(fmt.Sprintf("工具 %s 在目标聊天不可用，请先使用 tool_search 查询。", toolName))
	}
	if err := validateToolArguments(arguments, routedSchema(original, sessionID)); err != nil {
		return failure("工具参数不符合目标聊天的声明：" + validationMessage(err))
	}
	current, found, err := member.GetToolSpec(ctx, original.Name)
	if err != nil || !found {
		return failure("工具来源或目标权限已经变化，请重新查询工具。")
	}
	if toolIdentity(*current) != toolIdentity(original) {
		return failure("工具来源或目标权限已经变化，请重新查询工具。")
	}
	nativeArguments := map[string]any{}
	if nativeHasSessionID(original.Parameters) {
		nested, ok := arguments["tool_arguments"].(map[string]any)
		if !ok {
			return failure("工具参数不符合目标聊天的声明：缺少 tool_arguments。")
		}
		nativeArguments = cloneMap(nested)
	} else {
		for key, value := range arguments {
			if key == "session_id" {
				continue
			}
			nativeArguments[key] = value
		}
	}
	nativeArguments = member.ExpandMessageIDAliases(nativeArguments)
	if err := validateToolArguments(nativeArguments, current.Parameters); err != nil {
		return failure("工具参数声明已经变化：" + validationMessage(err))
	}
	invocation.Arguments = nativeArguments
	if invocation.Metadata == nil {
		invocation.Metadata = map[string]any{}
	}
	invocation.Metadata["focus_scope_id"] = r.group.ScopeID()
	invocation.Metadata["session_id"] = sessionID
	control := (toolName == "wait" || toolName == "stop") && original.Provider == maisaka.BuiltinProviderName
	var result maisaka.ToolExecutionResult
	if control {
		result = r.group.ExecuteControl(toolName, nativeArguments, sessionID)
	} else {
		native := maisaka.ToolInvocation{
			ToolName:      original.Name,
			Arguments:     nativeArguments,
			CallID:        callID,
			LatestThought: reasoning,
		}
		result = member.InvokeTool(ctx, native)
		result.ToolName = toolName
	}
	if result.Metadata == nil {
		result.Metadata = map[string]any{}
	}
	result.Metadata["session_id"] = sessionID
	return invocation, result, member, &original
}

// routedSchema 目标会话的声明：补 session_id 路由参数
func routedSchema(spec maisaka.ToolSpec, sessionID string) map[string]any {
	native := deepCopyMap(spec.Parameters)
	if native == nil {
		native = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	properties, _ := native["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	var schema map[string]any
	if _, hasSessionID := properties["session_id"]; hasSessionID {
		schema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_arguments": prefixLocalRefs(native, "#/properties/tool_arguments"),
			},
			"required":             []any{"tool_arguments"},
			"additionalProperties": false,
		}
	} else {
		schema = native
	}
	schemaProps, _ := schema["properties"].(map[string]any)
	if schemaProps == nil {
		schemaProps = map[string]any{}
		schema["properties"] = schemaProps
	}
	schemaProps["session_id"] = map[string]any{
		"type":        "string",
		"enum":        []any{sessionID},
		"description": "目标聊天的真实 session_id；必须从共享组会话列表或消息来源中选择。",
	}
	required := []any{}
	if existing, ok := schema["required"].([]any); ok {
		required = append(required, existing...)
	}
	required = append(required, "session_id")
	schema["required"] = dedupeAny(required)
	return schema
}

// prefixLocalRefs 包装参数或合并定义时同步移动本地引用
func prefixLocalRefs(value any, prefix string) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, prefixLocalRefs(item, prefix))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "$ref" {
				if text, ok := item.(string); ok && strings.HasPrefix(text, "#/") {
					out[key] = prefix + text[1:]
					continue
				}
			}
			out[key] = prefixLocalRefs(item, prefix)
		}
		return out
	default:
		return value
	}
}

// nativeHasSessionID 原始声明是否已有 session_id 业务参数
func nativeHasSessionID(parameters map[string]any) bool {
	properties, _ := parameters["properties"].(map[string]any)
	if properties == nil {
		return false
	}
	_, ok := properties["session_id"]
	return ok
}

func setSchemaSessionEnum(schema map[string]any, sessionIDs []string) {
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
		schema["properties"] = properties
	}
	properties["session_id"] = map[string]any{
		"type":        "string",
		"enum":        stringSliceToAny(sessionIDs),
		"description": "目标聊天的真实 session_id；必须从共享组会话列表或消息来源中选择。",
	}
	required := []any{}
	if existing, ok := schema["required"].([]any); ok {
		required = append(required, existing...)
	}
	required = append(required, "session_id")
	schema["required"] = dedupeAny(required)
}

func schemaRequires(schema map[string]any, name string) bool {
	required, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, item := range required {
		if text, ok := item.(string); ok && text == name {
			return true
		}
	}
	return false
}

func dedupeAny(values []any) []any {
	seen := map[string]struct{}{}
	out := make([]any, 0, len(values))
	for _, value := range values {
		key, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if _, ok := seen[string(key)]; ok {
			continue
		}
		seen[string(key)] = struct{}{}
		out = append(out, value)
	}
	return out
}

func schemasEqual(left, right map[string]any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	if errA != nil || errB != nil {
		return false
	}
	return string(a) == string(b)
}

func deepCopyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil
	}
	return out
}

func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func stringSliceToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func sortedKeys(values map[string]maisaka.ToolSpec) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sameSessionSet(variants map[string]maisaka.ToolSpec, sessionIDs map[string]struct{}) bool {
	if len(variants) != len(sessionIDs) {
		return false
	}
	for key := range variants {
		if _, ok := sessionIDs[key]; !ok {
			return false
		}
	}
	return true
}

func sortedMemberIDs(members map[string]Member) []string {
	out := make([]string, 0, len(members))
	for sessionID := range members {
		out = append(out, sessionID)
	}
	sort.Strings(out)
	return out
}
