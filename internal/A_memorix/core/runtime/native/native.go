package native

// A_Memorix 原生内核装配，对齐 Python SDKMemoryKernel 的构造顺序
// 与 runtime 包的 Kernel 转发器不同，这里直接调用 storage/retrieval/embedding 构造函数，
// 缺失的服务一律记录为缺失并显式降级，不伪造成功

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"maibot-go/internal/A_memorix/core/embedding"
	imagememory "maibot-go/internal/A_memorix/core/image"
	"maibot-go/internal/A_memorix/core/retrieval"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/A_memorix/core/runtime/services"
	"maibot-go/internal/A_memorix/core/storage"
	"maibot-go/internal/A_memorix/core/utils"
)

// Options 原生装配参数
type Options struct {
	// PluginRoot 插件根目录，对齐 python plugin_root
	PluginRoot string
	// Config 插件配置快照，对齐 python self.config
	Config map[string]any
	// Logger 日志器，nil 时使用默认日志器
	Logger utils.Logger
	// RequireCompleteServices 为 true 时必需服务缺失会让 Open 直接失败
	RequireCompleteServices bool
	// SessionLookup 把聊天流 ID 解析为群号与用户号，nil 时命中过滤按未知身份处理
	SessionLookup func(streamID string) (groupID string, userID string)
	// MessageSource 聊天消息来源，用于聊天摘要导入，nil 时摘要返回消息来源未初始化
	MessageSource utils.SummaryMessageSource
	// FindMessages 按平台/会话/消息 ID 回查引用消息，nil 时摘要引用按不可用处理
	FindMessages func(platform string, sessionID string, messageID string, limit int) ([]utils.SessionMessage, error)
	// EmbeddingConfigSource embedding 模型配置来源，nil 时向量通道按 model_config 未初始化降级
	EmbeddingConfigSource embedding.ModelConfigProvider
	// LLM 文本生成网关，用于聊天摘要与修正计划，nil 时按实例模型配置构造
	LLM utils.TextGenerationGateway
	// Prompt 提示词提供者，nil 时按实例 prompts 目录构造
	Prompt utils.SummaryPromptProvider
	// ModelTasks 可用文本生成任务，用于摘要与修正选择模型
	ModelTasks map[string]utils.TextGenerationTaskConfig
	// ModelDict 当前模型字典，对齐 models_dict
	ModelDict map[string]any
	// WebImportCommandRunner WebUI 批量迁移/转换执行器，nil 时导入命令不可用
	WebImportCommandRunner utils.WebImportCommandRunner
	// BotName 机器人昵称，用于摘要提示词
	BotName string
	// Personality 人格设定，用于摘要提示词
	Personality string
	// BotSelfIDs 按平台登记的机器人自身账号，用于摘要身份排除；平台名不区分大小写
	BotSelfIDs map[string][]string
	// BotSelfResolver 运行期机器人自身账号判定，非 nil 时优先于 BotSelfIDs，供宿主接 bot_accounts
	BotSelfResolver func(platform string, userID string) bool
}

// Report 装配结果，如实记录已装配、缺失与降级的通道
type Report struct {
	DataDir            string
	EmbeddingDimension int
	// Filled 已装配的服务名
	Filled []string
	// Missing 尚未实现或未装配的服务名
	Missing []string
	// MissingRequired KernelServices.Validate 判定为必需但缺失的服务
	MissingRequired []string
	// Degraded 降级通道与原因
	Degraded []string
}

// Runtime 原生运行时，持有内核、装配出的存储与检索对象以及写者锁
type Runtime struct {
	kernel *memruntime.Kernel
	logger utils.Logger

	requireComplete       bool
	sessionLookup         func(streamID string) (groupID string, userID string)
	messageSource         utils.SummaryMessageSource
	findMessages          func(platform string, sessionID string, messageID string, limit int) ([]utils.SessionMessage, error)
	llmGateway            utils.TextGenerationGateway
	embeddingConfigSource embedding.ModelConfigProvider
	promptProvider        utils.SummaryPromptProvider
	modelTasks            map[string]utils.TextGenerationTaskConfig
	modelDict             map[string]any
	webImportRunner       utils.WebImportCommandRunner
	botName               string
	personality           string
	botSelfIDs            map[string]map[string]struct{}
	botSelfResolver       func(platform string, userID string) bool

	initLock   sync.Mutex
	writerLock *memruntime.RuntimeWriterLock

	metadataStore        *storage.Store
	graphStore           *storage.GraphStore
	vectorStore          *storage.VectorStore
	paragraphVectorStore *storage.VectorStore
	graphVectorStore     *storage.VectorStore
	embeddingManager     *embedding.EmbeddingAPIAdapter
	sparseIndex          *retrieval.SparseBM25Index
	sparseEnabled        bool
	relationWriteService memruntime.RelationWriteService
	imageRuntime         *imagememory.ImageMemoryRuntime
	vectorSpace          *services.VectorSpaceService
	embeddingState       *services.EmbeddingStateService
	legacyVectorView     *storage.ReadOnlyVectorStoreView

	importManager *utils.WebImportManager
	importCancel  context.CancelFunc
	importDone    chan struct{}
	tuningManager *utils.RetrievalTuningManager

	retriever       *retrieval.DualPathRetriever
	thresholdFilter *retrieval.DynamicThresholdFilter
	runtimeBundle   memruntime.SearchRuntimeBundle

	base      *services.Base
	lifecycle *services.RuntimeLifecycleOrchestrator

	profileMu     sync.Mutex
	personProfile *utils.PersonProfileService

	activeMu      sync.Mutex
	activePersons map[string]float64

	maintenanceMu   sync.Mutex
	lastMaintenance float64
	// graphProjectionMu 串行化关系图投影与图快照保存，对齐 Python _relation_graph_projection_lock
	graphProjectionMu sync.Mutex
	stopping          atomic.Bool

	migrationMu     sync.Mutex
	migrationStatus map[string]any
	dualMigration   *services.DualVectorMigrationService

	mu       sync.RWMutex
	services memruntime.KernelServices
	report   Report
	degraded map[string]struct{}
	closed   bool
}

// Open 按 Python 初始化顺序装配原生运行时
func Open(opts Options) (*Runtime, error) {
	kernel := memruntime.NewKernel(opts.PluginRoot, opts.Config, opts.Logger)
	return OpenKernel(kernel, opts)
}

// OpenKernel 在既有内核上完成服务装配，供宿主 KernelFactory 复用内核实例
func OpenKernel(kernel *memruntime.Kernel, opts Options) (*Runtime, error) {
	if kernel == nil {
		return nil, errors.New("A_Memorix 原生装配缺少内核实例")
	}
	logger := opts.Logger
	if logger == nil {
		logger = kernel.Logger()
	}
	dataDir := kernel.DataDir()
	if dataDir == "" {
		return nil, errors.New("A_Memorix 数据目录为空，无法装配原生内核")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 A_Memorix 数据目录失败: %w", err)
	}
	runtime := &Runtime{
		kernel:                kernel,
		logger:                logger,
		requireComplete:       opts.RequireCompleteServices,
		sessionLookup:         opts.SessionLookup,
		messageSource:         opts.MessageSource,
		findMessages:          opts.FindMessages,
		llmGateway:            opts.LLM,
		embeddingConfigSource: opts.EmbeddingConfigSource,
		promptProvider:        opts.Prompt,
		modelTasks:            opts.ModelTasks,
		modelDict:             opts.ModelDict,
		webImportRunner:       opts.WebImportCommandRunner,
		botName:               opts.BotName,
		personality:           opts.Personality,
		botSelfIDs:            normalizeBotSelfIDs(opts.BotSelfIDs),
		botSelfResolver:       opts.BotSelfResolver,
		degraded:              map[string]struct{}{},
	}
	runtime.writerLock = memruntime.NewRuntimeWriterLock(filepath.Join(dataDir, ".a_memorix_runtime_writer.lock"))
	// 反馈纠错配置来自 A_Memorix integration 段，服务闭包每次读取最新值
	kernel.SetFeedbackIntegrationConfig(feedbackIntegrationConfig(kernel))
	if err := runtime.assemble(); err != nil {
		return nil, err
	}
	kernel.SetStatusReporter(runtime.HostStatusReport)
	memruntime.SetRuntimeKernel(kernel)
	return runtime, nil
}

// assemble 构造服务表并写入内核
func (r *Runtime) assemble() error {
	services, report, err := r.buildServices()
	if err != nil {
		return err
	}
	r.services = services
	r.report = report
	r.kernel.SetServices(services)
	if r.requireComplete {
		if missing := services.Validate(); len(missing) > 0 {
			return fmt.Errorf("A_Memorix 必需服务缺失: %v", missing)
		}
	}
	return nil
}

// Services 返回当前服务集合副本
func (r *Runtime) Services() memruntime.KernelServices {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.services
}

// Kernel 返回底层运行时内核
func (r *Runtime) Kernel() *memruntime.Kernel { return r.kernel }

// Report 返回装配报告副本
func (r *Runtime) Report() Report {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := r.report
	out.Filled = append([]string(nil), r.report.Filled...)
	out.Missing = append([]string(nil), r.report.Missing...)
	out.MissingRequired = append([]string(nil), r.report.MissingRequired...)
	out.Degraded = append([]string(nil), r.report.Degraded...)
	return out
}

// HostStatusReport 返回宿主可用观测的装配状态，键与宿主状态字段一致
func (r *Runtime) HostStatusReport() map[string]any {
	report := r.Report()
	return map[string]any{
		"degraded_reasons":          report.Degraded,
		"missing_services":          report.Missing,
		"missing_required_services": report.MissingRequired,
	}
}

// markDegraded 记录一次降级，重复原因只保留一条
func (r *Runtime) markDegraded(reason string) {
	if reason == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.degraded == nil {
		r.degraded = map[string]struct{}{}
	}
	if _, exists := r.degraded[reason]; exists {
		return
	}
	r.degraded[reason] = struct{}{}
	r.report.Degraded = append(r.report.Degraded, reason)
}

// degradedReasons 返回降级原因快照
func (r *Runtime) degradedReasons() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.report.Degraded...)
}

// storeRef 把可能为 nil 的指针安全地放入 any，避免 typed-nil 接口误判
func storeRef[T any](value *T) any {
	if value == nil {
		return nil
	}
	return value
}

// servicesBase 构造服务基座，元数据与图存储未打开时传 nil 端口
func (r *Runtime) servicesBase() *services.Base {
	return services.NewBase(r.kernel.Config(), r.kernel.DataDir(), newMetadataPort(r.metadataStore), newGraphPort(r.graphStore), slogLoggerOrNil())
}

// setServices 替换内核服务集合，供初始化后按真实存储重建
func (r *Runtime) setServices(next memruntime.KernelServices) {
	r.mu.Lock()
	r.services = next
	r.mu.Unlock()
	r.kernel.SetServices(next)
}

// MetadataStore 返回已打开的元数据存储
func (r *Runtime) MetadataStore() *storage.Store {
	if r == nil {
		return nil
	}
	return r.metadataStore
}

// WriterLock 返回数据目录写者锁
func (r *Runtime) WriterLock() *memruntime.RuntimeWriterLock {
	if r == nil {
		return nil
	}
	return r.writerLock
}

// Close 同步释放运行时，等价于 kernel.Close()；内核已停止时兜底关闭残留资源
func (r *Runtime) Close() {
	if r == nil || r.kernel == nil {
		return
	}
	r.kernel.Close()
	if r.kernel.Initialized() {
		return
	}
	if err := r.closeRuntime(); err != nil {
		r.logger.Warning(fmt.Sprintf("[native] 关闭残留运行时资源失败: %v", err))
	}
}

// Initialize 同步初始化内核，等价于 kernel.Initialize(ctx)
func (r *Runtime) Initialize(ctx context.Context) error {
	if r == nil || r.kernel == nil {
		return errors.New("A_Memorix 原生运行时未装配")
	}
	return r.kernel.Initialize(ctx)
}

// Shutdown 关闭内核，等价于 kernel.Shutdown(ctx)
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.kernel == nil {
		return errors.New("A_Memorix 原生运行时未装配")
	}
	if err := r.kernel.Shutdown(ctx); err != nil {
		return err
	}
	return r.closeRuntime()
}
