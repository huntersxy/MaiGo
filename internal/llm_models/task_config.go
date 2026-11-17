package llm

import (
	"fmt"

	"MaiGo/internal/config/maibot"
)

// emptyTaskFallbacks 与 MaiBot utils_model.EMPTY_TASK_FALLBACKS 一致
var emptyTaskFallbacks = map[TaskName]TaskName{
	TaskFastModel: TaskUtils,
	TaskLearner:   TaskUtils,
}

// TaskConfigResult 解析 model_task_config 后的任务配置
type TaskConfigResult struct {
	Requested TaskName
	Effective TaskName
	Config    maibot.TaskConfig
}

// ResolveTaskConfig 按任务名读取 TaskConfig，并在 model_list 为空时沿继用链回退
func ResolveTaskConfig(file maibot.ModelConfigFile, task TaskName) (TaskConfigResult, error) {
	if task == TaskWebSearch {
		return TaskConfigResult{}, fmt.Errorf("web_search 使用 WebSearchTaskConfig，请用 ResolveWebSearchModels")
	}
	cfg, err := taskConfigByName(file.ModelTaskConfig, task)
	if err != nil {
		return TaskConfigResult{}, err
	}
	effective := task
	for !hasModelList(cfg) {
		fallback, ok := emptyTaskFallbacks[effective]
		if !ok {
			break
		}
		fallbackCfg, err := taskConfigByName(file.ModelTaskConfig, fallback)
		if err != nil {
			break
		}
		cfg = fallbackCfg
		effective = fallback
	}
	if task == TaskImageEmbedding && !hasModelList(cfg) {
		return TaskConfigResult{}, fmt.Errorf("图片嵌入任务未配置模型，请在 image_embedding 中指定支持图片嵌入的模型")
	}
	return TaskConfigResult{Requested: task, Effective: effective, Config: cfg}, nil
}

// ResolveWebSearchModels 返回 web_search 任务配置的 model_list
func ResolveWebSearchModels(file maibot.ModelConfigFile) []string {
	return nonEmptyStrings(file.ModelTaskConfig.WebSearch.ModelList)
}

func taskConfigByName(mtc maibot.ModelTaskConfig, task TaskName) (maibot.TaskConfig, error) {
	switch task {
	case TaskReplyer:
		return mtc.Replyer, nil
	case TaskPlanner:
		return mtc.Planner, nil
	case TaskMemory:
		return mtc.Memory, nil
	case TaskUtils:
		return mtc.Utils, nil
	case TaskLearner:
		return mtc.Learner, nil
	case TaskFastModel:
		return mtc.FastModel, nil
	case TaskEmoji:
		return mtc.Emoji, nil
	case TaskVLM:
		return mtc.Vlm, nil
	case TaskVoice:
		return mtc.Voice, nil
	case TaskEmbedding:
		return mtc.Embedding, nil
	case TaskImageEmbedding:
		return mtc.ImageEmbedding, nil
	default:
		return maibot.TaskConfig{}, errUnknownTask
	}
}

func hasModelList(cfg maibot.TaskConfig) bool {
	return len(nonEmptyStrings(cfg.ModelList)) > 0
}

func nonEmptyStrings(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		s = trimTask(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TaskModelList 解析任务配置并返回有效 model_list（含 fast_model/learner→utils 继用）
func TaskModelList(file maibot.ModelConfigFile, task TaskName) (TaskConfigResult, []string, error) {
	res, err := ResolveTaskConfig(file, task)
	if err != nil {
		return TaskConfigResult{}, nil, err
	}
	return res, nonEmptyStrings(res.Config.ModelList), nil
}

// SelectModelName 取 model_list 第一个候选（与 sequential 初始选择一致）
func SelectModelName(cfg maibot.TaskConfig) (string, bool) {
	names := nonEmptyStrings(cfg.ModelList)
	if len(names) == 0 {
		return "", false
	}
	return names[0], true
}
