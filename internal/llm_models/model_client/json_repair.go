package model_client

// 工具调用参数的 JSON 修复
// 对等 MaiBot src/llm_models/model_client/openai_client.py 的 _parse_tool_arguments 所用 json_repair 0.60.1
//（版本 pin 见 Dockerfile 的 maibot==1.3.19）；归一化规则对齐该 Python 实现。
//
// Python 对齐的归一化与 OrderedMap 展平逻辑统一放在 internal/common/jsonrepair，
// 与 learners 的表达/黑话解析共用同一份实现。

import (
	"MaiGo/internal/common/jsonrepair"
)

// chatRepairJSON 按 Python json_repair 0.60.1 的口径修复工具调用参数。
func chatRepairJSON(raw string) (any, error) {
	return jsonrepair.RepairPythonParity(raw)
}
