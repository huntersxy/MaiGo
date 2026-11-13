package expressionvector

// 表达方式向量索引，对应 MaiBot src/chat/replyer/expression_vector_index.py。
//
// Python 侧该文件自成闭环：只 import common/config/numpy，自带索引专用的扁平记录
// IndexedExpression，不 import learners 的表达式模型。本包沿用同一结构，
// 通过 Port 端口取用嵌入客户端、配置与表达方式行，
// 使依赖方向与 Python 一致：learners 与 chat/replyer 都单向引用本包。

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TextEmbedder 文本嵌入客户端端口
//
// 对应 Python 的 EmbeddingServiceClient。
type TextEmbedder interface {
	EmbedText(ctx context.Context, text string) ([]float32, error)
}

// Settings 向量索引依赖的配置子集
//
// 对应 Python 从 global_config.expression 读取的字段。
type Settings struct {
	UseVectorExpression       bool
	ExpressionVectorIndexPath string
	VectorCandidatePoolSize   int
}

// Record 索引需要的一条表达方式
//
// 对齐 Python IndexedExpression：只保留索引与同步判定所需字段。
type Record struct {
	ID         uint
	Situation  string
	Style      string
	Count      int
	Checked    bool
	SessionID  string
	ModifiedBy string
}

// Port 向量索引取用外部能力的端口
//
// 由 learners.Registry 实现；本包不感知 Registry 类型。
type Port interface {
	// ExpressionVectors 返回当前索引实例
	ExpressionVectors() ExpressionVectorStore
	// Embedder 返回文本嵌入客户端，未配置时为 nil
	Embedder() TextEmbedder
	// Settings 返回向量相关配置
	Settings() Settings
	// ListExpressions 分页读取表达方式，供历史回填使用
	ListExpressions(ctx context.Context, offset, limit int) ([]Record, int, error)

	// EnablePersistentExpressionVectors 切换到持久化索引并载入已有文件
	EnablePersistentExpressionVectors(path string) (*PersistentExpressionVectorStore, error)

	// 同步状态回调，供回填循环上报进度与错误
	BeginExpressionVectorSync()
	FinishExpressionVectorSync(err error)
	RecordExpressionVectorSyncError(err error)
	ClearExpressionVectorSyncError()
}

// expressionEmbeddingText 构建写索引用的嵌入文本
//
// 对齐 Python expression_vector_index.expression_embedding_text。
func expressionEmbeddingText(situation, style string) string {
	situation = strings.TrimSpace(situation)
	style = normalizeExpressionStyle(style)
	if situation == "" && style == "" {
		return ""
	}
	return "情景：" + situation + "\n风格：" + style
}

// normalizeExpressionStyle 剥离「使用」前缀并去空白
func normalizeExpressionStyle(style string) string {
	normalized := strings.TrimSpace(style)
	if strings.HasPrefix(normalized, "使用") {
		return strings.TrimSpace(normalized[len("使用"):])
	}
	return normalized
}

// nowFunc 可在测试中替换
var nowFunc = time.Now

// SelectExpressionsByVector 供宿主在候选集上做向量召回的导出入口
func SelectExpressionsByVector(
	ctx context.Context,
	port Port,
	valid []Record,
	query string,
	filter ExpressionVectorFilter,
	candidatePoolSize ...int,
) []Record {
	return selectExpressionsByVector(ctx, port, valid, query, filter, candidatePoolSize...)
}

// ExpressionQueryEmbeddingText 构建回复侧查询嵌入文本（target + reply_reason）
//
// 对齐 Python expression_vector_index.expression_query_embedding_text。
func ExpressionQueryEmbeddingText(targetMessage, replyReason string) string {
	var parts []string
	if s := strings.TrimSpace(targetMessage); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(replyReason); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

const (
	heuristicExpressionPoolSize = 64
	// MaxSelectedExpressions 表达式选择上限，对齐 Python
	MaxSelectedExpressions = 5
)

// NormalizeExpressionStyleForLearning 规范化表达风格，导出给宿主复用
func NormalizeExpressionStyleForLearning(style string) string {
	return normalizeExpressionStyle(style)
}

// NoopStore 占位索引，供宿主在未配置向量能力时使用
type NoopStore = noopExpressionVectorStore

// BackfillRunner 历史补建后台循环
type BackfillRunner = expressionBackfillRunner

// NewBackfillRunner 构造补建循环
func NewBackfillRunner(port Port, opts ExpressionBackfillOptions) *expressionBackfillRunner {
	return &expressionBackfillRunner{reg: port, opts: opts.normalized()}
}

// Run 启动补建循环，调用方负责在 goroutine 中执行
func (r *expressionBackfillRunner) Run() { r.run() }

// Cancel 取消补建循环
func (r *expressionBackfillRunner) Cancel() { r.cancel() }

// Done 返回循环退出信号
func (r *expressionBackfillRunner) Done() <-chan struct{} { return r.done }

// SettingsSignatureOf 生成影响补建循环的学习配置签名
func SettingsSignatureOf(s Settings) string {
	return fmt.Sprintf("use=%t|path=%s", s.UseVectorExpression, strings.TrimSpace(s.ExpressionVectorIndexPath))
}

// NewBackfillRunnerWith 带运行期字段构造补建循环
func NewBackfillRunnerWith(
	port Port,
	opts ExpressionBackfillOptions,
	ctx context.Context,
	cancel context.CancelFunc,
	done chan struct{},
	wakeup chan struct{},
	settingsSignature string,
	routeSignature string,
) *expressionBackfillRunner {
	r := &expressionBackfillRunner{reg: port, opts: opts.normalized()}
	r.ctx = ctx
	r.cancel = cancel
	r.done = done
	r.wakeup = wakeup
	r.settingsSignature = settingsSignature
	r.routeSignature = routeSignature
	return r
}

// SettingsSignature 返回当前配置签名
func (r *expressionBackfillRunner) SettingsSignature() string { return r.settingsSignature }

// RouteSignature 返回当前路由签名
func (r *expressionBackfillRunner) RouteSignature() string { return r.routeSignature }

// UpdateSignatures 更新签名
func (r *expressionBackfillRunner) UpdateSignatures(settings, route string) {
	r.settingsSignature = settings
	if route != "" {
		r.routeSignature = route
	}
}

// Generation 返回当前补建代际
func (r *expressionBackfillRunner) Generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}
