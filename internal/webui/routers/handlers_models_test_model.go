package webuiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/pelletier/go-toml/v2"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models"
)

const (
	modelTestToolName    = "maibot_model_test_report"
	modelTestPromptText  = "你正在执行 MaiBot WebUI 的单模型能力测试。\n测试目标：确认模型可以读取普通文本，并可以按工具定义发起 tool call。\n%s\n请必须调用工具 %s，不要只用普通文本回答。\n工具参数要求：status 填 ok，echo 填 maibot model test。"
	modelTestImageBase64 = "iVBORw0KGgoAAAANSUhEUgAAACAAAAAgCAYAAABzenr0AAAAAXNSR0IArs4c6QAAAARnQU1BAACxjwv8YQUAAAAJcEhZcwAADsMAAA7DAcdvqGQAAABBSURBVFhH7c6hDQAgDEXBDstKjMkM4GsQBIq4Js9U/Fy0PmZlkR+vAwAA2AJOL+/lAAAAAAAAAP4H3A4AAKAcsAB+y6u1cyDAxAAAAABJRU5ErkJggg=="
)

type modelTestToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type modelTestEmbeddingPair struct {
	Label      string  `json:"label"`
	Similarity float64 `json:"similarity"`
}

type modelTestResponse struct {
	Success            bool                     `json:"success"`
	ModelName          string                   `json:"model_name"`
	VisualTested       bool                     `json:"visual_tested"`
	ToolCallOK         bool                     `json:"tool_call_ok"`
	Response           string                   `json:"response"`
	Reasoning          string                   `json:"reasoning"`
	ToolCalls          []modelTestToolCall      `json:"tool_calls"`
	LatencyMS          *float64                 `json:"latency_ms"`
	Error              *string                  `json:"error"`
	PromptTokens       int                      `json:"prompt_tokens"`
	CompletionTokens   int                      `json:"completion_tokens"`
	TotalTokens        int                      `json:"total_tokens"`
	TestKind           string                   `json:"test_kind"`
	EmbeddingDimension *int                     `json:"embedding_dimension"`
	EmbeddingPairs     []modelTestEmbeddingPair `json:"embedding_pairs"`
}

// handleModelsTestModel 对应 Python model.py:162 的单模型能力测试
func (d Deps) handleModelsTestModel(w http.ResponseWriter, r *http.Request) {
	var request modelTestRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	modelName := strings.TrimSpace(request.ModelName)
	if modelName == "" {
		writeValidationError(w, "model_name 不能为空")
		return
	}
	path, ok := d.modelConfigPath()
	if !ok {
		writeInstanceDirMissing(w, r.URL.Path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "配置文件不存在"})
		return
	}
	var file maibotcfg.ModelConfigFile
	if err := toml.Unmarshal(raw, &file); err != nil {
		writeInternalError(w, fmt.Errorf("解析 model_config.toml 失败: %w", err))
		return
	}
	if _, exists := findModelInfoByName(file.Models, modelName); !exists {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "未找到模型: " + modelName})
		return
	}
	cfg, err := llm.ModelConfigFromMaiBotFile(file, modelName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	started := time.Now()
	switch cfg.ModelType {
	case "embedding":
		writeJSON(w, http.StatusOK, testTextEmbeddingModel(r.Context(), cfg, modelName, started))
	case "image_embedding":
		writeJSON(w, http.StatusOK, testImageEmbeddingModel(r.Context(), cfg, modelName, started))
	case "llm":
		writeJSON(w, http.StatusOK, testChatModel(r.Context(), cfg, modelName, modelIdentifierByName(file.Models, modelName), started))
	default:
		writeInternalError(w, fmt.Errorf("模型 %s 的 model_type 无效: %s", modelName, cfg.ModelType))
	}
}

func findModelInfoByName(models []maibotcfg.ModelInfo, name string) (maibotcfg.ModelInfo, bool) {
	for _, item := range models {
		if strings.TrimSpace(item.Name) == name {
			return item, true
		}
	}
	return maibotcfg.ModelInfo{}, false
}

func modelIdentifierByName(models []maibotcfg.ModelInfo, name string) string {
	info, _ := findModelInfoByName(models, name)
	return strings.TrimSpace(info.ModelIdentifier)
}

func testChatModel(parent context.Context, cfg llm.ModelConfig, modelName, identifier string, started time.Time) modelTestResponse {
	replyer := isReplyerTestModel(identifier)
	visual := !replyer && cfg.Visual
	result := newModelTestResponse(modelName, "chat", visual)
	if replyer {
		result.TestKind = "replyer"
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return failModelTest(result, started, err)
	}
	options := []model.Option{model.WithMaxTokens(512), model.WithTemperature(0.7)}
	if !replyer {
		tools, err := parseReplayToolDefinitions([]json.RawMessage{json.RawMessage(buildModelTestToolDefinition())})
		if err != nil {
			return failModelTest(result, started, err)
		}
		options = append(options, model.WithTools(tools))
	}
	messages := []*schema.AgenticMessage{buildModelTestMessage(replyer, visual)}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	message, err := client.Generate(ctx, messages, options...)
	if err != nil {
		return failModelTest(result, started, err)
	}
	result.LatencyMS = latencyPointer(started)
	if message != nil {
		applyModelTestMessage(&result, message)
	}
	if result.ToolCalls == nil {
		result.ToolCalls = []modelTestToolCall{}
	}
	result.ToolCallOK = false
	for _, call := range result.ToolCalls {
		if call.Name == modelTestToolName {
			result.ToolCallOK = true
			break
		}
	}
	if replyer {
		result.Success = strings.TrimSpace(result.Response) != ""
		if !result.Success {
			result.Error = stringPointer("模拟 replyer 任务未返回回复正文")
		}
		return result
	}
	result.Success = result.ToolCallOK
	if !result.Success {
		result.Error = stringPointer("模型未按要求调用测试工具 " + modelTestToolName)
	}
	return result
}

func buildModelTestMessage(replyer, visual bool) *schema.AgenticMessage {
	blocks := []*schema.ContentBlock{}
	if replyer {
		blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: "你叫麦麦，是一个说话自然、随和的群友。"}))
		blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: "小明：终于忙完了，准备出去散散步\n小红：今天阳光不错，正好出去透透气\n你准备参与这段聊天，请用日常口语简短回复一句。"}))
	} else {
		imageInstruction := "本次消息没有附带图片，请在工具参数 saw_image 中填 false。"
		if visual {
			imageInstruction = "本次消息还附带了一张测试图片，请在工具参数 saw_image 中填 true，并简要描述图片。"
		}
		prompt := fmt.Sprintf(modelTestPromptText, imageInstruction, modelTestToolName)
		blocks = append(blocks, schema.NewContentBlock(&schema.UserInputText{Text: prompt}))
		if visual {
			blocks = append(blocks, schema.NewContentBlock(&schema.UserInputImage{Base64Data: modelTestImageBase64, MIMEType: "image/png"}))
		}
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}
}

func buildModelTestToolDefinition() string {
	payload := map[string]any{
		"name":        modelTestToolName,
		"description": "回报 MaiBot WebUI 模型测试结果。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status":        map[string]any{"type": "string", "enum": []any{"ok"}, "description": "测试状态，成功时填 ok。"},
				"echo":          map[string]any{"type": "string", "description": "文本回显，固定填 maibot model test。"},
				"saw_image":     map[string]any{"type": "boolean", "description": "本次请求中是否包含并识别了测试图片。"},
				"image_summary": map[string]any{"type": "string", "description": "如果包含图片，简短描述图片内容；未包含图片时留空。"},
			},
			"required": []any{"status", "echo", "saw_image"},
		},
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func applyModelTestMessage(result *modelTestResponse, message *schema.AgenticMessage) {
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.AssistantGenText != nil:
			result.Response += block.AssistantGenText.Text
		case block.UserInputText != nil:
			result.Response += block.UserInputText.Text
		case block.Reasoning != nil:
			result.Reasoning += block.Reasoning.Text
		case block.FunctionToolCall != nil:
			arguments := map[string]any{}
			if strings.TrimSpace(block.FunctionToolCall.Arguments) != "" {
				_ = json.Unmarshal([]byte(block.FunctionToolCall.Arguments), &arguments)
			}
			result.ToolCalls = append(result.ToolCalls, modelTestToolCall{
				ID: block.FunctionToolCall.CallID, Name: block.FunctionToolCall.Name, Arguments: arguments,
			})
		}
	}
	if message.ResponseMeta != nil && message.ResponseMeta.TokenUsage != nil {
		usage := message.ResponseMeta.TokenUsage
		result.PromptTokens = usage.PromptTokens
		result.CompletionTokens = usage.CompletionTokens
		result.TotalTokens = usage.TotalTokens
	}
}

func testTextEmbeddingModel(parent context.Context, cfg llm.ModelConfig, modelName string, started time.Time) modelTestResponse {
	result := newModelTestResponse(modelName, "text_embedding", false)
	client, err := llm.NewEmbeddingClient(cfg)
	if err != nil {
		return failModelTest(result, started, err)
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	texts := []string{"今天天气真好", "今日天气十分晴朗", "猫在键盘上散步"}
	embeddings := make([][]float64, 0, len(texts))
	for _, text := range texts {
		embedding, err := client.EmbedText(ctx, text)
		if err != nil {
			return failModelTest(result, started, err)
		}
		embeddings = append(embeddings, embedding.Embedding)
	}
	dimension := len(embeddings[0])
	result.Success = true
	result.LatencyMS = latencyPointer(started)
	result.Response = fmt.Sprintf("嵌入向量维度: %d", dimension)
	result.EmbeddingDimension = &dimension
	result.EmbeddingPairs = []modelTestEmbeddingPair{
		{Label: fmt.Sprintf("相近文本：「%s」×「%s」", texts[0], texts[1]), Similarity: cosineSimilarity(embeddings[0], embeddings[1])},
		{Label: fmt.Sprintf("无关文本：「%s」×「%s」", texts[0], texts[2]), Similarity: cosineSimilarity(embeddings[0], embeddings[2])},
	}
	return result
}

func testImageEmbeddingModel(parent context.Context, cfg llm.ModelConfig, modelName string, started time.Time) modelTestResponse {
	result := newModelTestResponse(modelName, "image_embedding", false)
	client, err := llm.NewEmbeddingClient(cfg)
	if err != nil {
		return failModelTest(result, started, err)
	}
	red, err := solidPNG(color.RGBA{R: 196, G: 42, B: 42, A: 255})
	if err != nil {
		return failModelTest(result, started, err)
	}
	blue, err := solidPNG(color.RGBA{R: 42, G: 68, B: 196, A: 255})
	if err != nil {
		return failModelTest(result, started, err)
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	first, err := client.EmbedImage(ctx, llm.ImageEmbeddingInput{Bytes: red, MIMEType: "image/png", PreprocessVersion: "webui_model_test_v1"})
	if err != nil {
		return failModelTest(result, started, err)
	}
	second, err := client.EmbedImage(ctx, llm.ImageEmbeddingInput{Bytes: red, MIMEType: "image/png", PreprocessVersion: "webui_model_test_v1"})
	if err != nil {
		return failModelTest(result, started, err)
	}
	other, err := client.EmbedImage(ctx, llm.ImageEmbeddingInput{Bytes: blue, MIMEType: "image/png", PreprocessVersion: "webui_model_test_v1"})
	if err != nil {
		return failModelTest(result, started, err)
	}
	dimension := len(first.Embedding)
	result.Success = true
	result.LatencyMS = latencyPointer(started)
	result.Response = fmt.Sprintf("图片嵌入向量维度: %d", dimension)
	result.EmbeddingDimension = &dimension
	result.EmbeddingPairs = []modelTestEmbeddingPair{
		{Label: "相同图片（一致性）", Similarity: cosineSimilarity(first.Embedding, second.Embedding)},
		{Label: "不同图片（区分度）", Similarity: cosineSimilarity(first.Embedding, other.Embedding)},
	}
	return result
}

func solidPNG(value color.RGBA) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, value)
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func cosineSimilarity(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	var dot, leftNorm, rightNorm float64
	for index := range left {
		l := left[index]
		r := right[index]
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return math.Round(dot/(math.Sqrt(leftNorm)*math.Sqrt(rightNorm))*10000) / 10000
}

func newModelTestResponse(modelName, testKind string, visual bool) modelTestResponse {
	return modelTestResponse{
		ModelName: modelName, TestKind: testKind, VisualTested: visual,
		ToolCalls: []modelTestToolCall{}, EmbeddingPairs: []modelTestEmbeddingPair{},
	}
}

func failModelTest(result modelTestResponse, started time.Time, err error) modelTestResponse {
	result.Success = false
	result.LatencyMS = latencyPointer(started)
	result.Error = stringPointer(formatModelTestError(err))
	return result
}

func formatModelTestError(err error) string {
	lines := []string{"错误类型: " + fmt.Sprintf("%T", err), "错误摘要: " + err.Error()}
	var statusErr *llm.HTTPStatusError
	if errors.As(err, &statusErr) {
		lines = append(lines[:1], append([]string{fmt.Sprintf("HTTP 状态码: %d", statusErr.StatusCode)}, lines[1:]...)...)
		if upstream := strings.TrimSpace(statusErr.Message); upstream != "" && upstream != err.Error() {
			lines = append(lines, "上游返回:\n"+upstream)
		}
	}
	if cause := errors.Unwrap(err); cause != nil {
		lines = append(lines, fmt.Sprintf("底层异常: %T: %v", cause, cause))
	}
	return strings.Join(lines, "\n")
}

func latencyPointer(started time.Time) *float64 {
	value := math.Round(float64(time.Since(started).Microseconds())/1000*100) / 100
	return &value
}

func isReplyerTestModel(identifier string) bool {
	normalized := strings.ToLower(strings.TrimSpace(identifier))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	return normalized == "v2p1_raw" || normalized == "v2p1_contract" || strings.Contains(normalized, "mai_replier")
}
