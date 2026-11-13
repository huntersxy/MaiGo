package memorywriteback

// 图片记忆写回 worker：任务库轮询、失败退避与图片描述补偿
// 对齐 Python src/services/memory_flow_service.py:753-991 与 src/A_memorix/core/runtime/image_writeback_journal.py

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	imagememory "maibot-go/internal/A_memorix/core/image"
	memruntime "maibot-go/internal/A_memorix/core/runtime"
	"maibot-go/internal/chat/message_receive"
	"maibot-go/internal/common/data_models/messagecomponent"
	"maibot-go/internal/common/database"
	maibotcfg "maibot-go/internal/config/maibot"
)

const imageWritebackDefaultPollInterval = 2 * time.Second

// ImageWritebackSettings 图片写回运行参数
type ImageWritebackSettings struct {
	PollInterval  time.Duration
	BatchSize     int
	MaxRetries    int
	ContextLength int
}

// DefaultImageWritebackSettings 返回 Python official_configs.py:1892-1913 的默认值
func DefaultImageWritebackSettings() ImageWritebackSettings {
	return ImageWritebackSettings{
		PollInterval:  imageWritebackDefaultPollInterval,
		BatchSize:     4,
		MaxRetries:    5,
		ContextLength: chatSummaryDefaultContextLen,
	}
}

// ImageWritebackSettingsFromInstance 从实例配置读取图片写回参数，配置缺失时退回默认值
func ImageWritebackSettingsFromInstance(cfg *maibotcfg.InstanceConfig) ImageWritebackSettings {
	settings := DefaultImageWritebackSettings()
	if cfg == nil {
		return settings
	}
	image := cfg.Bot.AMemorix.ImageMemory
	if image.JobPollIntervalSeconds >= 0.1 {
		settings.PollInterval = time.Duration(image.JobPollIntervalSeconds * float64(time.Second))
	}
	if image.JobBatchSize > 0 {
		settings.BatchSize = image.JobBatchSize
	}
	if image.JobMaxRetries >= 0 {
		settings.MaxRetries = image.JobMaxRetries
	}
	if contextLength := cfg.Bot.AMemorix.Integration.ChatSummaryWritebackContextLength; contextLength > 0 {
		settings.ContextLength = contextLength
	}
	return settings
}

// ImageMemoryEnabledFromInstance 对齐 Python _enabled: plugin.enabled and image_memory.enabled
func ImageMemoryEnabledFromInstance(cfg *maibotcfg.InstanceConfig) bool {
	if cfg == nil {
		return false
	}
	return cfg.Bot.AMemorix.Plugin.Enabled && cfg.Bot.AMemorix.ImageMemory.Enabled
}

// ImageDescriptionHook 延迟绑定图片描述查询，避免启动顺序耦合
type ImageDescriptionHook struct {
	mu sync.RWMutex
	fn ImageWritebackCachedDescription
}

// Set 绑定描述查询实现，允许在 worker 启动后注入
func (h *ImageDescriptionHook) Set(fn ImageWritebackCachedDescription) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

// Describe 查询缓存描述，未绑定时返回空串
func (h *ImageDescriptionHook) Describe(ctx context.Context, imageHash string) string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	fn := h.fn
	h.mu.RUnlock()
	if fn == nil {
		return ""
	}
	return fn(ctx, imageHash)
}

// ImageWritebackOptions 图片写回依赖
type ImageWritebackOptions struct {
	Enabled           func() bool
	Settings          func() ImageWritebackSettings
	JournalPath       func() string
	FindMessage       func(ctx context.Context, sessionID string, messageID string) (*memory.MessageLog, error)
	ImageRuntime      func() *imagememory.ImageMemoryRuntime
	ImageMemory       func(ctx context.Context, action string, payload map[string]any) (map[string]any, error)
	Kernel            func() ChatSummaryWritebackKernel
	CachedDescription ImageWritebackCachedDescription
}

// ImageWritebackService 图片记忆写回服务，包含任务轮询与描述补偿两个循环
type ImageWritebackService struct {
	opts ImageWritebackOptions

	mu               sync.Mutex
	journal          *imagememory.WritebackJournal
	started          bool
	stopping         bool
	cancel           context.CancelFunc
	workerDone       chan struct{}
	compensationDone chan struct{}
}

// NewImageWritebackService 创建图片写回服务
func NewImageWritebackService(opts ImageWritebackOptions) *ImageWritebackService {
	return &ImageWritebackService{opts: opts}
}

// Start 幂等启动任务轮询与描述补偿循环，对齐 Python start:765
// 未启用时直接返回，不创建任务库
func (s *ImageWritebackService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	if !s.enabled() {
		return nil
	}
	path := s.journalPath()
	journal, err := imagememory.OpenWritebackJournal(path)
	if err != nil {
		return fmt.Errorf("打开图片写回任务库失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.journal = journal
	s.cancel = cancel
	s.stopping = false
	s.started = true
	s.workerDone = make(chan struct{})
	s.compensationDone = make(chan struct{})
	go s.workerLoop(ctx)
	go s.compensationLoop(ctx)
	return nil
}

// Stop 取消两个循环并关闭任务库，对齐 Python shutdown:779
func (s *ImageWritebackService) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.stopping = true
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.started = false
	cancel := s.cancel
	journal := s.journal
	workerDone := s.workerDone
	compensationDone := s.compensationDone
	s.journal = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	closed := true
	for _, done := range []chan struct{}{workerDone, compensationDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			closed = false
		}
	}
	if !closed {
		return ctx.Err()
	}
	if journal != nil {
		if err := journal.Close(); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueIncoming 入站图片消息入队，对齐 Python enqueue:798
// 未启用、停止中与确定不含图片组件的消息直接跳过；任务库未就绪时先启动
func (s *ImageWritebackService) EnqueueIncoming(ctx context.Context, sessionID string, messageID string, source any) error {
	if !s.enabled() {
		return nil
	}
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return nil
	}
	if found, known := messageSourceHasImageComponents(source); known && !found {
		return nil
	}
	s.mu.Lock()
	journal := s.journal
	started := s.started
	s.mu.Unlock()
	if journal == nil && !started {
		if err := s.Start(); err != nil {
			return err
		}
		s.mu.Lock()
		journal = s.journal
		s.mu.Unlock()
	}
	if journal == nil {
		return errors.New("图片写回服务初始化未完成")
	}
	return journal.Enqueue(strings.TrimSpace(sessionID), strings.TrimSpace(messageID))
}

func (s *ImageWritebackService) enabled() bool {
	return s.opts.Enabled == nil || s.opts.Enabled()
}

func (s *ImageWritebackService) settings() ImageWritebackSettings {
	settings := DefaultImageWritebackSettings()
	if s.opts.Settings != nil {
		settings = s.opts.Settings()
	}
	if settings.PollInterval <= 0 {
		settings.PollInterval = imageWritebackDefaultPollInterval
	}
	if settings.BatchSize <= 0 {
		settings.BatchSize = DefaultImageWritebackSettings().BatchSize
	}
	if settings.MaxRetries < 0 {
		settings.MaxRetries = 0
	}
	if settings.ContextLength <= 0 {
		settings.ContextLength = chatSummaryDefaultContextLen
	}
	return settings
}

func (s *ImageWritebackService) journalPath() string {
	if s.opts.JournalPath == nil {
		return ImageWritebackJournalPath("")
	}
	path := strings.TrimSpace(s.opts.JournalPath())
	if path == "" {
		return ImageWritebackJournalPath("")
	}
	return path
}

func (s *ImageWritebackService) maxAttempts() int {
	return 1 + s.settings().MaxRetries
}

func (s *ImageWritebackService) journalRef() *imagememory.WritebackJournal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.journal
}

func (s *ImageWritebackService) imageRuntime() *imagememory.ImageMemoryRuntime {
	if s.opts.ImageRuntime == nil {
		return nil
	}
	return s.opts.ImageRuntime()
}

func (s *ImageWritebackService) kernel() ChatSummaryWritebackKernel {
	if s.opts.Kernel == nil {
		return nil
	}
	return s.opts.Kernel()
}

// workerLoop 轮询任务库，对齐 Python _worker_loop:843
func (s *ImageWritebackService) workerLoop(ctx context.Context) {
	defer close(s.workerDone)
	for {
		if ctx.Err() != nil {
			return
		}
		if !s.enabled() {
			if !sleepImageWriteback(ctx, s.settings().PollInterval) {
				return
			}
			continue
		}
		journal := s.journalRef()
		if journal == nil {
			zap.L().Error("图片写回任务库未初始化")
			return
		}
		job, err := journal.NextJob()
		if err != nil {
			zap.L().Warn("图片写回任务读取失败", zap.Error(err))
			if !sleepImageWriteback(ctx, s.settings().PollInterval) {
				return
			}
			continue
		}
		if job == nil {
			if !sleepImageWriteback(ctx, s.settings().PollInterval) {
				return
			}
			continue
		}
		s.processJob(ctx, journal, job)
	}
}

// processJob 处理单个任务：成功标记完成，失败保留重试并记录退避
func (s *ImageWritebackService) processJob(ctx context.Context, journal *imagememory.WritebackJournal, job map[string]any) {
	sessionID := strings.TrimSpace(imageWritebackString(job["session_id"]))
	messageID := strings.TrimSpace(imageWritebackString(job["message_id"]))
	err := s.processMessage(ctx, sessionID, messageID)
	if err == nil {
		if completeErr := journal.Complete(sessionID, messageID); completeErr != nil {
			zap.L().Warn("图片写回任务完成标记失败", zap.Error(completeErr))
		}
		return
	}
	if ctx.Err() != nil {
		return
	}
	if failErr := journal.Fail(job, err.Error(), s.maxAttempts()); failErr != nil {
		zap.L().Warn("图片写回任务失败标记写入失败", zap.Error(failErr))
	}
	zap.L().Warn("图片记忆写入失败，任务已保留", zap.Error(err))
}

// processMessage 重放单条消息的图片写入，对齐 Python _handle_message:932
func (s *ImageWritebackService) processMessage(ctx context.Context, sessionID string, messageID string) error {
	if s.opts.FindMessage == nil {
		return errors.New("消息回查端口未接线")
	}
	record, err := s.opts.FindMessage(ctx, sessionID, messageID)
	if err != nil {
		return fmt.Errorf("查询图片入库来源消息失败: %w", err)
	}
	if record == nil {
		return errors.New("图片入库任务的来源消息不存在")
	}
	sequence, err := messagecomponent.DecodeDBRecord(record.RawContent)
	if err != nil {
		return fmt.Errorf("解析图片消息组件失败: %w", err)
	}
	components := CollectImageWritebackComponents(sequence.Components)
	if len(components) == 0 {
		return nil
	}
	runtime := s.imageRuntime()
	if runtime == nil {
		return errors.New("图片记忆运行时未初始化")
	}
	userStatement := ExplicitMessageText(sequence.Components)
	failures := []string{}
	for _, component := range components {
		category, failure := BackfillImageComponent(ctx, runtime, record, sessionID, messageID, component, userStatement, false, s.opts.CachedDescription)
		if category == ImageWritebackProcessed {
			continue
		}
		failures = append(failures, fmt.Sprintf("图片组件处理失败: message_id=%s, component_path=%s: %s", messageID, component.Path, failure))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

// compensationLoop 轮询图片描述补偿，对齐 Python _compensation_loop:867
func (s *ImageWritebackService) compensationLoop(ctx context.Context) {
	defer close(s.compensationDone)
	for {
		if ctx.Err() != nil {
			return
		}
		if s.enabled() {
			s.processDescriptionCompensations(ctx)
		}
		if !sleepImageWriteback(ctx, s.settings().PollInterval) {
			return
		}
	}
}

// processDescriptionCompensations 领取描述补偿并逐条写回摘要，对齐 Python :881
func (s *ImageWritebackService) processDescriptionCompensations(ctx context.Context) {
	if s.opts.ImageMemory == nil {
		zap.L().Warn("图片描述摘要补偿处理失败", zap.Error(errors.New("图片记忆动作端口未接线")))
		return
	}
	claimed, err := s.opts.ImageMemory(ctx, "claim_description_compensations", map[string]any{"limit": s.settings().BatchSize})
	if err != nil {
		zap.L().Warn("图片描述摘要补偿处理失败", zap.Error(err))
		return
	}
	if !imageWritebackTruthy(claimed["success"]) {
		return
	}
	leaseToken := imageWritebackString(claimed["lease_token"])
	for _, item := range imageWritebackItems(claimed["items"]) {
		s.completeDescriptionCompensation(ctx, leaseToken, item)
	}
}

func (s *ImageWritebackService) completeDescriptionCompensation(ctx context.Context, leaseToken string, item map[string]any) {
	occurrenceID := strings.TrimSpace(imageWritebackString(item["occurrence_id"]))
	descriptionHash := strings.TrimSpace(imageWritebackString(item["description_hash"]))
	success := false
	failure := ""
	if err := s.ingestCompensation(ctx, item); err != nil {
		failure = err.Error()
	} else {
		success = true
	}
	payload := map[string]any{
		"occurrence_id":    occurrenceID,
		"description_hash": descriptionHash,
		"lease_token":      leaseToken,
		"success":          success,
		"error":            failure,
	}
	result, err := s.opts.ImageMemory(ctx, "complete_description_compensation", payload)
	if err != nil {
		zap.L().Warn("图片描述摘要补偿完成标记失败", zap.Error(err))
		return
	}
	if !imageWritebackTruthy(result["success"]) {
		zap.L().Warn("图片描述摘要补偿完成标记未生效", zap.String("occurrence_id", occurrenceID))
	}
}

// ingestCompensation 用描述补偿元数据触发摘要写回，对齐 Python :895-921
func (s *ImageWritebackService) ingestCompensation(ctx context.Context, item map[string]any) error {
	kernel := s.kernel()
	if kernel == nil {
		return errors.New("记忆内核未就绪")
	}
	settings := s.settings()
	timeEnd := imageWritebackFloat(item["occurred_at"]) + 0.001
	params := memruntime.IngestSummaryParams{
		ExternalID: fmt.Sprintf("image_description_compensation:%s:%s",
			strings.TrimSpace(imageWritebackString(item["occurrence_id"])),
			strings.TrimSpace(imageWritebackString(item["description_hash"]))),
		ChatID:       strings.TrimSpace(imageWritebackString(item["chat_id"])),
		Text:         "",
		Participants: []string{},
		TimeEnd:      &timeEnd,
		Metadata: map[string]any{
			"generate_from_chat":   true,
			"context_length":       settings.ContextLength,
			"writeback_source":     "image_description_compensation",
			"trigger":              "image_description_ready",
			"image_message_id":     strings.TrimSpace(imageWritebackString(item["message_id"])),
			"summary_review_count": 2,
		},
		RespectFilter: true,
	}
	result, err := kernel.IngestSummary(ctx, params)
	if err != nil {
		return err
	}
	if !imageWritebackTruthy(result["success"]) {
		if detail := imageWritebackString(result["detail"]); detail != "" {
			return errors.New(detail)
		}
		return errors.New("图片描述摘要写回未成功")
	}
	return nil
}

// messageSourceHasImageComponents 判断入站消息是否确定包含图片组件
// 未知来源返回 known=false，交给任务库重放时再判定
func messageSourceHasImageComponents(source any) (found bool, known bool) {
	switch typed := source.(type) {
	case *messagereceive.SessionMessage:
		if typed == nil || typed.RawMessage == nil {
			return false, true
		}
		return len(CollectImageWritebackComponents(typed.RawMessage.Components)) > 0, true
	case messagereceive.SessionMessage:
		if typed.RawMessage == nil {
			return false, true
		}
		return len(CollectImageWritebackComponents(typed.RawMessage.Components)) > 0, true
	case *messagecomponent.MessageSequence:
		if typed == nil {
			return false, true
		}
		return len(CollectImageWritebackComponents(typed.Components)) > 0, true
	}
	return false, false
}

// imageWritebackItems 兼容 []map[string]any 与 []any 两种返回
func imageWritebackItems(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
		return out
	default:
		return nil
	}
}

// imageWritebackFloat 解析数值字段
func imageWritebackFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%f", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

// sleepImageWriteback 可中断休眠
func sleepImageWriteback(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		duration = time.Millisecond
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// CurrentImageMemoryRuntime 取当前原生内核的图片记忆运行时，未就绪返回 nil
func CurrentImageMemoryRuntime() *imagememory.ImageMemoryRuntime {
	kernel := memruntime.GetRuntimeKernel()
	if kernel == nil {
		return nil
	}
	runtime, _ := kernel.ImageMemoryRuntime().(*imagememory.ImageMemoryRuntime)
	return runtime
}

// CurrentImageMemoryAction 调用当前内核的 image_memory 动作，内核未就绪时报错
func CurrentImageMemoryAction(ctx context.Context, action string, payload map[string]any) (map[string]any, error) {
	kernel := memruntime.GetRuntimeKernel()
	if kernel == nil {
		return nil, errors.New("记忆内核未就绪")
	}
	return kernel.ImageMemory(ctx, action, payload)
}
