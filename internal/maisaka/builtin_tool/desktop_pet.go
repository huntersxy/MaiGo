package tools

// 桌宠主人聊天的跨会话与待办工具，对齐 src/desktop_pet/chat_tools.py 与 todo_tools.py

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/desktop_pet"
	"MaiGo/internal/services/schedules"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

var (
	desktopPetMu  sync.RWMutex
	desktopPetSvc *desktoppet.Service
)

// RegisterDesktopPetService 注入桌宠服务，供主人聊天工具调用
func RegisterDesktopPetService(service *desktoppet.Service) {
	desktopPetMu.Lock()
	desktopPetSvc = service
	desktopPetMu.Unlock()
}

func currentDesktopPetService() *desktoppet.Service {
	desktopPetMu.RLock()
	defer desktopPetMu.RUnlock()
	return desktopPetSvc
}

// DesktopPetToolBuilders 返回主人聊天可用的桌宠工具构建器
func DesktopPetToolBuilders() []func() (tool.BaseTool, error) {
	return []func() (tool.BaseTool, error){
		NewDesktopPetTodoTool,
		NewListChatSessionsTool,
		NewHandoffToChatTool,
		func() (tool.BaseTool, error) { return NewDesktopPetScreenTool() },
		NewExplorationTaskTool,
		NewSearchExplorationProductsTool,
		NewRecordExperienceReuseTool,
		NewExplorationSchemaTool,
	}
}

// desktopPetChatToolsEnabled 对应 global_config.desktop_pet.chat_tools_enabled
func desktopPetChatToolsEnabled() bool {
	service := currentDesktopPetService()
	return service != nil && service.Config().ChatToolsEnabled
}

// DesktopPetChatToolsEnabled 暴露主人聊天工具开关，供运行时可见性过滤
func DesktopPetChatToolsEnabled() bool {
	return desktopPetChatToolsEnabled()
}

// DesktopPetToolVisible 对齐 Python list_tools 的 owner 会话过滤
func DesktopPetToolVisible(ctx context.Context, name string) bool {
	ownerOnly := map[string]string{
		"desktop_pet_todo": "chat", "list_chat_sessions": "chat", "handoff_to_chat": "chat",
		"desktop_pet_screen":           "screen",
		"desktop_pet_exploration_task": "exploration", "search_exploration_products": "exploration",
		"record_experience_reuse": "exploration", "desktop_pet_schema": "exploration",
	}
	capability, guarded := ownerOnly[name]
	if !guarded {
		return true
	}
	tc := GetToolContext(ctx)
	if tc == nil {
		return false
	}
	owner := tc.Platform == internalchat.Platform &&
		tc.ConversationKind == memory.ConversationKindPrivate &&
		tc.TargetID == internalchat.OwnerNumericID
	if !owner {
		return false
	}
	service := currentDesktopPetService()
	if service == nil {
		return false
	}
	config := service.Config()
	switch capability {
	case "screen":
		return config.ScreenReadEnabled
	case "exploration":
		return config.ExplorationEnabled
	default:
		return config.ChatToolsEnabled
	}
}

func desktopPetOwnerContext(ctx context.Context, message string) (*ToolContext, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil {
		return nil, fmt.Errorf("%s", message)
	}
	if tc.Platform != internalchat.Platform ||
		tc.ConversationKind != memory.ConversationKindPrivate ||
		tc.TargetID != internalchat.OwnerNumericID {
		return nil, fmt.Errorf("%s", message)
	}
	return tc, nil
}

// DesktopPetTodoInput 对齐 desktop_pet_todo 参数
type DesktopPetTodoInput struct {
	Action           string   `json:"action" jsonschema:"description=list 查询；add 新增；update 修改；delete 删除；complete 完成并归档；trigger 条件触发,enum=list,enum=add,enum=update,enum=delete,enum=complete,enum=trigger"`
	IncludeCompleted *bool    `json:"include_completed,omitempty" jsonschema:"description=list 时是否包含已完成的历史事项，默认 false"`
	Kind             string   `json:"kind,omitempty" jsonschema:"description=事项类型，默认 user,enum=bot,enum=user"`
	DueAt            *string  `json:"due_at,omitempty" jsonschema:"description=未来 ISO 8601 日期时间，建议含时区；无时区按服务器本地时间"`
	DelaySeconds     *float64 `json:"delay_seconds,omitempty" jsonschema:"description=从现在起多少秒后触发"`
	ConditionType    string   `json:"condition_type,omitempty" jsonschema:"description=条件类型，默认 natural,enum=natural,enum=app"`
	Condition        *string  `json:"condition,omitempty" jsonschema:"description=触发条件，空字符串取消条件"`
	Revision         string   `json:"revision,omitempty" jsonschema:"description=trigger 必填，来自当前上下文或 list 的真实版本"`
	Evidence         string   `json:"evidence,omitempty" jsonschema:"description=trigger 必填，说明当前条件成立的具体观察"`
	TodoID           string   `json:"todo_id,omitempty" jsonschema:"description=update、delete、complete、trigger 必填，使用查询结果中的 id"`
	Text             string   `json:"text,omitempty" jsonschema:"description=add 必填；update 可选，新的事项内容"`
	// DueAtSet 区分未填写与显式 null（取消定时）
	DueAtSet bool `json:"-" jsonschema:"-"`
}

// UnmarshalJSON 记录 due_at 是否显式传入，null 表示取消定时
func (in *DesktopPetTodoInput) UnmarshalJSON(data []byte) error {
	type alias DesktopPetTodoInput
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*in = DesktopPetTodoInput(decoded)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if value, exists := raw["due_at"]; exists {
		in.DueAtSet = true
		if string(value) == "null" {
			in.DueAt = nil
		}
	}
	return nil
}

// DesktopPetTodoOutput 桌宠待办命令回执
type DesktopPetTodoOutput struct {
	Success bool           `json:"success"`
	Result  map[string]any `json:"result,omitempty"`
}

func desktopPetTodo(ctx context.Context, input *DesktopPetTodoInput) (*DesktopPetTodoOutput, error) {
	if _, err := desktopPetOwnerContext(ctx, "只有主人内部聊天可以管理待办"); err != nil {
		return nil, err
	}
	if input == nil {
		input = &DesktopPetTodoInput{}
	}
	action := strings.TrimSpace(input.Action)
	switch action {
	case "list", "add", "update", "delete", "complete", "trigger":
	default:
		return nil, fmt.Errorf("未知待办操作")
	}
	command := map[string]any{"action": action}
	if action == "list" && input.IncludeCompleted != nil {
		command["include_completed"] = *input.IncludeCompleted
	}
	if action == "add" || (action == "update" && strings.TrimSpace(input.Text) != "") {
		text := strings.TrimSpace(input.Text)
		if count := utf8.RuneCountInString(text); count < 1 || count > 200 {
			return nil, fmt.Errorf("待办内容必须为 1 至 200 字")
		}
		command["text"] = text
	}
	if action == "add" || action == "update" {
		options := map[string]any{}
		if input.Kind != "" {
			options["kind"] = input.Kind
		}
		if input.DueAtSet {
			if input.DueAt == nil {
				options["due_at"] = nil
			} else {
				options["due_at"] = *input.DueAt
			}
		}
		if input.DelaySeconds != nil {
			options["delay_seconds"] = *input.DelaySeconds
		}
		if input.ConditionType != "" {
			options["condition_type"] = input.ConditionType
		}
		if input.Condition != nil {
			options["condition"] = *input.Condition
		}
		command["options"] = options
	}
	todoID := strings.TrimSpace(input.TodoID)
	if action == "update" || action == "delete" || action == "complete" || action == "trigger" {
		if todoID == "" {
			return nil, fmt.Errorf("请先查询待办并提供 todo_id")
		}
		command["todo_id"] = todoID
	}
	var result map[string]any
	var err error
	if action == "trigger" {
		result, err = schedules.Default().TriggerCondition(ctx, todoID, input.Revision, input.Evidence)
	} else {
		result, err = schedules.Default().Command(ctx, command, "")
	}
	if err != nil {
		return nil, err
	}
	return &DesktopPetTodoOutput{Success: true, Result: result}, nil
}

// NewDesktopPetTodoTool 创建桌宠待办工具
func NewDesktopPetTodoTool() (tool.BaseTool, error) {
	return utils.InferTool("desktop_pet_todo", "管理桌宠事项：bot 是到期唤醒自己的任务，user 是给主人的桌面提醒。支持绝对时间 due_at 或相对秒数 delay_seconds，不能同时传；省略时间保留原计划，due_at=null 取消定时。执行失败可查询 error，重新设置时间后重试。list 默认查询未完成，include_completed=true 包含归档；add 新增；update 修改；complete 完成并归档保留；delete 仅在明确要求删除时永久删除。修改、删除前先查询真实 todo_id，不猜 ID。condition_type=app 时程序检测应用，condition 填进程名或窗口标题关键字；natural 时 condition 保存自然语言触发条件，空字符串取消。条件与时间同时存在时，必须到时且条件成立。trigger 仅用于有当前证据的条件待办，必须提供真实 todo_id、revision 和 evidence。", desktopPetTodo)
}

// ListChatSessionsInput 对齐 list_chat_sessions 参数
type ListChatSessionsInput struct {
	Keyword  string `json:"keyword,omitempty"`
	Platform string `json:"platform,omitempty"`
	ChatType string `json:"chat_type,omitempty" jsonschema:"description=group 或 private,enum=group,enum=private"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=默认 20，最大 50"`
}

// ChatSessionOption 可传话聊天流
type ChatSessionOption struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
	ChatType  string `json:"chat_type"`
	TargetID  string `json:"target_id"`
}

// ListChatSessionsOutput 聊天流分页结果
type ListChatSessionsOutput struct {
	Sessions []ChatSessionOption `json:"sessions"`
	Total    int                 `json:"total"`
}

func listChatSessions(ctx context.Context, input *ListChatSessionsInput) (*ListChatSessionsOutput, error) {
	tc, err := desktopPetOwnerContext(ctx, "聊天流工具未启用或当前会话无权使用。")
	if err != nil {
		return nil, err
	}
	if !desktopPetChatToolsEnabled() {
		return nil, fmt.Errorf("聊天流工具未启用或当前会话无权使用。")
	}
	if input == nil {
		input = &ListChatSessionsInput{}
	}
	offset := input.Offset
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if offset < 0 || limit < 1 || limit > 50 {
		return nil, fmt.Errorf("分页参数无效。")
	}
	var rows []chatsession.Record
	if err := tc.MemoryMgr.GetDB().WithContext(ctx).
		Order("last_active_timestamp DESC NULLS LAST, session_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	keyword := strings.ToLower(strings.TrimSpace(input.Keyword))
	platform := strings.TrimSpace(input.Platform)
	chatType := strings.TrimSpace(input.ChatType)
	options := make([]ChatSessionOption, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if row.SessionID == tc.SessionID {
			continue
		}
		kind, targetID, targetText, ok := chatSessionTarget(row)
		if !ok {
			continue
		}
		name := chatsession.DisplayName(row)
		if name == "" {
			name = targetText
		}
		if keyword != "" && !strings.Contains(strings.ToLower(name), keyword) {
			continue
		}
		if platform != "" && !strings.EqualFold(platform, row.Platform) {
			continue
		}
		if chatType != "" && chatType != kind {
			continue
		}
		allowed, allowedErr := tc.MemoryMgr.ConversationAllowed(ctx, kind, targetID)
		if allowedErr != nil || !allowed {
			continue
		}
		options = append(options, ChatSessionOption{
			SessionID: row.SessionID, Name: name, Platform: row.Platform,
			ChatType: kind, TargetID: targetText,
		})
	}
	total := len(options)
	if offset >= total {
		return &ListChatSessionsOutput{Sessions: []ChatSessionOption{}, Total: total}, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return &ListChatSessionsOutput{Sessions: options[offset:end], Total: total}, nil
}

// NewListChatSessionsTool 创建聊天流列表工具
func NewListChatSessionsTool() (tool.BaseTool, error) {
	return utils.InferTool("list_chat_sessions", "查询可传话的真实聊天流。先按名称查询；重名必须确认，不能猜测 ID。", listChatSessions)
}

// HandoffToChatInput 对齐 handoff_to_chat 参数
type HandoffToChatInput struct {
	TargetSessionID string   `json:"target_session_id"`
	Instruction     string   `json:"instruction"`
	ImageRefs       []string `json:"image_refs,omitempty" jsonschema:"description=当前聊天带图消息 ID 或图片 hash"`
}

// HandoffToChatOutput 跨会话任务回执
type HandoffToChatOutput struct {
	TaskID          string `json:"task_id,omitempty"`
	Queued          bool   `json:"queued"`
	TargetSessionID string `json:"target_session_id"`
	Name            string `json:"name"`
	Notice          string `json:"notice"`
}

func handoffToChat(ctx context.Context, input *HandoffToChatInput) (*HandoffToChatOutput, error) {
	tc, err := desktopPetOwnerContext(ctx, "聊天流工具未启用或当前会话无权使用。")
	if err != nil {
		return nil, err
	}
	if !desktopPetChatToolsEnabled() {
		return nil, fmt.Errorf("聊天流工具未启用或当前会话无权使用。")
	}
	if input == nil {
		return nil, fmt.Errorf("请选择其他真实聊天流。")
	}
	target := strings.TrimSpace(input.TargetSessionID)
	if target == "" || target == tc.SessionID {
		return nil, fmt.Errorf("请选择其他真实聊天流。")
	}
	instruction := strings.TrimSpace(input.Instruction)
	if count := utf8.RuneCountInString(instruction); count < 1 || count > 10000 {
		return nil, fmt.Errorf("任务内容应为 1 至 10000 字。")
	}
	var row chatsession.Record
	if err := tc.MemoryMgr.GetDB().WithContext(ctx).Where("session_id = ?", target).First(&row).Error; err != nil {
		return nil, fmt.Errorf("请选择其他真实聊天流。")
	}
	kind, targetID, _, ok := chatSessionTarget(&row)
	if !ok {
		return nil, fmt.Errorf("请选择其他真实聊天流。")
	}
	allowed, err := tc.MemoryMgr.ConversationAllowed(ctx, kind, targetID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, fmt.Errorf("适配器策略禁止向此聊天流传话。")
	}
	// 图片引用在策略校验之后解析，与 Python chat_tools.py:84-92 的先后顺序一致
	var images []media.Ref
	if len(input.ImageRefs) > 0 {
		if tc.SourceImages == nil {
			// 生产路径由 agent/context.go 注入 session.SourceImages，此处仅防御未注入的调用方
			return nil, fmt.Errorf("当前会话未提供图片来源索引，无法解析图片引用")
		}
		available := tc.SourceImages()
		for _, ref := range input.ImageRefs {
			refs, ok := available[ref]
			if !ok {
				return nil, fmt.Errorf("找不到图片引用：%s", ref)
			}
			images = append(images, refs...)
		}
	}
	sourceName := "主人内部聊天"
	if tc.SessionID != "" {
		var source chatsession.Record
		if err := tc.MemoryMgr.GetDB().WithContext(ctx).Where("session_id = ?", tc.SessionID).First(&source).Error; err == nil {
			if name := chatsession.DisplayName(&source); name != "" {
				sourceName = name
			}
		}
	}
	text := fmt.Sprintf("[来自主人聊天 %s 的内部任务，不是本聊天成员的发言]\n%s", sourceName, instruction)
	if tc.InternalTaskCallback == nil {
		return nil, fmt.Errorf("内部任务通道未接线")
	}
	receipt, err := tc.InternalTaskCallback(ctx, kind, targetID, text, "desktop_pet_handoff", "主人跨聊天流传话", images)
	if err != nil {
		return nil, err
	}
	name := chatsession.DisplayName(&row)
	if name == "" {
		name = target
	}
	out := &HandoffToChatOutput{
		TargetSessionID: target, Name: name,
		Notice: "任务已入队，尚不代表已经发送消息。",
	}
	if receipt != nil {
		out.TaskID, _ = receipt["task_id"].(string)
		out.Queued, _ = receipt["queued"].(bool)
	}
	return out, nil
}

// NewHandoffToChatTool 创建跨会话任务工具
func NewHandoffToChatTool() (tool.BaseTool, error) {
	return utils.InferTool("handoff_to_chat", "向查询得到的聊天流 Agent 传达用户交代的任务并唤醒。回执只表示入队，不表示已发送消息；不要自行循环转交。", handoffToChat)
}

// chatSessionTarget 把 chat_sessions 记录还原为 Agent 会话类型与数值目标
func chatSessionTarget(row *chatsession.Record) (kind string, targetID int64, targetText string, ok bool) {
	if row == nil {
		return "", 0, "", false
	}
	if row.IsGroup() {
		groupID := strings.TrimSpace(*row.GroupID)
		if groupID == "" {
			return "", 0, "", false
		}
		if internalchat.IsVirtualGroupID(groupID) {
			return memory.ConversationKindGroup, internalchat.VirtualTargetID(groupID), groupID, true
		}
		parsed, err := strconv.ParseInt(groupID, 10, 64)
		if err != nil || parsed <= 0 {
			return "", 0, "", false
		}
		return memory.ConversationKindGroup, parsed, groupID, true
	}
	if row.UserID == nil {
		return "", 0, "", false
	}
	userID := strings.TrimSpace(*row.UserID)
	if userID == "" {
		return "", 0, "", false
	}
	if strings.EqualFold(row.Platform, internalchat.Platform) && userID == internalchat.OwnerUserID {
		return memory.ConversationKindPrivate, internalchat.OwnerNumericID, userID, true
	}
	parsed, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || parsed <= 0 {
		return "", 0, "", false
	}
	return memory.ConversationKindPrivate, parsed, userID, true
}

// NotifyDesktopPetUserMessage 通知桌宠主人最近活动，对齐 notify_user_message
func NotifyDesktopPetUserMessage(platform string, userID int64, isGroup bool) {
	if service := currentDesktopPetService(); service != nil {
		service.NotifyUserMessage(platform, strconv.FormatInt(userID, 10), isGroup)
	}
}
