package maisaka

// BuiltinToolConfig 返回当前会话的内置工具开关
func (r *Runtime) BuiltinToolConfig() BuiltinToolConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builtinConfig
}

// SetBuiltinToolConfig 更新当前会话的内置工具开关
func (r *Runtime) SetBuiltinToolConfig(config BuiltinToolConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builtinConfig = config
	r.builtinConfigSet = true
}

// BuiltinToolConfigSet 是否已由宿主注入内置工具开关；未注入时不按配置过滤
func (r *Runtime) BuiltinToolConfigSet() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.builtinConfigSet
}
