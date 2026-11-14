package execution

import (
	"os"
	"path/filepath"

	webuiutils "maibot-go/internal/webui/utils"

	"github.com/pelletier/go-toml/v2"
)

// FeatureToggles Go 平台特有功能开关；所有字段默认 false（对齐 Python 行为）
type FeatureToggles struct {
	NativeSessionScheduler bool `json:"native_session_scheduler" toml:"native_session_scheduler"`
	NativeEventDispatcher  bool `json:"native_event_dispatcher" toml:"native_event_dispatcher"`
	NativeWriteCoordinator bool `json:"native_write_coordinator" toml:"native_write_coordinator"`
	AsyncMemoryWriteback   bool `json:"async_memory_writeback" toml:"async_memory_writeback"`
	ParallelLLMCalls       bool `json:"parallel_llm_calls" toml:"parallel_llm_calls"`
	BatchDBWrites          bool `json:"batch_db_writes" toml:"batch_db_writes"`
}

// FeatureInfo 单个功能开关的描述信息，供 API 返回
type FeatureInfo struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Category    string `json:"category"`
}

// DefaultFeatureToggles 返回全部关闭的默认开关
func DefaultFeatureToggles() FeatureToggles {
	return FeatureToggles{}
}

// FeatureDescriptions 返回所有功能开关的静态元数据
func FeatureDescriptions() []FeatureInfo {
	return []FeatureInfo{
		{
			Key:         "native_session_scheduler",
			Name:        "原生会话调度器",
			Description: "使用 Go 协程池替代 Python 线程池进行会话调度，提升并发处理能力",
			Category:    "调度",
		},
		{
			Key:         "native_event_dispatcher",
			Name:        "原生事件分发器",
			Description: "使用 Go channel 替代 Python asyncio 事件分发，降低事件处理延迟",
			Category:    "调度",
		},
		{
			Key:         "native_write_coordinator",
			Name:        "原生写入协调器",
			Description: "使用 Go 原生锁机制协调并发写入，减少锁竞争开销",
			Category:    "存储",
		},
		{
			Key:         "async_memory_writeback",
			Name:        "异步记忆回写",
			Description: "记忆数据异步批量回写数据库，降低主流程 I/O 阻塞",
			Category:    "存储",
		},
		{
			Key:         "parallel_llm_calls",
			Name:        "并行 LLM 调用",
			Description: "允许同时向多个 LLM 端点发起请求，缩短多模型场景响应时间",
			Category:    "推理",
		},
		{
			Key:         "batch_db_writes",
			Name:        "批量数据库写入",
			Description: "合并短时间内的多次数据库写入为单次事务，提升写入吞吐量",
			Category:    "存储",
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

// toMap 将开关结构体转为 key→bool 映射
func (t FeatureToggles) toMap() map[string]bool {
	return map[string]bool{
		"native_session_scheduler": t.NativeSessionScheduler,
		"native_event_dispatcher":  t.NativeEventDispatcher,
		"native_write_coordinator": t.NativeWriteCoordinator,
		"async_memory_writeback":   t.AsyncMemoryWriteback,
		"parallel_llm_calls":       t.ParallelLLMCalls,
		"batch_db_writes":          t.BatchDBWrites,
	}
}

// SetByKey 按键名设置单个开关，返回 false 表示键名不存在
func (t *FeatureToggles) SetByKey(key string, enabled bool) bool {
	switch key {
	case "native_session_scheduler":
		t.NativeSessionScheduler = enabled
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

// LoadFeatureToggles 从 config/runtime.toml 的 [features] 读取功能开关
func LoadFeatureToggles(instanceDir string) FeatureToggles {
	toggles := DefaultFeatureToggles()
	path := RuntimeTomlPath(instanceDir)

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return toggles
	}

	var doc struct {
		Features FeatureToggles `toml:"features"`
	}
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return toggles
	}
	return doc.Features
}

// SaveFeatureToggles 将功能开关写入 config/runtime.toml 的 [features] 节，保留其他节
func SaveFeatureToggles(instanceDir string, toggles FeatureToggles) error {
	path := RuntimeTomlPath(instanceDir)

	// 读取现有文档或创建空文档
	doc, err := webuiutils.ReadTOMLDocument(path)
	if err != nil {
		doc = make(map[string]any)
	}

	// 更新 features 节
	doc["features"] = map[string]any{
		"native_session_scheduler": toggles.NativeSessionScheduler,
		"native_event_dispatcher":  toggles.NativeEventDispatcher,
		"native_write_coordinator": toggles.NativeWriteCoordinator,
		"async_memory_writeback":   toggles.AsyncMemoryWriteback,
		"parallel_llm_calls":       toggles.ParallelLLMCalls,
		"batch_db_writes":          toggles.BatchDBWrites,
	}

	return webuiutils.WriteTOMLDocument(path, doc)
}
