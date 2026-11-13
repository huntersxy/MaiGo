package payloadcontent

// Context Item 运行时层
// 对等 MaiBot src/llm_models/payload_content/context_item.py：
//   - 统一表示 system/user/reasoning/assistant/function call/function output/provider activity/opaque
//   - 工具调用关系由 logical_turn_id 绑定，列表位置是唯一顺序
//   - provider replay fragment 保存不可变原生 payload，快照与持久化时剥离
//
// Go 侧当前仍以 eino AgenticMessage 作为 Provider 请求的双向载体，ContextItem 层负责
// 插件 context_items 协议、失败快照投影与响应 Item 还原；chat/gemini 直接消费 eino 消息

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// ContextItemSchemaVersion 对齐 MaiBot CONTEXT_ITEM_SCHEMA_VERSION
	ContextItemSchemaVersion = 1
	// ProviderReplaySchemaVersion 对齐 MaiBot PROVIDER_REPLAY_SCHEMA_VERSION
	ProviderReplaySchemaVersion = 1
)

// SupportedContextImageFormats 上下文图片允许的格式集合
//
// 父包的 context_projection.go 需要同一份白名单；两处不一致会让
// 「校验通过但投影拒绝」或反之，因此导出为唯一来源。
var SupportedContextImageFormats = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "webp": true, "gif": true,
}

// ContextItemType Context Item 类型名，与 Python 类名一致
type ContextItemType string

const (
	ContextItemSystemMessage      ContextItemType = "SystemMessageItem"
	ContextItemUserMessage        ContextItemType = "UserMessageItem"
	ContextItemReasoning          ContextItemType = "ReasoningItem"
	ContextItemAssistantMessage   ContextItemType = "AssistantMessageItem"
	ContextItemFunctionCall       ContextItemType = "FunctionCallItem"
	ContextItemFunctionCallOutput ContextItemType = "FunctionCallOutputItem"
	ContextItemProviderActivity   ContextItemType = "ProviderActivityItem"
	ContextItemProviderOpaque     ContextItemType = "ProviderOpaqueItem"
)

// ContextRoleType 可直接投影为消息的上下文角色
type ContextRoleType string

const (
	ContextRoleSystem    ContextRoleType = "system"
	ContextRoleUser      ContextRoleType = "user"
	ContextRoleAssistant ContextRoleType = "assistant"
	ContextRoleTool      ContextRoleType = "tool"
)

// ContextContentPartType 内容片段类型
type ContextContentPartType string

const (
	ContextPartText    ContextContentPartType = "text"
	ContextPartImage   ContextContentPartType = "image"
	ContextPartRefusal ContextContentPartType = "refusal"
)

// ContextMedia 非文本原生媒体（音频/视频/文件）的 Provider 侧载荷。
// Type 仍保持 text 占位，使 chat 等不支持媒体的 Provider 继续使用占位文本；
// Gemini 投影看到 Media 后改为 inlineData/fileData。
type ContextMedia struct {
	Kind       string
	MIMEType   string
	Base64Data string
	URL        string
	FileID     string
	Name       string
}

// ContextContentPart 规范化内容片段；三种类型共用结构，按 Type 取字段
type ContextContentPart struct {
	Type        ContextContentPartType
	Text        string
	ImageFormat string
	ImageBase64 string
	Refusal     string
	Media       *ContextMedia
}

// Validate 校验片段，对齐 Python ContextTextPart/ContextImagePart/ContextRefusalPart
func (p ContextContentPart) Validate() error {
	switch p.Type {
	case ContextPartText:
		if p.Text == "" {
			return fmt.Errorf("文本内容片段不能为空字符串")
		}
	case ContextPartImage:
		format := strings.ToLower(strings.TrimSpace(p.ImageFormat))
		if !SupportedContextImageFormats[format] {
			return fmt.Errorf("不受支持的图片格式")
		}
		if p.ImageBase64 == "" {
			return fmt.Errorf("图片的 base64 编码不能为空")
		}
	case ContextPartRefusal:
		if p.Refusal == "" {
			return fmt.Errorf("拒答内容不能为空")
		}
	default:
		return fmt.Errorf("不支持的内容片段类型: %s", p.Type)
	}
	return nil
}

// NormalizedImageFormat 返回 Provider 通常接受的规范化图片格式
func (p ContextContentPart) NormalizedImageFormat() string {
	format := strings.ToLower(strings.TrimSpace(p.ImageFormat))
	if format == "jpg" || format == "jpeg" {
		return "jpeg"
	}
	return format
}

// ContextItemMeta 单个 Item 的稳定身份和关系元数据
type ContextItemMeta struct {
	ItemID        string
	LogicalTurnID string
	Timestamp     time.Time
}

// NewContextItemMeta 创建带随机 Item ID 的元数据
func NewContextItemMeta(logicalTurnID string) ContextItemMeta {
	return ContextItemMeta{
		ItemID:        strings.ReplaceAll(uuid.NewString(), "-", ""),
		LogicalTurnID: strings.TrimSpace(logicalTurnID),
		Timestamp:     time.Now(),
	}
}

// Validate 校验 Item ID
func (m ContextItemMeta) Validate() error {
	if strings.TrimSpace(m.ItemID) == "" {
		return fmt.Errorf("Context Item ID 不能为空")
	}
	return nil
}

// ContextToolCall 不可变工具调用；参数保存为普通 map
type ContextToolCall struct {
	CallID       string
	FuncName     string
	Args         map[string]any
	ExtraContent map[string]any
}

// NewContextToolCall 创建工具调用，缺省参数为空对象
func NewContextToolCall(callID, funcName string, args map[string]any) ContextToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return ContextToolCall{CallID: callID, FuncName: funcName, Args: args}
}

// Validate 校验工具调用
func (c ContextToolCall) Validate() error {
	if strings.TrimSpace(c.CallID) == "" {
		return fmt.Errorf("工具调用 ID 不能为空")
	}
	if strings.TrimSpace(c.FuncName) == "" {
		return fmt.Errorf("工具函数名称不能为空")
	}
	return nil
}

// ArgsJSON 返回规范 JSON bytes
func (c ContextToolCall) ArgsJSON() []byte {
	encoded, err := canonicalContextJSON(c.Args)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// ContextProviderScope Provider 原生 Item 可以安全回放的严格范围
type ContextProviderScope struct {
	SchemaVersion       int
	ClientType          string
	ProviderName        string
	EndpointFingerprint string
	ModelIdentifier     string
}

// Validate 校验 scope
func (s ContextProviderScope) Validate() error {
	if s.SchemaVersion <= 0 {
		return fmt.Errorf("Provider scope schema_version 必须为正整数")
	}
	if strings.TrimSpace(s.ClientType) == "" || strings.TrimSpace(s.ProviderName) == "" ||
		strings.TrimSpace(s.EndpointFingerprint) == "" || strings.TrimSpace(s.ModelIdentifier) == "" {
		return fmt.Errorf("Provider scope 字段不能为空")
	}
	return nil
}

// ContextProviderReplayFragment 附着到单个 Item 的深层不可变 Provider wire payload
type ContextProviderReplayFragment struct {
	Scope         ContextProviderScope
	PayloadJSON   []byte
	PayloadSHA256 string
}

// NewContextProviderReplayFragment 从普通 payload 创建不可变回放片段
func NewContextProviderReplayFragment(scope ContextProviderScope, payload map[string]any) (ContextProviderReplayFragment, error) {
	encoded, err := canonicalContextJSON(payload)
	if err != nil {
		return ContextProviderReplayFragment{}, err
	}
	if err := scope.Validate(); err != nil {
		return ContextProviderReplayFragment{}, err
	}
	sum := sha256.Sum256(encoded)
	return ContextProviderReplayFragment{Scope: scope, PayloadJSON: encoded, PayloadSHA256: hex.EncodeToString(sum[:])}, nil
}

// Materialize 返回与内部 bytes 隔离的新 payload
func (f ContextProviderReplayFragment) Materialize() (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(f.PayloadJSON, &payload); err != nil {
		return nil, fmt.Errorf("Provider replay payload 必须是 JSON object: %w", err)
	}
	return payload, nil
}

// ByteSize 返回回放片段的实际字节数
func (f ContextProviderReplayFragment) ByteSize() int { return len(f.PayloadJSON) }

// ContextItem 统一上下文 Item
type ContextItem struct {
	Type ContextItemType
	Meta ContextItemMeta

	Parts []ContextContentPart
	Phase string

	SummaryParts   []string
	TextParts      []string
	Representation string

	ToolCall *ContextToolCall

	CallID   string
	Output   string
	ToolName string
	Success  bool

	ProviderType   string
	Status         string
	DisplaySummary string
	ActionType     string
	Details        []string
	SourceCount    int

	Replay *ContextProviderReplayFragment

	// ProviderData 保存 Provider 原生回放所需的短生命周期附加信息（如 Gemini thought_signature）；
	// 不进入 Python 兼容快照，只用于同一次进程内的请求重建
	ProviderData map[string]any
}

// NewSystemMessageItem 创建系统消息 Item
func NewSystemMessageItem(parts ...ContextContentPart) ContextItem {
	return ContextItem{Type: ContextItemSystemMessage, Meta: NewContextItemMeta(""), Parts: parts}
}

// NewUserMessageItem 创建用户消息 Item
func NewUserMessageItem(parts ...ContextContentPart) ContextItem {
	return ContextItem{Type: ContextItemUserMessage, Meta: NewContextItemMeta(""), Parts: parts}
}

// NewAssistantMessageItem 创建助手正文 Item
func NewAssistantMessageItem(logicalTurnID string, parts ...ContextContentPart) ContextItem {
	return ContextItem{Type: ContextItemAssistantMessage, Meta: NewContextItemMeta(logicalTurnID), Parts: parts}
}

// NewReasoningItem 创建 reasoning Item
func NewReasoningItem(logicalTurnID, text string) ContextItem {
	item := ContextItem{Type: ContextItemReasoning, Meta: NewContextItemMeta(logicalTurnID), Representation: "raw_text"}
	if strings.TrimSpace(text) != "" {
		item.TextParts = []string{text}
	}
	return item
}

// NewFunctionCallItem 创建函数调用 Item
func NewFunctionCallItem(logicalTurnID string, call ContextToolCall) ContextItem {
	return ContextItem{Type: ContextItemFunctionCall, Meta: NewContextItemMeta(logicalTurnID), ToolCall: &call}
}

// NewFunctionCallOutputItem 创建工具结果 Item
func NewFunctionCallOutputItem(logicalTurnID, callID, output, toolName string) ContextItem {
	return ContextItem{
		Type: ContextItemFunctionCallOutput, Meta: NewContextItemMeta(logicalTurnID),
		CallID: callID, Output: output, ToolName: toolName, Success: true,
	}
}

// NewProviderActivityItem 创建 Provider 活动 Item
func NewProviderActivityItem(providerType, displaySummary string) ContextItem {
	return ContextItem{
		Type: ContextItemProviderActivity, Meta: NewContextItemMeta(""),
		ProviderType: providerType, DisplaySummary: displaySummary,
	}
}

// NewProviderOpaqueItem 创建未知 Provider Item
func NewProviderOpaqueItem(providerType, displaySummary string) ContextItem {
	return ContextItem{
		Type: ContextItemProviderOpaque, Meta: NewContextItemMeta(""),
		ProviderType: providerType, DisplaySummary: displaySummary,
	}
}

// Role 返回可投影角色；reasoning/provider activity 无直接角色
func (i ContextItem) Role() ContextRoleType {
	switch i.Type {
	case ContextItemSystemMessage:
		return ContextRoleSystem
	case ContextItemUserMessage:
		return ContextRoleUser
	case ContextItemAssistantMessage:
		return ContextRoleAssistant
	case ContextItemFunctionCallOutput:
		return ContextRoleTool
	default:
		return ""
	}
}

// Validate 校验 Item 结构与必填字段
func (i ContextItem) Validate() error {
	if err := i.Meta.Validate(); err != nil {
		return err
	}
	switch i.Type {
	case ContextItemSystemMessage, ContextItemUserMessage, ContextItemAssistantMessage:
		if len(i.Parts) == 0 {
			return fmt.Errorf("%s 内容不能为空", i.Type)
		}
		for _, part := range i.Parts {
			if err := part.Validate(); err != nil {
				return err
			}
		}
	case ContextItemReasoning:
		for _, part := range append(append([]string(nil), i.SummaryParts...), i.TextParts...) {
			if part == "" {
				return fmt.Errorf("reasoning 内容片段不能为空字符串")
			}
		}
		switch i.Representation {
		case "summary":
			if len(i.SummaryParts) == 0 {
				return fmt.Errorf("SUMMARY reasoning 必须包含 summary_parts")
			}
		case "raw_text":
			if len(i.TextParts) == 0 {
				return fmt.Errorf("RAW_TEXT reasoning 必须包含 text_parts")
			}
		}
	case ContextItemFunctionCall:
		if i.ToolCall == nil {
			return fmt.Errorf("FunctionCallItem 缺少 tool_call")
		}
		if err := i.ToolCall.Validate(); err != nil {
			return err
		}
		if i.Meta.LogicalTurnID == "" {
			return fmt.Errorf("function call 必须具有 logical_turn_id: %s", i.ToolCall.CallID)
		}
	case ContextItemFunctionCallOutput:
		if i.CallID == "" {
			return fmt.Errorf("工具结果的调用 ID 不能为空")
		}
		if i.Meta.LogicalTurnID == "" {
			return fmt.Errorf("function call output 必须具有 logical_turn_id: %s", i.CallID)
		}
	case ContextItemProviderActivity:
		if i.ProviderType == "" {
			return fmt.Errorf("Provider activity type 不能为空")
		}
		if i.SourceCount < 0 {
			return fmt.Errorf("Provider activity source_count 不能为负数")
		}
	case ContextItemProviderOpaque:
		if i.ProviderType == "" {
			return fmt.Errorf("Provider opaque type 不能为空")
		}
	default:
		return fmt.Errorf("不支持的 Context Item 类型: %s", i.Type)
	}
	return nil
}

// GetContextItemText 提取一个 Item 的可见文本，不混合 reasoning 与正文语义
func GetContextItemText(item ContextItem) string {
	switch item.Type {
	case ContextItemSystemMessage, ContextItemUserMessage, ContextItemAssistantMessage:
		var builder strings.Builder
		for _, part := range item.Parts {
			switch part.Type {
			case ContextPartText:
				builder.WriteString(part.Text)
			case ContextPartRefusal:
				builder.WriteString(part.Refusal)
			}
		}
		return builder.String()
	case ContextItemReasoning:
		parts := item.SummaryParts
		if len(parts) == 0 {
			parts = item.TextParts
		}
		return strings.Join(parts, "\n")
	case ContextItemFunctionCallOutput:
		return item.Output
	case ContextItemProviderActivity, ContextItemProviderOpaque:
		return item.DisplaySummary
	default:
		return ""
	}
}

// GetContextResponseText 按 Item 顺序拼接模型可见正文
func GetContextResponseText(items []ContextItem) string {
	var builder strings.Builder
	for _, item := range items {
		if item.Type == ContextItemAssistantMessage {
			builder.WriteString(GetContextItemText(item))
		}
	}
	return builder.String()
}

// GetContextResponseReasoning 按 Item 顺序拼接可展示 reasoning
func GetContextResponseReasoning(items []ContextItem) string {
	parts := make([]string, 0)
	for _, item := range items {
		if item.Type != ContextItemReasoning {
			continue
		}
		if text := GetContextItemText(item); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// GetContextResponseToolCalls 按 Item 顺序提取模型函数调用
func GetContextResponseToolCalls(items []ContextItem) []ContextToolCall {
	calls := make([]ContextToolCall, 0)
	for _, item := range items {
		if item.Type == ContextItemFunctionCall && item.ToolCall != nil {
			calls = append(calls, *item.ToolCall)
		}
	}
	return calls
}

// BuildPortableOutputItems 将无原生 Item 数组的 Provider 响应冻结为确定顺序的通用输出 Items
func BuildPortableOutputItems(content, reasoning string, toolCalls []ContextToolCall, logicalTurnID string) ([]ContextItem, error) {
	logicalTurnID = strings.TrimSpace(logicalTurnID)
	if logicalTurnID == "" {
		return nil, fmt.Errorf("logical_turn_id 不能为空")
	}
	items := make([]ContextItem, 0, 2+len(toolCalls))
	if text := strings.TrimSpace(reasoning); text != "" {
		item := NewReasoningItem(logicalTurnID, text)
		item.Meta.LogicalTurnID = logicalTurnID
		items = append(items, item)
	}
	if text := strings.TrimSpace(content); text != "" {
		item := NewAssistantMessageItem(logicalTurnID, ContextContentPart{Type: ContextPartText, Text: text})
		item.Meta.LogicalTurnID = logicalTurnID
		items = append(items, item)
	}
	for _, call := range toolCalls {
		item := NewFunctionCallItem(logicalTurnID, call)
		item.Meta.LogicalTurnID = logicalTurnID
		items = append(items, item)
	}
	return items, nil
}

// BindOutputItemsToTurn 为一次模型输出的所有 Items 绑定所属逻辑工具轮次
func BindOutputItemsToTurn(items []ContextItem, logicalTurnID string) ([]ContextItem, error) {
	logicalTurnID = strings.TrimSpace(logicalTurnID)
	if logicalTurnID == "" {
		return nil, fmt.Errorf("logical_turn_id 不能为空")
	}
	out := make([]ContextItem, len(items))
	copy(out, items)
	for index := range out {
		out[index].Meta.LogicalTurnID = logicalTurnID
	}
	return out, nil
}

// ReplaceOutputProjection 将旧式正文/工具 Hook 修改映射回 Items，并只清除被替换 Item 的 replay
func ReplaceOutputProjection(items []ContextItem, content string, replaceContent bool, toolCalls []ContextToolCall, replaceToolCalls bool) []ContextItem {
	output := make([]ContextItem, len(items))
	copy(output, items)
	if len(output) == 0 && content == "" && len(toolCalls) == 0 {
		return nil
	}
	templateMeta := NewContextItemMeta("")
	if len(output) > 0 {
		templateMeta = output[0].Meta
	}
	if replaceContent {
		insertIndex := len(output)
		for index, item := range output {
			if item.Type == ContextItemAssistantMessage {
				insertIndex = index
				templateMeta = item.Meta
				break
			}
		}
		filtered := make([]ContextItem, 0, len(output))
		for _, item := range output {
			if item.Type != ContextItemAssistantMessage {
				filtered = append(filtered, item)
			}
		}
		output = filtered
		if text := strings.TrimSpace(content); text != "" {
			item := ContextItem{
				Type:  ContextItemAssistantMessage,
				Meta:  templateMeta,
				Parts: []ContextContentPart{{Type: ContextPartText, Text: text}},
			}
			if insertIndex > len(output) {
				insertIndex = len(output)
			}
			output = append(output, ContextItem{})
			copy(output[insertIndex+1:], output[insertIndex:])
			output[insertIndex] = item
		}
	}
	if replaceToolCalls {
		insertIndex := len(output)
		for index, item := range output {
			if item.Type == ContextItemFunctionCall {
				insertIndex = index
				templateMeta = item.Meta
				break
			}
		}
		filtered := make([]ContextItem, 0, len(output))
		for _, item := range output {
			if item.Type != ContextItemFunctionCall {
				filtered = append(filtered, item)
			}
		}
		output = filtered
		for offset, call := range toolCalls {
			meta := templateMeta
			meta.ItemID = strings.ReplaceAll(uuid.NewString(), "-", "")
			item := ContextItem{Type: ContextItemFunctionCall, Meta: meta, ToolCall: &call}
			position := insertIndex + offset
			if position > len(output) {
				position = len(output)
			}
			output = append(output, ContextItem{})
			copy(output[position+1:], output[position:])
			output[position] = item
		}
	}
	return output
}

// GetContextItemReplay 读取支持 replay 的 Item 上的回放片段
func GetContextItemReplay(item ContextItem) *ContextProviderReplayFragment {
	switch item.Type {
	case ContextItemReasoning, ContextItemAssistantMessage, ContextItemFunctionCall,
		ContextItemProviderActivity, ContextItemProviderOpaque:
		return item.Replay
	default:
		return nil
	}
}

// WithoutContextItemReplay 仅清除当前 Item 的 replay fragment
func WithoutContextItemReplay(item ContextItem) ContextItem {
	if GetContextItemReplay(item) == nil {
		return item
	}
	item.Replay = nil
	return item
}

// BuildProviderEndpointFingerprint 生成不包含鉴权信息的 Provider 端点指纹
func BuildProviderEndpointFingerprint(clientType, baseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	payload := strings.TrimSpace(clientType) + "\n" + normalized
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// canonicalContextJSON 稳定 JSON 编码：键排序、不转义 HTML、紧凑分隔符
func canonicalContextJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		return nil, err
	}
	// encoding/json 已按 map key 排序；再走一次确保数字等无 HTML 转义
	buffer := &strings.Builder{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(generic); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(buffer.String())), nil
}

func sortedContextStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// NormalizeRole 对齐 services/llm_service.py `_normalize_role`：
// 去空白转小写后匹配四个角色，不支持时报错。
func NormalizeRole(roleName string) (ContextRoleType, error) {
	normalized := strings.ToLower(strings.TrimSpace(roleName))
	switch ContextRoleType(normalized) {
	case ContextRoleSystem, ContextRoleUser, ContextRoleAssistant, ContextRoleTool:
		return ContextRoleType(normalized), nil
	default:
		return "", fmt.Errorf("不支持的角色类型: %s", roleName)
	}
}
