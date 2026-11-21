package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/services/schedules"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type ScheduleInput struct {
	Action           string   `json:"action"`
	Text             *string  `json:"text,omitempty"`
	DelaySeconds     *float64 `json:"delay_seconds,omitempty"`
	DueAt            *string  `json:"due_at,omitempty"`
	TodoID           string   `json:"todo_id,omitempty"`
	IncludeCompleted *bool    `json:"include_completed,omitempty"`
	Kind             *string  `json:"kind,omitempty"`
	ConditionType    *string  `json:"condition_type,omitempty"`
	Condition        *string  `json:"condition,omitempty"`
	Revision         *string  `json:"revision,omitempty"`
	Evidence         *string  `json:"evidence,omitempty"`
	dueAtSet         bool
}

func (in *ScheduleInput) UnmarshalJSON(data []byte) error {
	type input ScheduleInput
	var decoded input
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if name != "due_at" && string(value) == "null" {
			return fmt.Errorf("%s 不能为 null", name)
		}
	}
	*in = ScheduleInput(decoded)
	_, in.dueAtSet = fields["due_at"]
	return nil
}

type ScheduleOutput struct {
	Success bool           `json:"success"`
	Result  map[string]any `json:"result,omitempty"`
}

func scheduleTool(ctx context.Context, input *ScheduleInput) (*ScheduleOutput, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil || strings.TrimSpace(tc.SessionID) == "" {
		return nil, fmt.Errorf("找不到当前真实聊天流，不能创建或管理日程")
	}
	if input == nil {
		return nil, fmt.Errorf("缺少日程参数")
	}
	var record chatsession.Record
	if err := tc.MemoryMgr.GetDB().WithContext(ctx).Where("session_id = ?", tc.SessionID).First(&record).Error; err != nil {
		return nil, fmt.Errorf("找不到当前真实聊天流，不能创建或管理日程")
	}
	service := schedules.Default()
	owner := service.IsOwner(ctx, tc.SessionID)
	if !owner && (input.Kind != nil || input.Condition != nil || input.ConditionType != nil || input.Revision != nil || input.Evidence != nil) {
		return nil, fmt.Errorf("桌面提醒和条件待办仅限主人内部聊天")
	}
	action := input.Action
	switch action {
	case "add", "list", "update", "cancel", "complete":
	case "delete", "trigger":
		if !owner {
			return nil, fmt.Errorf("当前聊天不支持此日程操作")
		}
	default:
		return nil, fmt.Errorf("当前聊天不支持此日程操作")
	}
	command := map[string]any{"action": action}
	if action == "list" {
		command["include_completed"] = input.IncludeCompleted != nil && *input.IncludeCompleted
	}
	if action != "add" && action != "list" {
		if strings.TrimSpace(input.TodoID) == "" {
			return nil, fmt.Errorf("请先查询并填写真实 todo_id")
		}
		command["todo_id"] = input.TodoID
	}
	if action == "add" || action == "update" {
		if input.DelaySeconds != nil && input.dueAtSet {
			return nil, fmt.Errorf("delay_seconds 和 due_at 不能同时填写")
		}
		if !owner && action == "add" && input.DelaySeconds == nil && input.DueAt == nil {
			return nil, fmt.Errorf("新增日程必须填写触发时间")
		}
		if action == "add" || input.Text != nil {
			text := ""
			if input.Text != nil {
				text = strings.TrimSpace(*input.Text)
			}
			if length := utf8.RuneCountInString(text); length < 1 || length > 200 {
				return nil, fmt.Errorf("事项内容必须为 1 至 200 字")
			}
			command["text"] = text
		}
		options := map[string]any{}
		if input.dueAtSet {
			if input.DueAt != nil {
				options["due_at"] = *input.DueAt
			} else if owner {
				options["due_at"] = nil
			} else {
				return nil, fmt.Errorf("due_at 必须为未来 ISO 8601 时间")
			}
		}
		if input.DelaySeconds != nil {
			options["delay_seconds"] = *input.DelaySeconds
		}
		if action == "add" {
			name := chatsession.DisplayName(&record)
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("无法获取真实聊天名称")
			}
			options["kind"] = "bot"
			options["session_id"] = tc.SessionID
			options["session_name"] = name
		}
		for key, value := range map[string]*string{"kind": input.Kind, "condition": input.Condition, "condition_type": input.ConditionType} {
			if value != nil {
				options[key] = *value
			}
		}
		command["options"] = options
	}
	var result map[string]any
	var err error
	if action == "trigger" {
		if input.Revision == nil || input.Evidence == nil {
			return nil, fmt.Errorf("trigger 必须提供 revision 和 evidence")
		}
		result, err = service.TriggerCondition(ctx, tc.SessionID, input.TodoID, *input.Revision, *input.Evidence)
	} else {
		result, err = service.Command(ctx, command, tc.SessionID)
	}
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	if rawItems, ok := result["items"].([]map[string]any); ok {
		items = rawItems
	} else if item, ok := result["item"].(map[string]any); ok {
		items = append(items, item)
	}
	for _, item := range items {
		if due, ok := item["due_at"].(float64); ok {
			item["due_at_local"] = time.Unix(0, int64(due*1e9)).In(time.Local).Format(time.RFC3339)
		}
	}
	return &ScheduleOutput{Success: true, Result: result}, nil
}

func NewScheduleTool() (tool.InvokableTool, error) {
	return utils.InferTool("schedule", "管理日程、待办和主人桌面提醒", scheduleTool)
}

func scheduleToolSpec(owner bool) MaisakaToolSpec {
	actions := []string{"add", "list", "update", "cancel", "complete"}
	description := "创建，查询或者修改日程，日程可以在定时唤起提醒，当需要在特定时间点做事时使用。"
	dueAt := map[string]any{"type": "string", "description": "未来 ISO 8601 时间，建议带时区"}
	properties := map[string]any{
		"text":              map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "事项内容，add 必填"},
		"delay_seconds":     map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "多少秒后触发，与 due_at 二选一"},
		"due_at":            dueAt,
		"todo_id":           map[string]any{"type": "string", "minLength": 1, "description": "操作已有事项时填写查询返回的真实 id"},
		"include_completed": map[string]any{"type": "boolean", "description": "list 时包含已完成或取消的事项，默认 false"},
	}
	if owner {
		actions = append(actions, "delete", "trigger")
		description += "也可记录无定时待办、设置条件或给主人发桌面提醒。delete 仅用于明确要求永久删除的事项。"
		dueAt["type"] = []string{"string", "null"}
		dueAt["description"] = "未来 ISO 8601 时间，建议带时区；null 取消定时"
		properties["kind"] = map[string]any{"type": "string", "enum": []string{"bot", "user"}, "description": "bot 唤醒你执行任务（默认）；user 给主人显示桌面提醒"}
		properties["condition_type"] = map[string]any{"type": "string", "enum": []string{"natural", "app"}, "description": "natural 由你判断条件（默认）；app 由程序检测应用"}
		properties["condition"] = map[string]any{"type": "string", "maxLength": 500, "description": "触发条件；app 填进程名或窗口标题关键字；空字符串取消条件"}
		properties["revision"] = map[string]any{"type": "string", "description": "trigger 必填，使用当前快照或查询返回的版本"}
		properties["evidence"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 1000, "description": "trigger 必填，说明条件成立的实际依据"}
	} else {
		description += "新增必须填写未来时间。"
	}
	properties["action"] = map[string]any{"type": "string", "enum": actions}
	description += "修改前先查询真实 todo_id；update 省略时间保留计划。创建日程只登记任务。"
	return MaisakaToolSpec{Name: "schedule", Description: description, Schema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"action"},
		"properties": properties, "not": map[string]any{"required": []string{"delay_seconds", "due_at"}},
	}}
}
