// Package contract 是 LLM 层的共享数据契约。
//
// 对齐 MaiBot src/llm_models/model_client/base_client.py：Python 把 ModelConfig、
// UsageRecord、GenerationTrace、APIResponse 等放在 model_client 基类里，供
// 「客户端实现」与「快照/统计层」双向引用——Python 靠运行期延迟导入绕开循环，
// Go 编译期不允许，因此把这些纯数据契约定为独立包，两侧都向下依赖它。
//
// 本包不引用任何上层类型，也不含业务逻辑，只保证「同一份字段定义只有一处」。
package contract

import (
	"time"
)

type ModelConfig struct {
	ClientType      string // MaiBot api_providers.client_type 或 model_profiles.provider
	ProviderName    string // MaiBot api_providers[].name，图片嵌入指纹的 api_provider；数据库档案无此字段时为空
	APIKey          string
	BaseURL         string
	Model           string
	ThinkingType    string
	ReasoningEffort string
	ExtraParams     map[string]any // models[].extra_params，嵌入维度等

	// 以下字段对齐 MaiBot api_providers 的解析模式与组织标识
	ReasoningParseMode    string // reasoning_parse_mode：auto/native/think_tag/none
	ToolArgumentParseMode string // tool_argument_parse_mode：auto/strict/repair/double_decode
	Organization          string // Organization，OpenAI SDK 的 OpenAI-Organization 头
	Project               string // Project，OpenAI SDK 的 OpenAI-Project 头

	// 以下字段对齐 MaiBot api_providers 的请求级编排与鉴权配置
	MaxRetry int // max_retry：含首次请求的总尝试次数；未显式配置时按默认 3
	// MaxRetrySet 区分未配置（默认 3）与显式 0（不重试）
	MaxRetrySet      bool
	RetryInterval    time.Duration // retry_interval
	Timeout          time.Duration // timeout：单次 HTTP 请求超时，0 表示不限
	AuthType         string
	AuthHeaderName   string
	AuthHeaderPrefix string
	// AuthHeaderPrefixSet 区分未配置（默认 Bearer）与显式空串（裸 key）
	AuthHeaderPrefixSet bool
	AuthQueryName      string
	DefaultHeaders     map[string]string
	DefaultQuery       map[string]string

	// PluginID 插件 Provider 的归属插件；留空时由 PluginProviderResolver 解析
	PluginID string

	// 以下字段对应 MaiBot models[]，用于插件 model_info 快照与请求默认参数
	ModelDisplayName string // models[].name
	ModelType        string // models[].model_type
	Visual           bool   // models[].visual
	ForceStreamMode  bool   // models[].force_stream_mode
	SendTemperature  bool   // models[].send_temperature
	ModelTemperature any    // models[].temperature
	ModelMaxTokens   any    // models[].max_tokens
	// SendTemperatureSet 区分 TOML 未写 send_temperature（Python 默认 true）与显式 false
	SendTemperatureSet bool
	// TaskTemperature/TaskMaxTokens 是 model_task_config 的任务默认档（Python TaskConfig 默认 0.7/4096）
	TaskTemperature *float64
	TaskMaxTokens   *int

	// ModelListEndpoint 对齐 api_providers[].model_list_endpoint
	ModelListEndpoint string

	// TaskName/RequestType/SessionID 只用于失败快照、缓存统计与日志，不参与协议指纹或请求
	TaskName    string
	RequestType string
	SessionID   string

	// PriceIn/CachePriceIn/PriceOut 对齐 MaiBot models[] 的每百万 token 价格（元）
	PriceIn      float64
	CachePriceIn float64
	PriceOut     float64
	// PricePeriods 按时段覆盖基础价格，按本地 HH:MM 判断
	PricePeriods []ModelPricePeriod

	// ResponseFormat 结构化输出配置；nil 不发送，支持 "json_object"/"text"/"json_schema" 或完整对象
	ResponseFormat any
}

// ModelPricePeriod 对齐 MaiBot model_configs.ModelPricePeriod
type ModelPricePeriod struct {
	StartTime    string
	EndTime      string
	PriceIn      float64
	PriceOut     float64
	CachePriceIn float64
}
