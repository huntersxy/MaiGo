package llm

import "MaiGo/internal/config/maibot"

// HasTaskModels 判断 model_task_config 中该任务自身是否配置了非空 model_list（不含 fast_model→utils 继用；对齐 MaiBot _is_vlm_task_configured）
func (r *Router) HasTaskModels(task TaskName) bool {
	if r == nil || r.file == nil {
		return false
	}
	cfg, err := taskConfigByName(r.file.ModelTaskConfig, task)
	if err != nil {
		return false
	}
	return hasModelList(cfg)
}

// ModelConfigFile 返回当前绑定的 model_config 快照（只读引用，热更由 Router 内部替换）
func (r *Router) ModelConfigFile() *maibot.ModelConfigFile {
	if r == nil {
		return nil
	}
	return r.file
}
