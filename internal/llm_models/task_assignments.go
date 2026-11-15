package llm

import "MaiGo/internal/config/maibot"

// taskAssignmentSources 对齐 MaiBot model_task_config 的 TOML 任务键与 model_list 来源
// 遥测上报用原始配置，不做 fast_model/learner 到 utils 的继用回退
var taskAssignmentSources = []struct {
	name string
	list func(mtc maibot.ModelTaskConfig) []string
}{
	{"web_search", func(mtc maibot.ModelTaskConfig) []string { return mtc.WebSearch.ModelList }},
	{"replyer", func(mtc maibot.ModelTaskConfig) []string { return mtc.Replyer.ModelList }},
	{"planner", func(mtc maibot.ModelTaskConfig) []string { return mtc.Planner.ModelList }},
	{"memory", func(mtc maibot.ModelTaskConfig) []string { return mtc.Memory.ModelList }},
	{"utils", func(mtc maibot.ModelTaskConfig) []string { return mtc.Utils.ModelList }},
	{"learner", func(mtc maibot.ModelTaskConfig) []string { return mtc.Learner.ModelList }},
	{"fast_model", func(mtc maibot.ModelTaskConfig) []string { return mtc.FastModel.ModelList }},
	{"emoji", func(mtc maibot.ModelTaskConfig) []string { return mtc.Emoji.ModelList }},
	{"vlm", func(mtc maibot.ModelTaskConfig) []string { return mtc.Vlm.ModelList }},
	{"voice", func(mtc maibot.ModelTaskConfig) []string { return mtc.Voice.ModelList }},
	{"embedding", func(mtc maibot.ModelTaskConfig) []string { return mtc.Embedding.ModelList }},
	{"image_embedding", func(mtc maibot.ModelTaskConfig) []string { return mtc.ImageEmbedding.ModelList }},
}

// ModelTaskAssignments 返回 model_task_config 各任务自身配置的 model_list（对齐 MaiBot 遥测 model_task_assignment）
func (r *Router) ModelTaskAssignments() map[string][]string {
	if r == nil || r.file == nil {
		return nil
	}
	out := make(map[string][]string, len(taskAssignmentSources))
	for _, source := range taskAssignmentSources {
		out[source.name] = nonEmptyStrings(source.list(r.file.ModelTaskConfig))
	}
	return out
}
