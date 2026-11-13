package payloadcontent

// Context Item 关系协议
// 对等 MaiBot src/llm_models/payload_content/context_protocol.py：
//   - 列表位置是唯一顺序
//   - 只描述 function call/output 关系，不引入 Response Group 或 ordinal
//   - 错误文案与 Python 一致，便于插件与快照回归比对

import (
	"fmt"
	"sort"
	"strings"
)

// ContextProtocolMode 不同生命周期边界采用的关系约束
type ContextProtocolMode string

const (
	ContextProtocolRequestContext ContextProtocolMode = "request_context"
	ContextProtocolModelOutput    ContextProtocolMode = "model_output"
	ContextProtocolHistory        ContextProtocolMode = "history"
)

// ContextRelationReport 一次纯关系分析的结果
type ContextRelationReport struct {
	DuplicateItemIDs       []string
	DuplicateCallIDs       []string
	DuplicateOutputCallIDs []string
	OrphanOutputCallIDs    []string
	UnansweredCallIDs      []string
	MisplacedOutputCallIDs []string
	MismatchedTurnCallIDs  []string
	MissingTurnCallIDs     []string
	InvalidTurnIDs         []string
}

// AnalyzeContextItemRelations 按列表位置分析 call/output 关系，不修改输入
func AnalyzeContextItemRelations(items []ContextItem, pendingCallIDs []string) ContextRelationReport {
	pending := make(map[string]bool, len(pendingCallIDs))
	for _, callID := range pendingCallIDs {
		if callID != "" {
			pending[callID] = true
		}
	}
	itemsByID := make(map[string]ContextItem, len(items))
	duplicateItemIDs := map[string]bool{}
	calls := make(map[string]struct {
		index int
		item  ContextItem
	}, len(items))
	duplicateCallIDs := map[string]bool{}
	outputs := make(map[string]struct {
		index int
		item  ContextItem
	}, len(items))
	duplicateOutputCallIDs := map[string]bool{}
	missingTurnCallIDs := map[string]bool{}
	invalidTurnIDs := map[string]bool{}

	addInvalidTurn := func(turnID string) {
		if turnID != "" {
			invalidTurnIDs[turnID] = true
		}
	}

	for index, item := range items {
		itemID := item.Meta.ItemID
		if previous, exists := itemsByID[itemID]; exists {
			duplicateItemIDs[itemID] = true
			addInvalidTurn(previous.Meta.LogicalTurnID)
			addInvalidTurn(item.Meta.LogicalTurnID)
		} else {
			itemsByID[itemID] = item
		}

		if item.Type == ContextItemFunctionCall {
			if item.ToolCall == nil {
				continue
			}
			callID := item.ToolCall.CallID
			if previous, exists := calls[callID]; exists {
				duplicateCallIDs[callID] = true
				addInvalidTurn(previous.item.Meta.LogicalTurnID)
				addInvalidTurn(item.Meta.LogicalTurnID)
			} else {
				calls[callID] = struct {
					index int
					item  ContextItem
				}{index: index, item: item}
			}
			if item.Meta.LogicalTurnID == "" {
				missingTurnCallIDs[callID] = true
			}
			continue
		}

		if item.Type == ContextItemFunctionCallOutput {
			callID := item.CallID
			if previous, exists := outputs[callID]; exists {
				duplicateOutputCallIDs[callID] = true
				addInvalidTurn(previous.item.Meta.LogicalTurnID)
				addInvalidTurn(item.Meta.LogicalTurnID)
			} else {
				outputs[callID] = struct {
					index int
					item  ContextItem
				}{index: index, item: item}
			}
			if item.Meta.LogicalTurnID == "" {
				missingTurnCallIDs[callID] = true
			}
		}
	}

	orphanOutputCallIDs := map[string]bool{}
	for callID := range outputs {
		if _, exists := calls[callID]; !exists {
			orphanOutputCallIDs[callID] = true
		}
	}
	unansweredCallIDs := map[string]bool{}
	for callID := range calls {
		if _, exists := outputs[callID]; exists {
			continue
		}
		if pending[callID] {
			continue
		}
		unansweredCallIDs[callID] = true
	}
	misplacedOutputCallIDs := map[string]bool{}
	mismatchedTurnCallIDs := map[string]bool{}
	for callID, call := range calls {
		output, exists := outputs[callID]
		if !exists {
			continue
		}
		callTurnID := call.item.Meta.LogicalTurnID
		outputTurnID := output.item.Meta.LogicalTurnID
		if output.index <= call.index {
			misplacedOutputCallIDs[callID] = true
		}
		if callTurnID != outputTurnID {
			mismatchedTurnCallIDs[callID] = true
		}
		if output.index <= call.index || callTurnID != outputTurnID {
			addInvalidTurn(callTurnID)
			addInvalidTurn(outputTurnID)
		}
	}
	for callID := range orphanOutputCallIDs {
		addInvalidTurn(outputs[callID].item.Meta.LogicalTurnID)
	}
	for callID := range unansweredCallIDs {
		addInvalidTurn(calls[callID].item.Meta.LogicalTurnID)
	}

	return ContextRelationReport{
		DuplicateItemIDs:       sortedStringSet(duplicateItemIDs),
		DuplicateCallIDs:       sortedStringSet(duplicateCallIDs),
		DuplicateOutputCallIDs: sortedStringSet(duplicateOutputCallIDs),
		OrphanOutputCallIDs:    sortedStringSet(orphanOutputCallIDs),
		UnansweredCallIDs:      sortedStringSet(unansweredCallIDs),
		MisplacedOutputCallIDs: sortedStringSet(misplacedOutputCallIDs),
		MismatchedTurnCallIDs:  sortedStringSet(mismatchedTurnCallIDs),
		MissingTurnCallIDs:     sortedStringSet(missingTurnCallIDs),
		InvalidTurnIDs:         sortedStringSet(invalidTurnIDs),
	}
}

func sortedStringSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ValidateContextItems 在明确的生命周期边界校验 Item 类型和工具关系
func ValidateContextItems(items []ContextItem, mode ContextProtocolMode, pendingCallIDs []string) error {
	if mode == ContextProtocolModelOutput {
		invalidTypes := map[string]bool{}
		for _, item := range items {
			if !isContextModelOutputItem(item) {
				invalidTypes[string(item.Type)] = true
			}
		}
		if len(invalidTypes) > 0 {
			return fmt.Errorf("模型输出包含非模型输出 Item: %s", pythonStringList(sortedStringSet(invalidTypes)))
		}
	}
	report := AnalyzeContextItemRelations(items, pendingCallIDs)
	if len(report.DuplicateItemIDs) > 0 {
		return fmt.Errorf("Context Items 存在重复 item_id: %s", pythonStringList(report.DuplicateItemIDs))
	}
	if len(report.DuplicateCallIDs) > 0 {
		return fmt.Errorf("Context Items 存在重复 function call: %s", pythonStringList(report.DuplicateCallIDs))
	}
	if len(report.DuplicateOutputCallIDs) > 0 {
		return fmt.Errorf("Context Items 存在重复 function output: %s", pythonStringList(report.DuplicateOutputCallIDs))
	}
	if len(report.MissingTurnCallIDs) > 0 {
		return fmt.Errorf("工具 Item 缺少 logical_turn_id: %s", pythonStringList(report.MissingTurnCallIDs))
	}
	if len(report.OrphanOutputCallIDs) > 0 {
		return fmt.Errorf("Context Items 存在孤儿 function output: %s", pythonStringList(report.OrphanOutputCallIDs))
	}
	if len(report.MisplacedOutputCallIDs) > 0 {
		return fmt.Errorf("function output 位于调用之前: %s", pythonStringList(report.MisplacedOutputCallIDs))
	}
	if len(report.MismatchedTurnCallIDs) > 0 {
		return fmt.Errorf("function call/output logical_turn_id 不一致: %s", pythonStringList(report.MismatchedTurnCallIDs))
	}
	if mode != ContextProtocolModelOutput && len(report.UnansweredCallIDs) > 0 {
		return fmt.Errorf("Context Items 存在未回答 function call: %s", pythonStringList(report.UnansweredCallIDs))
	}
	return nil
}

func isContextModelOutputItem(item ContextItem) bool {
	switch item.Type {
	case ContextItemReasoning, ContextItemAssistantMessage, ContextItemFunctionCall,
		ContextItemProviderActivity, ContextItemProviderOpaque:
		return true
	default:
		return false
	}
}

// PruneContextItemsForHistory 按 HISTORY 规则删除非法完整 turn，并严格校验保留结果
func PruneContextItemsForHistory(items []ContextItem, pendingCallIDs []string) ([]ContextItem, ContextRelationReport, error) {
	report := AnalyzeContextItemRelations(items, pendingCallIDs)
	invalidTurns := make(map[string]bool, len(report.InvalidTurnIDs))
	for _, turnID := range report.InvalidTurnIDs {
		invalidTurns[turnID] = true
	}
	retained := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if item.Meta.LogicalTurnID != "" && invalidTurns[item.Meta.LogicalTurnID] {
			continue
		}
		retained = append(retained, item)
	}
	if err := ValidateContextItems(retained, ContextProtocolHistory, pendingCallIDs); err != nil {
		return nil, report, err
	}
	return retained, report, nil
}

// SelectContextItemsWithProtocolClosure 扩展所选工具 turn，并从历史选择结果删除非法 turn
func SelectContextItemsWithProtocolClosure(items []ContextItem, selectedItemIDs []string, pendingCallIDs []string) ([]ContextItem, error) {
	selected := make(map[string]bool, len(selectedItemIDs))
	for _, itemID := range selectedItemIDs {
		selected[itemID] = true
	}
	knownIDs := make(map[string]bool, len(items))
	toolTurnIDs := make(map[string]bool)
	for _, item := range items {
		knownIDs[item.Meta.ItemID] = true
		if item.Type == ContextItemFunctionCall || item.Type == ContextItemFunctionCallOutput {
			if item.Meta.LogicalTurnID != "" {
				toolTurnIDs[item.Meta.LogicalTurnID] = true
			}
		}
	}
	unknown := make([]string, 0)
	for itemID := range selected {
		if !knownIDs[itemID] {
			unknown = append(unknown, itemID)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("未知 Context Item: %s", pythonStringList(unknown))
	}

	for turnID := range toolTurnIDs {
		turnItemIDs := make([]string, 0)
		matched := false
		for _, item := range items {
			if item.Meta.LogicalTurnID != turnID {
				continue
			}
			turnItemIDs = append(turnItemIDs, item.Meta.ItemID)
			if selected[item.Meta.ItemID] {
				matched = true
			}
		}
		if matched {
			for _, itemID := range turnItemIDs {
				selected[itemID] = true
			}
		}
	}

	report := AnalyzeContextItemRelations(items, pendingCallIDs)
	invalidTurns := make(map[string]bool, len(report.InvalidTurnIDs))
	for _, turnID := range report.InvalidTurnIDs {
		invalidTurns[turnID] = true
	}
	out := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if !selected[item.Meta.ItemID] {
			continue
		}
		if item.Meta.LogicalTurnID != "" && invalidTurns[item.Meta.LogicalTurnID] {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

// pythonStringList 渲染与 Python list repr 一致的单引号列表
func pythonStringList(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
