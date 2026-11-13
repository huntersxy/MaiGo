package tools

import (
	"github.com/cloudwego/eino/components/tool"
)

// MaisakaCompatToolBuilders 注册 MaiBot maisaka_builtin 的全部工具名
// 模型可见声明使用 Python 逐字规格，执行复用既有实现
func MaisakaCompatToolBuilders() []func() (tool.BaseTool, error) {
	return []func() (tool.BaseTool, error){
		maisakaSpecBuilder("wait", NewWaitTool),
		maisakaSpecBuilder("stop", NewStopTool),
		maisakaSpecBuilder("reply", NewReplyTool),
		maisakaSpecBuilder("view_forward_message", NewViewForwardMessageTool),
		func() (tool.BaseTool, error) { return NewQueryMemoryTool() },
		maisakaSpecBuilder("query_image_memory", NewQueryImageMemoryTool),
		maisakaSpecBuilder("query_person_profile", NewQueryPersonProfileTool),
		maisakaSpecBuilder("send_emoji", NewSendEmojiTool),
		maisakaSpecBuilder("show_emoji_list", NewShowEmojiListTool),
		maisakaSpecBuilder("tool_search", NewToolSearchTool),
		maisakaSpecBuilder("subagent", NewSubagentTool),
		maisakaSpecBuilder("subagent_status", NewSubagentStatusTool),
		maisakaSpecBuilder("subagent_stop", NewSubagentStopTool),
		maisakaSpecBuilder("read_tool_result", NewReadToolResultMaisakaTool),
	}
}
