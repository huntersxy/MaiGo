package search

// 答疑 Agent 的内置技能目录和单轮工作计划，对齐 src/webui/services/ai_help_harness.py

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

const (
	loadHelpSkillTool  = "load_help_skill"
	updateHelpPlanTool = "update_help_plan"
)

var helpHarnessToolNames = map[string]struct{}{
	loadHelpSkillTool:  {},
	updateHelpPlanTool: {},
}

var helpSkillIDs = map[string]struct{}{
	"setup": {}, "troubleshoot": {}, "plugin": {}, "develop": {},
}

// HelpHarness 技能与计划仅在单次答疑运行内生效
type HelpHarness struct {
	Locale       string
	ActiveSkills map[string]string
	Plan         []PlanStep
}

// NewHelpHarness 创建单轮答疑技能状态
func NewHelpHarness(locale string) *HelpHarness {
	return &HelpHarness{Locale: locale, ActiveSkills: map[string]string{}}
}

func buildHarnessToolInfos() []*schema.ToolInfo {
	return []*schema.ToolInfo{
		buildToolInfo(
			loadHelpSkillTool,
			"Load a built-in workflow only when its scope fits the current question. The plugin workflow exposes plugin tools on the next model step.",
			map[string]any{
				"skill": map[string]any{
					"type": "string",
					"enum": []string{"setup", "troubleshoot", "plugin", "develop"},
					"description": "setup: initial setup, model providers, configuration or migration; " +
						"troubleshoot: failures, logs, connectivity, unexpected behavior; " +
						"plugin: find, compare, install or configure plugins; " +
						"develop: plugin SDK, extension APIs, implementation or debugging plugin code.",
				},
			},
			[]any{"skill"},
		),
		buildToolInfo(
			updateHelpPlanTool,
			"For multi-step work, publish or replace a concise user-visible plan in the user's language. "+
				"Use at most one in-progress step. Mark a step completed only after doing the work. "+
				"Do not include secrets, raw logs, or private reasoning. Skip for straightforward lookups.",
			map[string]any{
				"steps": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 5,
					"items": map[string]any{
						"type": "object", "additionalProperties": false,
						"properties": map[string]any{
							"title":  map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
							"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}},
						},
						"required": []any{"title", "status"},
					},
				},
			},
			[]any{"steps"},
		),
	}
}

// Execute 执行技能加载或计划更新
func (h *HelpHarness) Execute(name string, arguments map[string]any) (map[string]any, *ProgressEvent) {
	if h == nil {
		return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
	}
	switch name {
	case loadHelpSkillTool:
		skill, _ := arguments["skill"].(string)
		skill = strings.TrimSpace(skill)
		if _, ok := helpSkillIDs[skill]; !ok {
			return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
		}
		if _, loaded := h.ActiveSkills[skill]; loaded {
			return map[string]any{"skill": skill, "message": "该技能已经加载，请继续执行"}, nil
		}
		if len(h.ActiveSkills) >= 2 {
			return map[string]any{"error": "单轮最多加载两个相关技能，请使用已加载的技能完成任务"}, nil
		}
		instructions := aiSearchPromptText("webui_help_skill_"+skill, h.Locale, nil)
		h.ActiveSkills[skill] = instructions
		event := NewProgressEvent("skill")
		event.Skill = skill
		event.Status = "completed"
		return map[string]any{"skill": skill, "instructions": instructions}, &event
	case updateHelpPlanTool:
		rawSteps, ok := arguments["steps"].([]any)
		if !ok || len(rawSteps) < 1 || len(rawSteps) > 5 {
			return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
		}
		steps := make([]PlanStep, 0, len(rawSteps))
		inProgress := 0
		for _, raw := range rawSteps {
			item, ok := raw.(map[string]any)
			if !ok {
				return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
			}
			title, _ := item["title"].(string)
			status, _ := item["status"].(string)
			title = strings.TrimSpace(title)
			if title == "" || len([]rune(title)) > 100 ||
				(status != "pending" && status != "in_progress" && status != "completed") {
				return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
			}
			if status == "in_progress" {
				inProgress++
			}
			steps = append(steps, PlanStep{Title: title, Status: status})
		}
		if inProgress > 1 {
			return map[string]any{"error": "工作计划同时最多有一个进行中的步骤"}, nil
		}
		if planStepsEqual(steps, h.Plan) {
			return map[string]any{"message": "工作计划无变化"}, nil
		}
		h.Plan = steps
		event := NewProgressEvent("plan")
		event.Plan = append([]PlanStep(nil), steps...)
		return map[string]any{"message": "工作计划已更新"}, &event
	default:
		return map[string]any{"error": "技能或计划参数无效，请按照工具定义提交"}, nil
	}
}

func planStepsEqual(left, right []PlanStep) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
