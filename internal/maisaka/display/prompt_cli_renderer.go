package display

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prompt 预览相关常量
const (
	PromptPreviewImageDirName   = "prompt_imgs"
	ProviderBase64OmitThreshold = 64 * 1024
)

var supportedStructuredImageFormats = map[string]struct{}{"jpg": {}, "jpeg": {}, "png": {}, "webp": {}, "gif": {}}

// PromptPreviewRouteTarget Prompt 预览记录对应的 WebUI 路由目标
type PromptPreviewRouteTarget struct {
	RelativePath string
	Stage        string
	Session      string
	Stem         string
}

// PromptPreviewAccess Prompt 预览文件的展示入口和可直接打开的路径
type PromptPreviewAccess struct {
	Body            string
	RecordPath      string
	RecordURI       string
	PreviewWebURI   string
	ReasoningWebURI string
	Payload         map[string]any
}

// PromptCLIVisualizer 负责构建 CLI 下 prompt 展示所需组件
type PromptCLIVisualizer struct {
	BaseDir string
}

// NewPromptCLIVisualizer 创建 Prompt 可视化器
func NewPromptCLIVisualizer(baseDir string) *PromptCLIVisualizer {
	if baseDir == "" {
		baseDir = DefaultPromptPreviewLogger().BaseDir
	}
	return &PromptCLIVisualizer{BaseDir: baseDir}
}

// NormalizePreviewMetadata 规范化 Prompt 预览元数据
func NormalizePreviewMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return map[string]any{}
	}
	normalized := map[string]any{}
	modelName := ""
	if value, ok := metadata["model_name"].(string); ok {
		modelName = strings.TrimSpace(value)
	}
	if modelName == "" {
		if value, ok := metadata["model"].(string); ok {
			modelName = strings.TrimSpace(value)
		}
	}
	if modelName != "" {
		normalized["model_name"] = modelName
	}
	if value, ok := metadata["prompt_cache_reported"].(bool); ok {
		normalized["prompt_cache_reported"] = value
	}
	for _, key := range []string{"tool_choice", "request_source"} {
		if value, ok := metadata[key].(string); ok {
			normalized[key] = value
		}
	}
	if raw, ok := metadata["duration_ms"]; ok {
		if duration, ok := toFloat(raw); ok {
			normalized["duration_ms"] = roundTo(duration, 2)
		}
	}
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"} {
		if count, ok := toInt(rawValue(metadata, key)); ok {
			normalized[key] = maxInt(count, 0)
		}
	}
	if _, ok := normalized["total_tokens"]; !ok {
		promptTokens, promptOK := normalized["prompt_tokens"].(int)
		completionTokens, completionOK := normalized["completion_tokens"].(int)
		if promptOK && completionOK {
			normalized["total_tokens"] = promptTokens + completionTokens
		}
	}
	return normalized
}

// ExtractTokenMetadataFromAttempts 从已序列化的 Generation Attempt 中提取最近一次有效 Token 用量
func ExtractTokenMetadataFromAttempts(attempts []map[string]any) map[string]int {
	for index := len(attempts) - 1; index >= 0; index-- {
		attempt := attempts[index]
		if trace, ok := attempt["trace"].(map[string]any); ok {
			if result := normalizeTokenMap(trace); len(result) > 0 {
				return result
			}
		}
		wireResponse, ok := attempt["wire_response"].(map[string]any)
		if !ok {
			continue
		}
		usage, ok := wireResponse["usage"].(map[string]any)
		if !ok {
			continue
		}
		converted := map[string]any{
			"prompt_tokens":     firstValue(usage, "prompt_tokens", "input_tokens"),
			"completion_tokens": firstValue(usage, "completion_tokens", "output_tokens"),
			"total_tokens":      usage["total_tokens"],
		}
		if result := normalizeTokenMap(converted); len(result) > 0 {
			return result
		}
	}
	return map[string]int{}
}

// BuildPromptStatsText 构造 prompt 统计文本
func BuildPromptStatsText(selectedHistoryCount, builtMessageCount, promptTokens, completionTokens, totalTokens int) string {
	return fmt.Sprintf("上下文消息数量=%d 已构建消息数=%d 实际输入Token=%s 输出Token=%s 总Token=%s",
		selectedHistoryCount, builtMessageCount,
		FormatTokenCount(promptTokens), FormatTokenCount(completionTokens), FormatTokenCount(totalTokens))
}

// ResolvePreviewRouteTarget 解析预览文件对应的路由目标
func (v *PromptCLIVisualizer) ResolvePreviewRouteTarget(filePath string) (PromptPreviewRouteTarget, bool) {
	absolute, err := filepath.Abs(filePath)
	if err != nil {
		return PromptPreviewRouteTarget{}, false
	}
	relative, err := filepath.Rel(v.BaseDir, absolute)
	if err != nil || strings.HasPrefix(relative, "..") {
		return PromptPreviewRouteTarget{}, false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 3 {
		return PromptPreviewRouteTarget{}, false
	}
	stage, session := parts[0], parts[1]
	stem := strings.TrimSuffix(filepath.Base(parts[len(parts)-1]), filepath.Ext(parts[len(parts)-1]))
	if stage == "" || session == "" || stem == "" {
		return PromptPreviewRouteTarget{}, false
	}
	return PromptPreviewRouteTarget{RelativePath: filepath.ToSlash(relative), Stage: stage, Session: session, Stem: stem}, true
}

// BuildPromptPreviewWebURI 构建 WebUI 可访问的 Prompt 预览地址
func (v *PromptCLIVisualizer) BuildPromptPreviewWebURI(filePath string) string {
	target, ok := v.ResolvePreviewRouteTarget(filePath)
	if !ok {
		return BuildFileURI(filePath)
	}
	return "/api/webui/config/maisaka-prompt-preview?path=" + percentEncode(target.RelativePath)
}

// BuildPromptReasoningWebURI 构建 WebUI 推理过程页面地址
func (v *PromptCLIVisualizer) BuildPromptReasoningWebURI(filePath string, baseURL string) string {
	target, ok := v.ResolvePreviewRouteTarget(filePath)
	if !ok {
		return ""
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8000"
	}
	return baseURL + "/reasoning-process?stage=" + percentEncode(target.Stage) +
		"&session=" + percentEncode(target.Session) + "&stem=" + percentEncode(target.Stem)
}

// BuildImageFileLink 优先返回已有 data 图片路径；不存在时登记后台落盘的 prompt 图片
func (v *PromptCLIVisualizer) BuildImageFileLink(imageFormat, imageBase64 string) (string, PreviewImageAsset, bool) {
	normalizedFormat := NormalizeImageFormat(imageFormat)
	if normalizedFormat == "" {
		normalizedFormat = "bin"
	}
	content, err := base64.StdEncoding.DecodeString(imageBase64)
	if err != nil || len(content) == 0 {
		return "", PreviewImageAsset{}, false
	}
	logger := DefaultPromptPreviewLogger()
	cachePath := logger.BuildImageCachePath(normalizedFormat, content)
	asset := PreviewImageAsset{FileName: filepath.Base(cachePath), Content: content}
	return BuildFileURI(cachePath), asset, true
}

// ExtractDataURLImage 从 data URL 中提取图片格式和 Base64 内容
func ExtractDataURLImage(imageURL string) (string, string, bool) {
	normalized := strings.TrimSpace(imageURL)
	if !strings.HasPrefix(normalized, "data:image/") || !strings.Contains(normalized, ";base64,") {
		return "", "", false
	}
	parts := strings.SplitN(normalized, ";base64,", 2)
	format := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(parts[0], "data:image/")))
	if format == "" || parts[1] == "" {
		return "", "", false
	}
	return format, parts[1], true
}

// IsSupportedStructuredImageFormat 判断图片格式是否受支持
func IsSupportedStructuredImageFormat(imageFormat string) bool {
	_, ok := supportedStructuredImageFormats[NormalizeImageFormat(imageFormat)]
	return ok
}

func normalizeTokenMap(values map[string]any) map[string]int {
	result := map[string]int{}
	positive := false
	for _, key := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if count, ok := toInt(rawValue(values, key)); ok {
			result[key] = maxInt(count, 0)
			if count > 0 {
				positive = true
			}
		}
	}
	if !positive {
		return map[string]int{}
	}
	if _, ok := result["total_tokens"]; !ok {
		prompt, promptOK := result["prompt_tokens"]
		completion, completionOK := result["completion_tokens"]
		if promptOK && completionOK {
			result["total_tokens"] = prompt + completion
		}
	}
	return result
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func rawValue(values map[string]any, key string) any {
	if values == nil {
		return nil
	}
	return values[key]
}

func toInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	}
	return 0, false
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}

func roundTo(value float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	return float64(int(value*scale+0.5)) / scale
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func percentEncode(value string) string {
	replacer := strings.NewReplacer("/", "%2F", " ", "%20", "?", "%3F", "&", "%26", "=", "%3D", ":", "%3A")
	return replacer.Replace(value)
}

// EnsurePromptImageDir 确保 prompt 图片目录存在
func EnsurePromptImageDir(baseDir string) error {
	if baseDir == "" {
		baseDir = filepath.Join(RepoRoot(), "data", PromptPreviewImageDirName)
	}
	return os.MkdirAll(baseDir, 0o755)
}
