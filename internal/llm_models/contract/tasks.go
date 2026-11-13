package contract

import "errors"

// ErrUnknownTask 未知的任务名
var ErrUnknownTask = errors.New("未知的 LLM 任务名")

// TaskName 对应 MaiBot model_config.toml 的 model_task_config 字段名
type TaskName string

const (
	TaskWebSearch      TaskName = "web_search"
	TaskReplyer        TaskName = "replyer"
	TaskPlanner        TaskName = "planner"
	TaskMemory         TaskName = "memory"
	TaskUtils          TaskName = "utils"
	TaskLearner        TaskName = "learner"
	TaskFastModel      TaskName = "fast_model"
	TaskEmoji          TaskName = "emoji"
	TaskVLM            TaskName = "vlm"
	TaskVoice          TaskName = "voice"
	TaskEmbedding      TaskName = "embedding"
	TaskImageEmbedding TaskName = "image_embedding"
)

// AllTaskNames 返回 model_task_config 下全部 TaskConfig 类任务名（不含 web_search 的异构配置）
func AllTaskNames() []TaskName {
	return []TaskName{
		TaskReplyer,
		TaskPlanner,
		TaskMemory,
		TaskUtils,
		TaskLearner,
		TaskFastModel,
		TaskEmoji,
		TaskVLM,
		TaskVoice,
		TaskEmbedding,
		TaskImageEmbedding,
	}
}

// NormalizeTaskName 校验并规范化任务名；空串视为 replyer（主聊天）
func NormalizeTaskName(name string) (TaskName, error) {
	n := TaskName(trimTask(name))
	if n == "" {
		return TaskReplyer, nil
	}
	for _, known := range append(AllTaskNames(), TaskWebSearch) {
		if n == known {
			return n, nil
		}
	}
	return "", ErrUnknownTask
}

// TrimTask 去除任务名首尾空白
func TrimTask(s string) string { return trimTask(s) }

func trimTask(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
