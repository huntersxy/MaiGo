package tools

// 为当前真实聊天创建、查看和管理持久化单次日程，对齐 maisaka/builtin_tool/schedule.py。

import (
	"context"
	"fmt"
	"strings"
	"time"

	chatsession "MaiGo/internal/chat/session"
	"MaiGo/internal/services/schedules"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// ScheduleInput 对齐 schedule 工具参数
type ScheduleInput struct {
	Action           string   `json:"action" jsonschema:"description=add 新增；list 查询；update 修改；cancel 取消；complete 完成,enum=add,enum=list,enum=update,enum=cancel,enum=complete"`
	Text             string   `json:"text,omitempty" jsonschema:"description=到期要执行的任务"`
	DelaySeconds     *float64 `json:"delay_seconds,omitempty" jsonschema:"description=从现在起多少秒后唤醒"`
	DueAt            *string  `json:"due_at,omitempty" jsonschema:"description=未来 ISO 8601 时间"`
	TodoID           string   `json:"todo_id,omitempty" jsonschema:"description=查询返回的真实日程 id"`
	IncludeCompleted *bool    `json:"include_completed,omitempty" jsonschema:"description=list 时包含已完成或取消的历史，默认 false"`
}

// ScheduleOutput 日程命令回执
type ScheduleOutput struct {
	Success bool           `json:"success"`
	Result  map[string]any `json:"result,omitempty"`
}

func scheduleTool(ctx context.Context, input *ScheduleInput) (*ScheduleOutput, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.MemoryMgr == nil || strings.TrimSpace(tc.SessionID) == "" {
		return nil, fmt.Errorf("找不到当前真实聊天流，不能创建或管理日程")
	}
	var record chatsession.Record
	if err := tc.MemoryMgr.GetDB().WithContext(ctx).Where("session_id = ?", tc.SessionID).First(&record).Error; err != nil {
		return nil, fmt.Errorf("找不到当前真实聊天流，不能创建或管理日程")
	}
	action := strings.TrimSpace(input.Action)
	switch action {
	case "add", "list", "update", "cancel", "complete":
	default:
		return nil, fmt.Errorf("未知日程操作")
	}
	command := map[string]any{"action": action}
	if action == "list" {
		command["include_completed"] = input.IncludeCompleted != nil && *input.IncludeCompleted
	}
	if action == "update" || action == "cancel" || action == "complete" {
		if strings.TrimSpace(input.TodoID) == "" {
			return nil, fmt.Errorf("请先查询并填写真实 todo_id")
		}
		command["todo_id"] = input.TodoID
	}
	if action == "add" || action == "update" {
		if input.DelaySeconds != nil && input.DueAt != nil {
			return nil, fmt.Errorf("delay_seconds 和 due_at 不能同时填写")
		}
		if action == "add" && (strings.TrimSpace(input.Text) == "" || (input.DelaySeconds == nil && input.DueAt == nil)) {
			return nil, fmt.Errorf("新增日程必须填写任务内容和触发时间")
		}
		if input.Text != "" {
			command["text"] = input.Text
		}
		options := map[string]any{}
		if input.DueAt != nil {
			options["due_at"] = *input.DueAt
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
		command["options"] = options
	}
	result, err := schedules.Default().Command(ctx, command, tc.SessionID)
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
			item["due_at_local"] = time.Unix(0, int64(due*1e9)).In(time.Local).Format("2006-01-02T15:04:05-07:00")
		}
	}
	return &ScheduleOutput{Success: true, Result: result}, nil
}

// NewScheduleTool 创建单次日程工具
func NewScheduleTool() (tool.InvokableTool, error) {
	return utils.InferTool("schedule",
		"创建，查询或者修改日程，日程可以在定时唤起提醒，当需要在特定时间点做事时使用。新增必须填写 text 和未来时间：delay_seconds 或 ISO 8601 due_at 二选一。修改前先查询真实 todo_id；update 省略时间保留计划。创建日程只登记任务。",
		scheduleTool)
}
