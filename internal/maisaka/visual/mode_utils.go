package visual

import (
	"strings"

	"go.uber.org/zap"
)

// ModelInfo 模型视觉能力投影
type ModelInfo struct {
	Name   string
	Visual bool
}

// ResolveEnableVisualPlanner 根据 planner 配置的模型视觉能力决定是否启用视觉消息
func ResolveEnableVisualPlanner(plannerModels []string, models []ModelInfo) bool {
	taskModels := NormalizeModelNames(plannerModels)
	modelsByName := make(map[string]ModelInfo, len(models))
	for _, model := range models {
		modelsByName[model.Name] = model
	}
	missing := make([]string, 0, 2)
	nonVisual := make([]string, 0, 2)
	for _, modelName := range taskModels {
		model, ok := modelsByName[modelName]
		if !ok {
			missing = append(missing, modelName)
			continue
		}
		if !model.Visual {
			nonVisual = append(nonVisual, modelName)
		}
	}
	if len(missing) > 0 {
		zap.L().Warn("自动选择视觉模式时发现 planner 任务存在未定义模型，将退化为纯文本 planner",
			zap.Strings("models", missing))
		return false
	}
	return len(taskModels) > 0 && len(nonVisual) == 0
}

// NormalizeModelNames 过滤空模型名
func NormalizeModelNames(modelNames []string) []string {
	normalized := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if trimmed := strings.TrimSpace(modelName); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return normalized
}
