package model_client

// 工具调用参数的 JSON 修复
// 对等 MaiBot src/llm_models/model_client/openai_client.py 的 _parse_tool_arguments 所用 json_repair 0.60.1
//（产品 pin 见 requirements.txt:13）；对齐记录见 docs/migration/verification/VERIFY_JSON_REPAIR_0601_2026-11-06.md
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
