package maisaka

// BuiltinToolName MaiBot maisaka_builtin 工具名
type BuiltinToolName string

const (
	ToolWait               BuiltinToolName = "wait"
	ToolReply              BuiltinToolName = "reply"
	ToolSchedule           BuiltinToolName = "schedule"
	ToolViewForwardMessage BuiltinToolName = "view_forward_message"
	ToolQueryMemory        BuiltinToolName = "query_memory"
	ToolQueryImageMemory   BuiltinToolName = "query_image_memory"
	ToolQueryPersonProfile BuiltinToolName = "query_person_profile"
	ToolSaveEmoji          BuiltinToolName = "save_emoji"
	ToolToolSearch         BuiltinToolName = "tool_search"
	ToolSubagent           BuiltinToolName = "subagent"
	ToolSubagentStatus     BuiltinToolName = "subagent_status"
	ToolSubagentStop       BuiltinToolName = "subagent_stop"
	ToolReadToolResult     BuiltinToolName = "read_tool_result"
)

// GoToolMapping 记录 MaiGo 当前对等或桥接关系，供迁移与提示词对照
var GoToolMapping = map[BuiltinToolName]string{
	ToolReply:              "reply → speak（仅 content/text 直发；完整 replyer 未迁移）",
	ToolSchedule:           "schedule → services/schedules Command",
	ToolWait:               "wait → 无参数结束本轮；指定 seconds 进入计数等待",
	ToolQueryMemory:        "query_memory → memorix Host Search（Bridge）；mode=episode 为话题/knowledge 替身；aggregate/纯 time 未迁移",
	ToolQueryImageMemory:   "query_image_memory → memory.SearchStickerPage 关键词（无 A_Memorix 同图向量）",
	ToolQueryPersonProfile: "query_person_profile → personinfo.GetProfile",
	ToolSaveEmoji:          "save_emoji → 会话 hash/URL + media.Ensure + MAIBOT_STICKER_DIR + images 表（EMOJI）；可选 VLM 打标",
	ToolViewForwardMessage: "view_forward_message → readForward",
	ToolToolSearch:         "tool_search → deferred 工具关键词检索，发现后按模型轮次动态暴露",
	ToolSubagent:           "subagent → 后台单次 LLM 骨架（planner→utils + model_list failover；无工具 ReAct）",
	ToolSubagentStatus:     "subagent_status → 内存任务状态/结论",
	ToolSubagentStop:       "subagent_stop → 取消运行中任务",
	ToolReadToolResult:     "read_tool_result → maisaka.DefaultToolResultStore（offset/keyword）",
}
