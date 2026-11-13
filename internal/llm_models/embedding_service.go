package llm

// EmbeddingServiceClient 面向上层的文本/图片嵌入服务门面，对齐 MaiBot
// src/services/embedding_service.py 的 EmbeddingServiceClient：
//   - 文本嵌入按任务路由解析模型，再交给底层 EmbeddingClient
//   - 图片嵌入固定走 image_embedding 任务，并回填 MIME 类型与预处理版本
//   - 文本批量默认串行；显式 max_concurrent>1 时并发且保持输入顺序
//
// Go 的 EmbeddingClient 接口不带 session_id，门面构造时保存的 session 只进入请求
// 快照/统计元数据；逐次覆盖 session 的 Python 行为由调用方新建门面实现。

import (
	"context"
	"fmt"
	"strings"
)

// embeddingClientFactory 让测试替换底层客户端构造；生产路径始终通过 Router 路由
type embeddingClientFactory func(ctx context.Context, resolved ResolvedRoute) (EmbeddingClient, error)

// EmbeddingServiceClient 嵌入服务门面
type EmbeddingServiceClient struct {
	router        *Router
	taskName      TaskName
	requestType   string
	sessionID     string
	clientFactory embeddingClientFactory
}

// NewEmbeddingServiceClient 创建嵌入门面
// taskName 为空时对齐 Python 默认使用 embedding 任务；requestType/sessionID 仅用于调用链元数据
func NewEmbeddingServiceClient(router *Router, taskName TaskName, requestType string, sessionID string) (*EmbeddingServiceClient, error) {
	if router == nil {
		return nil, fmt.Errorf("LLM 路由器未初始化")
	}
	return newEmbeddingServiceClient(router, taskName, requestType, sessionID, nil)
}

// newEmbeddingServiceClient 内部构造；factory 非空时用于测试或自定义客户端来源
func newEmbeddingServiceClient(
	router *Router,
	taskName TaskName,
	requestType string,
	sessionID string,
	factory embeddingClientFactory,
) (*EmbeddingServiceClient, error) {
	if trimTask(string(taskName)) == "" {
		taskName = TaskEmbedding
	}
	normalized, err := NormalizeTaskName(string(taskName))
	if err != nil {
		return nil, err
	}
	if router == nil && factory == nil {
		return nil, fmt.Errorf("LLM 路由器未初始化")
	}
	return &EmbeddingServiceClient{
		router:        router,
		taskName:      normalized,
		requestType:   strings.TrimSpace(requestType),
		sessionID:     strings.TrimSpace(sessionID),
		clientFactory: factory,
	}, nil
}

// TaskName 返回门面绑定的任务名
func (c *EmbeddingServiceClient) TaskName() TaskName {
	if c == nil {
		return ""
	}
	return c.taskName
}

// RequestType 返回门面绑定的业务类型
func (c *EmbeddingServiceClient) RequestType() string {
	if c == nil {
		return ""
	}
	return c.requestType
}

// SessionID 返回门面绑定的聊天流 ID
func (c *EmbeddingServiceClient) SessionID() string {
	if c == nil {
		return ""
	}
	return c.sessionID
}

// EmbedText 生成单条文本嵌入
func (c *EmbeddingServiceClient) EmbedText(ctx context.Context, text string) (EmbeddingResult, error) {
	client, err := c.clientForTask(ctx, c.taskName)
	if err != nil {
		return EmbeddingResult{}, err
	}
	return client.EmbedText(ctx, text)
}

// EmbedTexts 串行生成文本嵌入；空输入返回空切片，与 Python embed_texts 一致
// 任一请求失败立即返回错误，不保留部分结果
func (c *EmbeddingServiceClient) EmbedTexts(ctx context.Context, texts []string) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return []EmbeddingResult{}, nil
	}
	client, err := c.clientForTask(ctx, c.taskName)
	if err != nil {
		return nil, err
	}
	return client.EmbedTexts(ctx, texts)
}

// EmbedTextsConcurrent 按 maxConcurrent 并发嵌入，结果保持输入顺序
// maxConcurrent <= 1 时与 Python 一样退回串行
func (c *EmbeddingServiceClient) EmbedTextsConcurrent(ctx context.Context, texts []string, maxConcurrent int) ([]EmbeddingResult, error) {
	if len(texts) == 0 {
		return []EmbeddingResult{}, nil
	}
	if maxConcurrent <= 1 {
		return c.EmbedTexts(ctx, texts)
	}
	client, err := c.clientForTask(ctx, c.taskName)
	if err != nil {
		return nil, err
	}
	if concurrent, ok := client.(ConcurrentEmbeddingClient); ok {
		return concurrent.EmbedTextsConcurrent(ctx, texts, maxConcurrent)
	}
	return embedTextsConcurrently(ctx, texts, maxConcurrent, client.EmbedText)
}

// EmbedImage 生成单张图片嵌入；固定路由 image_embedding 任务
func (c *EmbeddingServiceClient) EmbedImage(ctx context.Context, input ImageEmbeddingInput) (EmbeddingResult, error) {
	if len(input.Bytes) == 0 {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入输入不能为空")
	}
	mimeType := strings.TrimSpace(input.MIMEType)
	if !strings.HasPrefix(mimeType, "image/") {
		return EmbeddingResult{}, fmt.Errorf("图片嵌入输入必须提供 image/* MIME 类型")
	}
	input.MIMEType = mimeType
	if strings.TrimSpace(input.TaskName) == "" {
		input.TaskName = string(c.taskName)
	}
	if strings.TrimSpace(input.RequestType) == "" {
		input.RequestType = c.requestType
	}
	if strings.TrimSpace(input.SessionID) == "" {
		input.SessionID = c.sessionID
	}
	client, err := c.clientForTask(ctx, TaskImageEmbedding)
	if err != nil {
		return EmbeddingResult{}, err
	}
	result, err := client.EmbedImage(ctx, input)
	if err != nil {
		return EmbeddingResult{}, err
	}
	result.MIMEType = mimeType
	result.PreprocessVersion = input.PreprocessVersion
	return result, nil
}

// clientForTask 按任务解析模型并构造底层客户端；每次请求重新解析，跟随热更新配置
// factory 分支保留旧的单路由构造；生产路径走 Router 的候选 failover
func (c *EmbeddingServiceClient) clientForTask(ctx context.Context, task TaskName) (EmbeddingClient, error) {
	if c == nil {
		return nil, fmt.Errorf("EmbeddingServiceClient 未初始化")
	}
	meta := embeddingRequestMetadata{
		taskName:    c.taskName,
		requestType: c.requestType,
		sessionID:   c.sessionID,
	}
	if c.clientFactory != nil {
		resolved := ResolvedRoute{Task: task}
		if c.router != nil {
			var err error
			resolved, err = c.router.Resolve(ctx, task)
			if err != nil {
				return nil, err
			}
		}
		applyEmbeddingRequestMetadata(&resolved.Config, meta)
		return c.clientFactory(ctx, resolved)
	}
	if c.router == nil {
		return nil, fmt.Errorf("LLM 路由器未初始化")
	}
	_, client, err := c.router.newEmbeddingClientForTask(ctx, task, meta)
	return client, err
}

// 接口实现检查
var (
	_ EmbeddingClient           = (*EmbeddingServiceClient)(nil)
	_ ConcurrentEmbeddingClient = (*EmbeddingServiceClient)(nil)
)
