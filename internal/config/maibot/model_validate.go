package maibot

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"
)

var modelPriceTimePattern = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidateModelConfig 对齐 MaiBot ModelConfig.model_post_init（ApplyUpgrades 之后调用）
//
// 只保留会破坏结构唯一性的硬错误；不完整配置在启动和 WebUI 中仅告警，调用时再拒绝
func ValidateModelConfig(file ModelConfigFile, log *zap.Logger) error {
	for _, msg := range ModelConfigWarnings(file) {
		validationWarn(log, msg)
	}

	providers := make(map[string]APIProvider, len(file.ApiProviders))
	for _, p := range file.ApiProviders {
		name := strings.TrimSpace(p.Name)
		if _, dup := providers[name]; dup {
			return fmt.Errorf("api_providers 名称重复: %q", name)
		}
		providers[name] = p
	}

	modelNames := make(map[string]struct{}, len(file.Models))
	for _, m := range file.Models {
		name := strings.TrimSpace(m.Name)
		if _, dup := modelNames[name]; dup {
			return fmt.Errorf("models 名称重复: %q", name)
		}
		modelNames[name] = struct{}{}
		if strings.TrimSpace(m.ModelIdentifier) == "" {
			return fmt.Errorf("模型 %q 缺少 model_identifier", name)
		}
		if err := validateModelInfo(name, m); err != nil {
			return err
		}
	}
	return nil
}

// ModelConfigWarnings 收集不完整但允许启动的模型配置问题
func ModelConfigWarnings(file ModelConfigFile) []string {
	warnings := make([]string, 0)
	if len(file.Models) == 0 {
		warnings = append(warnings, "模型列表为空，请在 WebUI 添加模型")
	}
	if len(file.ApiProviders) == 0 {
		warnings = append(warnings, "供应商列表为空，请在 WebUI 添加供应商")
	}
	providers := make(map[string]struct{}, len(file.ApiProviders))
	for _, p := range file.ApiProviders {
		name := strings.TrimSpace(p.Name)
		providers[name] = struct{}{}
		warnings = append(warnings, ProviderConfigurationErrors(name, p)...)
	}
	modelNames := make(map[string]struct{}, len(file.Models))
	for _, m := range file.Models {
		name := strings.TrimSpace(m.Name)
		modelNames[name] = struct{}{}
		providerName := strings.TrimSpace(m.ApiProvider)
		if providerName == "" {
			warnings = append(warnings, fmt.Sprintf("模型 %q 未配置 api_provider", name))
		} else if _, ok := providers[providerName]; !ok {
			warnings = append(warnings, fmt.Sprintf("模型 %q 引用的 api_provider %q 未定义", name, providerName))
		}
	}
	for _, entry := range taskModelListEntries(file.ModelTaskConfig) {
		for _, raw := range entry.list {
			modelName := strings.TrimSpace(raw)
			if modelName == "" {
				continue
			}
			if _, ok := modelNames[modelName]; !ok {
				warnings = append(warnings, fmt.Sprintf("model_task_config.%s.model_list 引用了未定义的模型 %q", entry.task, modelName))
			}
		}
	}
	if !taskHasModelList(file.ModelTaskConfig.Vlm.ModelList) {
		warnings = append(warnings, "未配置视觉识图模型，部分图片理解可能受限，请在webui或model_config中配置")
	}
	sort.Strings(warnings)
	return warnings
}

type taskModelListEntry struct {
	task string
	list []string
}

func taskModelListEntries(mtc ModelTaskConfig) []taskModelListEntry {
	return []taskModelListEntry{
		{task: "web_search", list: mtc.WebSearch.ModelList},
		{task: "replyer", list: mtc.Replyer.ModelList},
		{task: "planner", list: mtc.Planner.ModelList},
		{task: "memory", list: mtc.Memory.ModelList},
		{task: "utils", list: mtc.Utils.ModelList},
		{task: "learner", list: mtc.Learner.ModelList},
		{task: "fast_model", list: mtc.FastModel.ModelList},
		{task: "emoji", list: mtc.Emoji.ModelList},
		{task: "vlm", list: mtc.Vlm.ModelList},
		{task: "voice", list: mtc.Voice.ModelList},
		{task: "embedding", list: mtc.Embedding.ModelList},
		{task: "image_embedding", list: mtc.ImageEmbedding.ModelList},
	}
}

func taskHasModelList(list []string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

func ProviderConfigurationErrors(name string, p APIProvider) []string {
	authType := strings.TrimSpace(strings.ToLower(p.AuthType))
	if authType == "" {
		authType = "bearer"
	}
	clientType := strings.TrimSpace(strings.ToLower(p.ClientType))
	if clientType == "" {
		clientType = "openai"
	}
	errors := make([]string, 0)
	if strings.TrimSpace(p.BaseUrl) == "" && clientType != "gemini" {
		errors = append(errors, fmt.Sprintf("api_provider %q 未配置 base_url", name))
	}
	if strings.TrimSpace(p.Name) == "" {
		errors = append(errors, "api_provider 名称不能为空")
	}
	switch authType {
	case "bearer", "header", "query", "none":
	default:
		errors = append(errors, fmt.Sprintf("api_provider %q 不支持的 auth_type：%s", name, p.AuthType))
	}
	if authType == "header" && strings.TrimSpace(p.AuthHeaderName) == "" {
		errors = append(errors, fmt.Sprintf("api_provider %q：auth_type=header 时 auth_header_name 不能为空", name))
	}
	if authType == "query" && strings.TrimSpace(p.AuthQueryName) == "" {
		errors = append(errors, fmt.Sprintf("api_provider %q：auth_type=query 时 auth_query_name 不能为空", name))
	}
	return errors
}

// validateModelInfo 对齐 ModelInfo.model_post_init：标识、价格时段格式与跨午夜重叠
func validateModelInfo(modelName string, m ModelInfo) error {
	if strings.TrimSpace(m.ModelIdentifier) == "" {
		return fmt.Errorf("模型 %q 缺少 model_identifier", modelName)
	}
	type interval struct {
		start string
		end   string
		index int
	}
	intervals := make([]interval, 0, len(m.PricePeriods))
	for index, period := range m.PricePeriods {
		if !modelPriceTimePattern.MatchString(period.StartTime) {
			return fmt.Errorf("模型 %q 价格时段 %d 的开始时间格式非法: %q", modelName, index+1, period.StartTime)
		}
		if !modelPriceTimePattern.MatchString(period.EndTime) {
			return fmt.Errorf("模型 %q 价格时段 %d 的结束时间格式非法: %q", modelName, index+1, period.EndTime)
		}
		if math.IsNaN(period.PriceIn) || math.IsInf(period.PriceIn, 0) || period.PriceIn < 0 {
			return fmt.Errorf("模型 %q 价格时段 %d 的 price_in 必须是非负有限数", modelName, index+1)
		}
		if math.IsNaN(period.PriceOut) || math.IsInf(period.PriceOut, 0) || period.PriceOut < 0 {
			return fmt.Errorf("模型 %q 价格时段 %d 的 price_out 必须是非负有限数", modelName, index+1)
		}
		if math.IsNaN(period.CachePriceIn) || math.IsInf(period.CachePriceIn, 0) || period.CachePriceIn < 0 {
			return fmt.Errorf("模型 %q 价格时段 %d 的 cache_price_in 必须是非负有限数", modelName, index+1)
		}
		if period.StartTime == period.EndTime {
			return fmt.Errorf("模型 %q 价格时段 %d 的开始时间和结束时间不能相同", modelName, index+1)
		}
		if period.StartTime < period.EndTime {
			intervals = append(intervals, interval{start: period.StartTime, end: period.EndTime, index: index + 1})
			continue
		}
		intervals = append(intervals, interval{start: period.StartTime, end: "24:00", index: index + 1})
		if period.EndTime != "00:00" {
			intervals = append(intervals, interval{start: "00:00", end: period.EndTime, index: index + 1})
		}
	}
	sort.SliceStable(intervals, func(i, j int) bool {
		if intervals[i].start != intervals[j].start {
			return intervals[i].start < intervals[j].start
		}
		return intervals[i].end < intervals[j].end
	})
	for index := 1; index < len(intervals); index++ {
		if intervals[index].start < intervals[index-1].end {
			return fmt.Errorf("模型 %q 价格时段 %d 与 %d 重叠", modelName, intervals[index-1].index, intervals[index].index)
		}
	}
	return nil
}

func validationWarn(log *zap.Logger, msg string) {
	if log == nil {
		log = zap.L()
	}
	if log == nil {
		return
	}
	log.Warn(msg)
}
