package llm

// 结构化输出与任务名的别名。
//
// 定义在 contract 包：response_format 与 effective_params 被「客户端实现」
// 与「快照/统计层」共同使用，任务名类型也被两侧引用（对应 Python
// src/llm_models/utils_model.py 的任务枚举 + payload_content/resp_format.py）。

import "MaiGo/internal/llm_models/contract"

type (
	// TaskName 模型任务名
	TaskName = contract.TaskName
	// ResponseFormatSpec 结构化输出的规范化描述
	ResponseFormatSpec = contract.ResponseFormatSpec
)

var (
	// parseResponseFormat 解析 ResponseFormat 配置
	parseResponseFormat = contract.ParseResponseFormat
	// parseResponseFormatKind 按类型名解析
	parseResponseFormatKind = contract.ParseResponseFormatKind
	// chatResponseFormat 转成 Chat Completions 的 response_format
	chatResponseFormat = contract.ChatResponseFormat
	// applyGeminiResponseFormat 写入 Gemini generationConfig
	applyGeminiResponseFormat = contract.ApplyGeminiResponseFormat
	// chatScalarString 转成标量字符串
	chatScalarString = contract.ChatScalarString

	// resolveEffectiveParams 解析最终生效的请求参数
	resolveEffectiveParams = contract.ResolveEffectiveParams
	// effectiveSendTemperature 是否发送 temperature
	effectiveSendTemperature = contract.EffectiveSendTemperature
	// effectiveTemperatureValue 生效的 temperature
	effectiveTemperatureValue = contract.EffectiveTemperatureValue
	// effectiveMaxTokensValue 生效的 max_tokens
	effectiveMaxTokensValue = contract.EffectiveMaxTokensValue
	// taskConfigEffectiveParameters 任务默认参数
	taskConfigEffectiveParameters = contract.TaskConfigEffectiveParameters
	// withTaskEffectiveParameters 应用任务参数
	withTaskEffectiveParameters = contract.WithTaskEffectiveParameters
	// withTaskEffectiveParametersFor 按任务名应用参数
	withTaskEffectiveParametersFor = contract.WithTaskEffectiveParametersFor
	// allTaskNames 全部任务名
	allTaskNames = contract.AllTaskNames
	// AllTaskNames 全部任务名
	AllTaskNames = contract.AllTaskNames
	// NormalizeTaskName 校验并规范化任务名
	NormalizeTaskName = contract.NormalizeTaskName
	// normalizeTaskName 归一任务名
	normalizeTaskName = contract.NormalizeTaskName
	// trimTask 去除任务名首尾空白
	trimTask = contract.TrimTask
)

// 任务名常量转发
const (
	TaskWebSearch      = contract.TaskWebSearch
	TaskReplyer        = contract.TaskReplyer
	TaskPlanner        = contract.TaskPlanner
	TaskMemory         = contract.TaskMemory
	TaskUtils          = contract.TaskUtils
	TaskLearner        = contract.TaskLearner
	TaskFastModel      = contract.TaskFastModel
	TaskVLM            = contract.TaskVLM
	TaskVoice          = contract.TaskVoice
	TaskEmbedding      = contract.TaskEmbedding
	TaskImageEmbedding = contract.TaskImageEmbedding
)

// responseFormatSnapshot 序列化为 MaiBot RespFormat.to_dict 形状
var responseFormatSnapshot = contract.ResponseFormatSnapshot
