package services

// LLM 服务层调用编排
// 对等 MaiBot src/services/llm_service.py 的职责边界：
//   - 任务名规范化与路由器选择（delegated 到 internal/llm）
//   - 任务级默认 temperature/max_tokens 合并，显式参数优先
//   - 请求上下文注入 response_format 与缓存统计元数据
//   - 统一错误包装，供上层转 LLMServiceResult
// 不含：Python 的 orchestrator 生成循环（重试/故障切换在 internal/llm attemptRunner 与 failoverModel）
// 未移植：Python LLMService.generate_response_from_snapshot；Go 的 ReplaySnapshot/BuildReplayRequest
// 只构造 *http.Request，不解析成 eino 响应，无法在不伪造响应的前提下提供等价入口。

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"maibot-go/internal/llm_models"
)

// LLMServiceClient 绑定一个任务/请求类型/会话的 LLM 服务入口
type LLMServiceClient struct {
	Router      *llm.Router
	TaskName    llm.TaskName
	RequestType string
	SessionID   string
}

// NewLLMServiceClient 创建服务客户端；task 为空时按 replyer 处理
func NewLLMServiceClient(router *llm.Router, taskName, requestType, sessionID string) *LLMServiceClient {
	task, _ := llm.NormalizeTaskName(taskName)
	if strings.TrimSpace(requestType) == "" {
		requestType = string(task)
	}
	return &LLMServiceClient{Router: router, TaskName: task, RequestType: strings.TrimSpace(requestType), SessionID: strings.TrimSpace(sessionID)}
}

// LLMServiceError 保留底层错误链，同时提供 Python 风格的上层文案
type LLMServiceError struct {
	Message string
	Cause   error
}

func (e *LLMServiceError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unwrap 暴露底层错误，便于 errors.Is/As 分类
func (e *LLMServiceError) Unwrap() error { return e.Cause }

// Generate 用单条用户文本发起生成
func (c *LLMServiceClient) Generate(ctx context.Context, prompt string, opts ...model.Option) (*schema.AgenticMessage, error) {
	return c.GenerateWithContext(ctx, func(context.Context) ([]*schema.AgenticMessage, error) {
		return []*schema.AgenticMessage{{
			Role:          schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: prompt})},
		}}, nil
	}, opts...)
}

// GenerateImage 用文本+图片发起生成，图片按 data URI/base64 语义交给具体客户端
func (c *LLMServiceClient) GenerateImage(ctx context.Context, prompt string, image *schema.UserInputImage, opts ...model.Option) (*schema.AgenticMessage, error) {
	return c.GenerateWithContext(ctx, func(context.Context) ([]*schema.AgenticMessage, error) {
		blocks := []*schema.ContentBlock{schema.NewContentBlock(&schema.UserInputText{Text: prompt})}
		if image != nil {
			blocks = append(blocks, schema.NewContentBlock(image))
		}
		return []*schema.AgenticMessage{{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}}, nil
	}, opts...)
}

// LLMServiceAudioResult 语音转写服务结果
// Python LLMAudioTranscriptionResult 只有 text；Go 额外回填实际命中的模型与用量，供调用链报告
type LLMServiceAudioResult struct {
	Text            string
	ModelName       string
	ModelIdentifier string
	APIProvider     string
	Usage           *schema.TokenUsage
}

// TranscribeAudio 用当前任务路由执行语音转写；voiceBase64 是音频字节的标准 base64
// 对齐 MaiBot LLMService.transcribe_audio → orchestrator.generate_response_for_voice
func (c *LLMServiceClient) TranscribeAudio(ctx context.Context, voiceBase64 string) (*LLMServiceAudioResult, error) {
	if c == nil || c.Router == nil {
		return nil, &LLMServiceError{Message: "音频转写时出错: LLM 路由器未初始化", Cause: fmt.Errorf("LLM 路由器未初始化")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	route, result, err := c.Router.TranscribeAudioForTask(ctx, c.TaskName, c.RequestType, c.SessionID, llm.AudioTranscriptionRequest{
		AudioBase64: voiceBase64,
	})
	if err != nil {
		return nil, &LLMServiceError{Message: "音频转写时出错: " + err.Error(), Cause: err}
	}
	modelName := strings.TrimSpace(result.ModelName)
	if modelName == "" {
		modelName = strings.TrimSpace(route.MaiBotModel)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(route.Profile.ModelName)
	}
	modelIdentifier := strings.TrimSpace(result.ModelIdentifier)
	if modelIdentifier == "" {
		modelIdentifier = strings.TrimSpace(route.Config.Model)
	}
	apiProvider := strings.TrimSpace(result.APIProvider)
	if apiProvider == "" {
		apiProvider = strings.TrimSpace(route.Config.ProviderName)
	}
	return &LLMServiceAudioResult{
		Text:            strings.TrimSpace(result.Text),
		ModelName:       modelName,
		ModelIdentifier: modelIdentifier,
		APIProvider:     apiProvider,
		Usage:           result.Usage,
	}, nil
}

// GenerateWithContext 延迟构造消息并生成；context factory 可读取已注入的任务元数据
func (c *LLMServiceClient) GenerateWithContext(ctx context.Context, factory func(context.Context) ([]*schema.AgenticMessage, error), opts ...model.Option) (*schema.AgenticMessage, error) {
	if c == nil || c.Router == nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: LLM 路由器未初始化", Cause: fmt.Errorf("LLM 路由器未初始化")}
	}
	if factory == nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: 缺少消息构造器", Cause: fmt.Errorf("缺少消息构造器")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = llm.WithCacheStatsMetadata(ctx, string(c.TaskName), c.RequestType, c.SessionID)
	ctx = llm.WithModelUsageMetadata(ctx, string(c.TaskName), c.RequestType, c.SessionID)
	messages, err := factory(ctx)
	if err != nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: " + err.Error(), Cause: err}
	}
	if len(messages) == 0 {
		return nil, &LLMServiceError{Message: "生成内容时出错: 消息为空", Cause: fmt.Errorf("消息为空")}
	}

	_, client, err := c.Router.NewClientForTaskWithFailover(ctx, c.TaskName)
	if err != nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: " + err.Error(), Cause: err}
	}
	// 任务默认温度/max_tokens 由 Router.Resolve 写入 ModelConfig，再由各客户端解析；
	// 这里不再用 eino option 预注入，避免默认值压过 models[].temperature / extra_params
	message, err := client.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: " + err.Error(), Cause: err}
	}
	if message == nil {
		return nil, &LLMServiceError{Message: "生成内容时出错: 响应为空", Cause: fmt.Errorf("响应为空")}
	}
	return message, nil
}
