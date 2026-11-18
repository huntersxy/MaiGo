package execution

// FeatureToggles Go 平台特有功能开关；所有字段默认 false（对齐 Python 行为）
type FeatureToggles struct {
	NativeSessionScheduler bool `json:"native_session_scheduler" toml:"native_session_scheduler"`
	NativeMessageCommitter bool `json:"native_message_committer" toml:"native_message_committer"`
	NativeEventDispatcher  bool `json:"native_event_dispatcher" toml:"native_event_dispatcher"`
	NativeWriteCoordinator bool `json:"native_write_coordinator" toml:"native_write_coordinator"`
	AsyncMemoryWriteback   bool `json:"async_memory_writeback" toml:"async_memory_writeback"`
	ParallelLLMCalls       bool `json:"parallel_llm_calls" toml:"parallel_llm_calls"`
	BatchDBWrites          bool `json:"batch_db_writes" toml:"batch_db_writes"`
	SQLiteReadPool         bool `json:"sqlite_read_pool" toml:"sqlite_read_pool"`
}

// FeatureInfo 单个功能开关的描述信息，供 API 与开发者页面返回
type FeatureInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Description string `json:"description"`
	PythonState string `json:"python_state"`
	GoState     string `json:"go_state"`
	// Implemented 表示该开关是否已经接入运行时；未接入时页面只展示规划状态。
	Implemented bool `json:"implemented"`
	// Phase 分阶段实施编号，0 表示未排期。
	Phase   int  `json:"phase"`
	Enabled bool `json:"enabled"`
}

// DefaultFeatureToggles 返回全部关闭的默认开关（完全对齐 Python 行为）
func DefaultFeatureToggles() FeatureToggles {
	return FeatureToggles{}
}

// FeatureDescriptions 返回所有功能开关的静态元数据与双模式对照。
//
// 只有已经接入运行时的功能才会把 Implemented 置为 true；其余功能按阶段逐步接入，
// 页面只展示规划状态。
func FeatureDescriptions() []FeatureInfo {
	return []FeatureInfo{
		{
			Key:         "native_session_scheduler",
			Name:        "会话并发调度",
			Category:    "调度",
			Description: "在 native 模式且开启本开关时，按会话建立 FIFO 邮箱，跨会话交给有界工作池处理；同一会话内保持严格顺序。",
			PythonState: "兼容：按 Python 单事件循环的节奏串行排队。",
			GoState:     "原生：跨会话并行，单会话 FIFO 保序。",
			Implemented: true,
			Phase:       2,
		},
		{
			Key:         "native_message_committer",
			Name:        "入站提交定序",
			Category:    "调度",
			Description: "同一会话的入站提交按到达序号定序，跨会话并发执行；关闭后按 Python 的独立任务方式就地提交。",
			PythonState: "兼容：每条入站事件作为独立任务直接执行，无队列与序号。",
			GoState:     "原生：会话内按序号保序，会话间并行，不占用调用方协程。",
			Implemented: true,
			Phase:       2,
		},
		{
			Key:         "native_event_dispatcher",
			Name:        "事件异步分发",
			Category:    "事件",
			Description: "广播事件与插件 Hook 异步并发分发，拦截型事件仍保持同步保序链。",
			PythonState: "兼容：事件总线与插件 Hook 全局串行保序。",
			GoState:     "原生：广播与 Hook 并发分发，单插件内保序。",
			Phase:       3,
		},
		{
			Key:         "native_write_coordinator",
			Name:        "写入协调",
			Category:    "存储",
			Description: "协调并发写入，合并同一会话内的多次写入，降低 SQLite 单写者竞争。",
			PythonState: "兼容：沿用 Python 的事务边界逐次提交。",
			GoState:     "原生：会话内写合并，带幂等键与 Journal 保护。",
			Phase:       3,
		},
		{
			Key:         "async_memory_writeback",
			Name:        "异步记忆回写",
			Category:    "记忆",
			Description: "记忆提取与持久化改为异步批处理，带 Journal 重试与幂等去重，减少主流程阻塞。",
			PythonState: "兼容：与 Python 相同的同步等待触发时机。",
			GoState:     "原生：后台批量提取与写回，主聊天流程不等待。",
			Phase:       4,
		},
		{
			Key:         "parallel_llm_calls",
			Name:        "跨会话 LLM 并行",
			Category:    "推理",
			Description: "不同会话可以并发发起 LLM 请求，会话内部仍按回合串行，降低多用户等待。",
			PythonState: "兼容：与 Python 相同的排队并发模型。",
			GoState:     "原生：跨会话并行推理，会话内按回合串行。",
			Phase:       2,
		},
		{
			Key:         "batch_db_writes",
			Name:        "批量数据库写入",
			Category:    "存储",
			Description: "把短时间内的消息记录与日志写入聚合成批量事务，降低磁盘 I/O 与 WAL 膨胀。",
			PythonState: "兼容：单条消息单次写回事务。",
			GoState:     "原生：缓冲队列批量提交，带崩溃安全重放。",
			Phase:       3,
		},
		{
			Key:         "sqlite_read_pool",
			Name:        "SQLite 只读连接池",
			Category:    "存储",
			Description: "保留单写者互斥，同时用独立只读连接池分担统计、历史查询与检索压力。",
			PythonState: "兼容：单连接对齐读写。",
			GoState:     "原生：读写分离，只读查询并发执行。",
			Phase:       3,
		},
	}
}

// ToInfoList 将当前开关值与静态描述合并，生成完整的功能信息列表
func (t FeatureToggles) ToInfoList() []FeatureInfo {
	descs := FeatureDescriptions()
	values := t.toMap()
	for i := range descs {
		if v, ok := values[descs[i].Key]; ok {
			descs[i].Enabled = v
		}
	}
	return descs
}

// IsKnownFeatureKey 判断键名是否为已定义的功能开关。
func IsKnownFeatureKey(key string) bool {
	for _, desc := range FeatureDescriptions() {
		if desc.Key == key {
			return true
		}
	}
	return false
}

// IsFeatureImplemented 判断该功能是否已经接入运行时。
func IsFeatureImplemented(key string) bool {
	for _, desc := range FeatureDescriptions() {
		if desc.Key == key {
			return desc.Implemented
		}
	}
	return false
}

// UnimplementedEnabledKeys 返回被请求开启、但尚未接入运行时的功能键。
func UnimplementedEnabledKeys(features map[string]bool) []string {
	var keys []string
	for key, enabled := range features {
		if enabled && IsKnownFeatureKey(key) && !IsFeatureImplemented(key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// toMap 将开关结构体转为 key→bool 映射
func (t FeatureToggles) toMap() map[string]bool {
	return map[string]bool{
		"native_session_scheduler": t.NativeSessionScheduler,
		"native_message_committer": t.NativeMessageCommitter,
		"native_event_dispatcher":  t.NativeEventDispatcher,
		"native_write_coordinator": t.NativeWriteCoordinator,
		"async_memory_writeback":   t.AsyncMemoryWriteback,
		"parallel_llm_calls":       t.ParallelLLMCalls,
		"batch_db_writes":          t.BatchDBWrites,
		"sqlite_read_pool":         t.SQLiteReadPool,
	}
}

// SetByKey 按键名设置单个开关，返回 false 表示键名不存在
func (t *FeatureToggles) SetByKey(key string, enabled bool) bool {
	switch key {
	case "native_session_scheduler":
		t.NativeSessionScheduler = enabled
	case "native_message_committer":
		t.NativeMessageCommitter = enabled
	case "native_event_dispatcher":
		t.NativeEventDispatcher = enabled
	case "native_write_coordinator":
		t.NativeWriteCoordinator = enabled
	case "async_memory_writeback":
		t.AsyncMemoryWriteback = enabled
	case "parallel_llm_calls":
		t.ParallelLLMCalls = enabled
	case "batch_db_writes":
		t.BatchDBWrites = enabled
	case "sqlite_read_pool":
		t.SQLiteReadPool = enabled
	default:
		return false
	}
	return true
}

// MergeFromMap 从 JSON 反序列化的 map 合并开关值
func (t *FeatureToggles) MergeFromMap(m map[string]bool) {
	for key, val := range m {
		t.SetByKey(key, val)
	}
}

// Enabled 返回指定功能键当前是否开启。
func (t FeatureToggles) Enabled(key string) bool {
	return t.toMap()[key]
}

// LoadFeatureToggles 从 config/runtime.toml 的 [features] 读取功能开关。
// 文件不存在时返回默认值；文件损坏时返回错误，避免页面把坏配置静默当成全关。
func LoadFeatureToggles(instanceDir string) (FeatureToggles, error) {
	_, toggles, err := LoadRuntimeFile(instanceDir)
	return toggles, err
}

// SaveFeatureToggles 将功能开关写入 config/runtime.toml 的 [features] 节，保留其他内容。
func SaveFeatureToggles(instanceDir string, toggles FeatureToggles) error {
	_, _, err := UpdateRuntime(instanceDir, func(_ *Profile, current *FeatureToggles) error {
		*current = toggles
		return nil
	})
	return err
}
