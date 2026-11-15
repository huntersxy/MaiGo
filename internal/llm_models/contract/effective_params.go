package contract

// 有效生成参数解析。
//
// 对齐 src/llm_models/utils_model.py:681-724：temperature 与 max_tokens 均按
// 「调用方显式 > models[] > extra_params 顶层 > model_task_config 任务默认」解析，
// send_temperature=false 时请求级温度置空。
//
// wire 层另循 openai_client / responses / gemini：extra_body（或 config）已含
// temperature 时由该值覆盖请求级温度，且 send_temperature=false 不剥 extra 内温度。

import (
	"encoding/json"
	"strconv"

	"MaiGo/internal/config/maibot"

	"github.com/cloudwego/eino/components/model"
)

const (
	// defaultTaskTemperature 对齐 model_configs.py:408 TaskConfig.temperature 默认 0.7
	defaultTaskTemperature = 0.7
	// defaultTaskMaxTokens 对齐 model_configs.py:398 TaskConfig.max_tokens 默认 4096
	defaultTaskMaxTokens = 4096
)

// EffectiveSendTemperature 返回模型级 send_temperature；TOML 未写时按 Python 默认 true
func EffectiveSendTemperature(cfg ModelConfig) bool {
	if cfg.SendTemperatureSet {
		return cfg.SendTemperature
	}
	return true
}

// ResolveEffectiveParams 按 Python 五档/四档把模型级与任务级默认值写入 eino options
// 调用方显式值（options 非 nil 字段）优先；send_temperature=false 时清空温度
func ResolveEffectiveParams(cfg ModelConfig, options *model.Options) {
	if options == nil {
		return
	}
	if !EffectiveSendTemperature(cfg) {
		options.Temperature = nil
	} else if options.Temperature == nil {
		if value, ok := EffectiveTemperatureValue(cfg); ok {
			converted := float32(value)
			options.Temperature = &converted
		}
	}
	if options.MaxTokens == nil {
		if value, ok := EffectiveMaxTokensValue(cfg); ok {
			converted := value
			options.MaxTokens = &converted
		}
	}
}

// EffectiveTemperatureValue 解析模型级 temperature 档位（不含调用方显式值）
func EffectiveTemperatureValue(cfg ModelConfig) (float64, bool) {
	if cfg.ModelTemperature != nil {
		if value, ok := EffectiveFloatFromAny(cfg.ModelTemperature); ok {
			return value, true
		}
	}
	if raw, ok := cfg.ExtraParams["temperature"]; ok && raw != nil {
		if value, ok := EffectiveFloatFromAny(raw); ok {
			return value, true
		}
	}
	if cfg.TaskTemperature != nil {
		return *cfg.TaskTemperature, true
	}
	return 0, false
}

// EffectiveMaxTokensValue 解析模型级 max_tokens 档位（不含调用方显式值）
func EffectiveMaxTokensValue(cfg ModelConfig) (int, bool) {
	if cfg.ModelMaxTokens != nil {
		if value, ok := EffectiveIntFromAny(cfg.ModelMaxTokens); ok {
			return value, true
		}
	}
	if raw, ok := cfg.ExtraParams["max_tokens"]; ok && raw != nil {
		if value, ok := EffectiveIntFromAny(raw); ok {
			return value, true
		}
	}
	if cfg.TaskMaxTokens != nil {
		return *cfg.TaskMaxTokens, true
	}
	return 0, false
}

// TaskConfigEffectiveParameters 返回任务默认档；字段缺省时用 Python Pydantic 默认值
func TaskConfigEffectiveParameters(taskCfg maibot.TaskConfig) (float64, int) {
	temperature := defaultTaskTemperature
	if taskCfg.TemperatureSet {
		temperature = taskCfg.Temperature
	}
	maxTokens := defaultTaskMaxTokens
	if taskCfg.MaxTokensSet {
		maxTokens = taskCfg.MaxTokens
	}
	return temperature, maxTokens
}

// WithTaskEffectiveParameters 把任务默认档写入路由配置
func WithTaskEffectiveParameters(cfg ModelConfig, taskCfg maibot.TaskConfig) ModelConfig {
	temperature, maxTokens := TaskConfigEffectiveParameters(taskCfg)
	cfg.TaskTemperature = &temperature
	cfg.TaskMaxTokens = &maxTokens
	return cfg
}

// WithTaskEffectiveParametersFor 只对 Python TaskConfig 任务写入默认档
// web_search 使用 WebSearchTaskConfig（仅 model_list），没有温度/max_tokens 默认值
func WithTaskEffectiveParametersFor(task TaskName, cfg ModelConfig, taskCfg maibot.TaskConfig) ModelConfig {
	if task == TaskWebSearch {
		return cfg
	}
	return WithTaskEffectiveParameters(cfg, taskCfg)
}

// EffectiveFloatFromAny 解析 TOML / JSON / 字符串中的浮点数
func EffectiveFloatFromAny(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// EffectiveIntFromAny 解析 TOML / JSON / 字符串中的整数
func EffectiveIntFromAny(v any) (int, bool) {
	switch value := v.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case float32:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return int(parsed), true
		}
		floatValue, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return int(floatValue), true
	case string:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
