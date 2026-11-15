package runtime

import (
	"context"
	"fmt"

	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"
	maisakautils "MaiGo/internal/maisaka/utils"
)

// agentToolRecordStore 把 Maisaka 工具执行记录落到 tool_records 表
type agentToolRecordStore struct {
	mem              *memory.Manager
	recordStructured bool
}

// StoreToolExecution 构造统一工具记录并写入 tool_records
func (s *agentToolRecordStore) StoreToolExecution(ctx context.Context, invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult, spec *maisaka.ToolSpec) (map[string]any, error) {
	if s == nil || s.mem == nil {
		return nil, fmt.Errorf("记忆管理器未初始化")
	}
	record, err := s.mem.StoreToolInfo(ctx, buildToolRecordInput(invocation, result, spec, s.recordStructured))
	if err != nil {
		return nil, err
	}
	return toolRecordSavedRecord(record), nil
}

// toolRecordSavedRecord 按 Python database_service 返回的 dict 形状暴露已保存记录
func toolRecordSavedRecord(record *memory.ToolRecord) map[string]any {
	if record == nil {
		return nil
	}
	var reasoning any
	if record.ToolReasoning != nil {
		reasoning = *record.ToolReasoning
	}
	return map[string]any{
		"id":             record.ID,
		"tool_id":        record.ToolID,
		"timestamp":      record.Timestamp,
		"session_id":     record.SessionID,
		"tool_name":      record.ToolName,
		"tool_data":      record.ToolData,
		"tool_reasoning": reasoning,
	}
}

func buildToolRecordInput(invocation maisaka.ToolInvocation, result maisaka.ToolExecutionResult, spec *maisaka.ToolSpec, recordStructured bool) memory.ToolRecordInput {
	payload := maisakautils.BuildToolRecordPayload(invocation, result, spec, maisakautils.ToolRecordOptions{
		RecordStructuredContent: recordStructured,
	})
	return memory.ToolRecordInput{
		ToolID:        invocation.CallID,
		SessionID:     invocation.SessionID,
		ToolName:      invocation.ToolName,
		ToolReasoning: invocation.LatestThought,
		ToolData:      payload,
	}
}
