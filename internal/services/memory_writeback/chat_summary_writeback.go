package memorywriteback

// 聊天摘要自动写回：按消息阈值触发、游标恢复与失败不推进游标
// 对齐 Python src/services/memory_flow_service.py:504-750

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	memruntime "MaiGo/internal/A_memorix/core/runtime"
	maibotcfg "MaiGo/internal/config/maibot"
)

const (
	chatSummaryWritebackQueueSize = 256
	chatSummaryDefaultThreshold   = 36
	chatSummaryDefaultContextLen  = 36
)

// ChatSummaryWritebackSettings 聊天摘要写回运行参数
type ChatSummaryWritebackSettings struct {
	MessageThreshold int
	ContextLength    int
}

// DefaultChatSummaryWritebackSettings 返回 Python official_configs.py:1519/1529 的默认值
func DefaultChatSummaryWritebackSettings() ChatSummaryWritebackSettings {
	return ChatSummaryWritebackSettings{MessageThreshold: chatSummaryDefaultThreshold, ContextLength: chatSummaryDefaultContextLen}
}

// ChatSummaryWritebackSettingsFromInstance 从实例配置读取写回参数，配置缺失时退回默认值
func ChatSummaryWritebackSettingsFromInstance(cfg *maibotcfg.InstanceConfig) ChatSummaryWritebackSettings {
	settings := DefaultChatSummaryWritebackSettings()
	if cfg == nil {
		return settings
	}
	integration := cfg.Bot.AMemorix.Integration
	if integration.ChatSummaryWritebackMessageThreshold > 0 {
		settings.MessageThreshold = integration.ChatSummaryWritebackMessageThreshold
	}
	if integration.ChatSummaryWritebackContextLength > 0 {
		settings.ContextLength = integration.ChatSummaryWritebackContextLength
	}
	return settings
}

// ChatSummaryWritebackKernel 聊天摘要写回所需的内核能力
type ChatSummaryWritebackKernel interface {
	IngestSummary(ctx context.Context, params memruntime.IngestSummaryParams) (map[string]any, error)
	SummaryCheckpointCount(chatID string) (int, error)
	ParagraphsBySource(source string) ([]map[string]any, error)
}

// memorixChatSummaryKernel 用原生内核实现写回内核端口
type memorixChatSummaryKernel struct {
	kernel *memruntime.Kernel
}

// NewMemorixChatSummaryKernel 包装原生内核，内核为空时返回 nil
func NewMemorixChatSummaryKernel(kernel *memruntime.Kernel) ChatSummaryWritebackKernel {
	if kernel == nil {
		return nil
	}
	return memorixChatSummaryKernel{kernel: kernel}
}

// CurrentChatSummaryKernel 取当前原生内核，未就绪时返回 nil
func CurrentChatSummaryKernel() ChatSummaryWritebackKernel {
	return NewMemorixChatSummaryKernel(memruntime.GetRuntimeKernel())
}

func (k memorixChatSummaryKernel) IngestSummary(ctx context.Context, params memruntime.IngestSummaryParams) (map[string]any, error) {
	return k.kernel.IngestSummary(ctx, params)
}

func (k memorixChatSummaryKernel) SummaryCheckpointCount(chatID string) (int, error) {
	store := k.kernel.MetadataStore()
	if store == nil {
		return 0, errors.New("记忆元数据存储未就绪")
	}
	return store.GetSummaryCheckpointCount(chatID)
}

func (k memorixChatSummaryKernel) ParagraphsBySource(source string) ([]map[string]any, error) {
	store := k.kernel.MetadataStore()
	if store == nil {
		return nil, errors.New("记忆元数据存储未就绪")
	}
	return store.GetParagraphsBySource(source)
}

// ChatSummaryWritebackEvent 待写回的聊天消息事件
type ChatSummaryWritebackEvent struct {
	SessionID   string
	UserID      string
	GroupID     string
	MessageTime *time.Time
}

// ChatSummaryWritebackState 单个聊天流的写回游标，对齐 Python ChatSummaryWritebackState:504
type ChatSummaryWritebackState struct {
	LastTriggerMessageCount int
	LastTriggerTime         float64
}

// ChatSummaryWritebackOptions 聊天摘要写回依赖
type ChatSummaryWritebackOptions struct {
	Enabled       func() bool
	Settings      func() ChatSummaryWritebackSettings
	Kernel        func() ChatSummaryWritebackKernel
	CountMessages func(ctx context.Context, sessionID string, endTime *time.Time) (int64, error)
	QueueCapacity int
	Now           func() time.Time
}

// ChatSummaryWritebackService 聊天摘要自动写回服务
type ChatSummaryWritebackService struct {
	opts  ChatSummaryWritebackOptions
	queue chan ChatSummaryWritebackEvent

	mu       sync.Mutex
	states   map[string]*ChatSummaryWritebackState
	started  bool
	stopping bool
	cancel   context.CancelFunc
	done     chan struct{}
}

// NewChatSummaryWritebackService 创建写回服务，队列容量默认 256（Python :510）
func NewChatSummaryWritebackService(opts ChatSummaryWritebackOptions) *ChatSummaryWritebackService {
	capacity := opts.QueueCapacity
	if capacity <= 0 {
		capacity = chatSummaryWritebackQueueSize
	}
	return &ChatSummaryWritebackService{
		opts:   opts,
		queue:  make(chan ChatSummaryWritebackEvent, capacity),
		states: map[string]*ChatSummaryWritebackState{},
	}
}

// Start 幂等启动写回 worker，对齐 Python start:516
func (s *ChatSummaryWritebackService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	s.stopping = false
	go s.workerLoop(ctx)
	return nil
}

// Stop 停止写回 worker 并等待退出，对齐 Python shutdown:522
func (s *ChatSummaryWritebackService) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.stopping = true
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.started = false
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EnqueueSent 发送后入队，对齐 Python enqueue:536
// 未启用与停止中直接跳过；队列满只记录 warning，不影响发送链路
func (s *ChatSummaryWritebackService) EnqueueSent(sessionID string, userID string, groupID string, timestamp time.Time) error {
	if !s.enabled() {
		return nil
	}
	s.mu.Lock()
	stopping := s.stopping
	s.mu.Unlock()
	if stopping {
		return nil
	}
	event := ChatSummaryWritebackEvent{SessionID: sessionID, UserID: userID, GroupID: groupID}
	if !timestamp.IsZero() {
		value := timestamp
		event.MessageTime = &value
	}
	select {
	case s.queue <- event:
		return nil
	default:
		zap.L().Warn("聊天摘要写回队列已满，跳过本次触发")
		return nil
	}
}

func (s *ChatSummaryWritebackService) enabled() bool {
	return s.opts.Enabled == nil || s.opts.Enabled()
}

func (s *ChatSummaryWritebackService) settings() ChatSummaryWritebackSettings {
	if s.opts.Settings == nil {
		return DefaultChatSummaryWritebackSettings()
	}
	settings := s.opts.Settings()
	if settings.MessageThreshold <= 0 {
		settings.MessageThreshold = chatSummaryDefaultThreshold
	}
	if settings.ContextLength <= 0 {
		settings.ContextLength = chatSummaryDefaultContextLen
	}
	return settings
}

func (s *ChatSummaryWritebackService) now() time.Time {
	if s.opts.Now == nil {
		return time.Now()
	}
	return s.opts.Now()
}

// workerLoop 串行消费队列，对齐 Python _worker_loop:546
func (s *ChatSummaryWritebackService) workerLoop(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.queue:
			if err := s.HandleMessage(ctx, event); err != nil {
				zap.L().Warn("聊天摘要写回处理失败", zap.Error(err))
			}
		}
	}
}

// HandleMessage 处理一条写回事件，对齐 Python _handle_message:559
// 失败时只返回错误且不推进游标，成功后才把 trigger_message_count 记为 total
func (s *ChatSummaryWritebackService) HandleMessage(ctx context.Context, event ChatSummaryWritebackEvent) error {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return nil
	}
	total, err := s.countMessages(ctx, sessionID, event.MessageTime)
	if err != nil {
		return fmt.Errorf("统计会话消息数失败: %w", err)
	}
	if total <= 0 {
		return nil
	}
	settings := s.settings()
	state := s.stateFor(ctx, sessionID, total)
	pending := total - state.LastTriggerMessageCount
	if pending < 0 {
		pending = 0
	}
	if pending < settings.MessageThreshold {
		return nil
	}
	contextLength := effectiveChatSummaryContextLength(settings.ContextLength, pending)
	var timeEnd *float64
	if event.MessageTime != nil && !event.MessageTime.IsZero() {
		value := float64(event.MessageTime.UnixNano()) / 1e9
		timeEnd = &value
	}
	params := memruntime.IngestSummaryParams{
		ExternalID:   fmt.Sprintf("chat_auto_summary:%s:%d", sessionID, total),
		ChatID:       sessionID,
		Text:         "",
		Participants: []string{},
		TimeEnd:      timeEnd,
		Metadata: map[string]any{
			"generate_from_chat":             true,
			"context_length":                 contextLength,
			"configured_context_length":      settings.ContextLength,
			"writeback_source":               "memory_flow_service",
			"trigger":                        "message_threshold",
			"previous_trigger_message_count": state.LastTriggerMessageCount,
			"pending_message_count":          pending,
			"trigger_message_count":          total,
			"summary_review_count":           2,
		},
		RespectFilter: memoryWritebackBoolPtr(true),
		UserID:        strings.TrimSpace(event.UserID),
		GroupID:       strings.TrimSpace(event.GroupID),
	}
	kernel := s.kernel()
	if kernel == nil {
		return errors.New("记忆内核未就绪")
	}
	result, err := kernel.IngestSummary(ctx, params)
	if err != nil {
		return err
	}
	if !chatSummaryTruthy(result["success"]) {
		detail := chatSummaryString(result["detail"])
		if detail == "" {
			detail = "聊天摘要写回未成功"
		}
		return errors.New(detail)
	}
	s.mu.Lock()
	state.LastTriggerMessageCount = total
	state.LastTriggerTime = float64(s.now().Unix())
	s.mu.Unlock()
	zap.L().Info(fmt.Sprintf("聊天摘要自动写回成功: session_id=%s trigger=message_threshold total_messages=%d context_length=%d detail=%s",
		sessionID, total, contextLength, chatSummaryString(result["detail"])))
	return nil
}

func (s *ChatSummaryWritebackService) kernel() ChatSummaryWritebackKernel {
	if s.opts.Kernel == nil {
		return nil
	}
	return s.opts.Kernel()
}

func (s *ChatSummaryWritebackService) countMessages(ctx context.Context, sessionID string, endTime *time.Time) (int, error) {
	if s.opts.CountMessages == nil {
		return 0, errors.New("会话消息计数端口未接线")
	}
	count, err := s.opts.CountMessages(ctx, sessionID, endTime)
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

// stateFor 首次触发时恢复游标，对齐 Python _handle_message:559 的状态初始化
func (s *ChatSummaryWritebackService) stateFor(ctx context.Context, sessionID string, total int) *ChatSummaryWritebackState {
	s.mu.Lock()
	if state, exists := s.states[sessionID]; exists {
		s.mu.Unlock()
		return state
	}
	s.mu.Unlock()
	restored := LoadChatSummaryTriggerCount(ctx, s.kernel(), sessionID, total)
	state := &ChatSummaryWritebackState{LastTriggerMessageCount: restored}
	if restored > 0 {
		state.LastTriggerTime = float64(s.now().Unix())
	}
	s.mu.Lock()
	if existing, exists := s.states[sessionID]; exists {
		s.mu.Unlock()
		return existing
	}
	s.states[sessionID] = state
	s.mu.Unlock()
	return state
}

// LoadChatSummaryTriggerCount 恢复聊天摘要写回游标，对齐 Python _load_last_trigger_message_count:625
// 摘要段落存在时取段落元数据与检查点的较大值并截断到 total，异常一律退回 0
func LoadChatSummaryTriggerCount(ctx context.Context, kernel ChatSummaryWritebackKernel, sessionID string, total int) int {
	if kernel == nil {
		return 0
	}
	checkpoint, err := kernel.SummaryCheckpointCount(sessionID)
	if err != nil {
		zap.L().Debug("恢复聊天摘要写回游标失败: 检查点查询失败", zap.String("session_id", sessionID), zap.Error(err))
		return 0
	}
	paragraphs, err := kernel.ParagraphsBySource("chat_summary:" + sessionID)
	if err != nil {
		zap.L().Debug("恢复聊天摘要写回游标失败: 摘要段落查询失败", zap.String("session_id", sessionID), zap.Error(err))
		return 0
	}
	if len(paragraphs) == 0 {
		return minChatSummaryInt(total, checkpoint)
	}
	trigger := 0
	for _, paragraph := range paragraphs {
		metadata := chatSummaryParagraphMetadata(paragraph)
		chatID := sessionID
		if value, exists := metadata["chat_id"]; exists {
			chatID = chatSummaryString(value)
		}
		if chatID != sessionID {
			continue
		}
		trigger = maxChatSummaryInt(trigger, chatSummaryPositiveInt(metadata["trigger_message_count"]))
	}
	highest := maxChatSummaryInt(trigger, checkpoint)
	if highest > 0 {
		return minChatSummaryInt(total, highest)
	}
	return total
}

// chatSummaryParagraphMetadata 解析段落元数据，支持 map、JSON 字符串与字节
// 对齐 Python _paragraph_metadata:674
func chatSummaryParagraphMetadata(paragraph map[string]any) map[string]any {
	if paragraph == nil {
		return map[string]any{}
	}
	raw, exists := paragraph["metadata"]
	if !exists {
		return map[string]any{}
	}
	switch typed := raw.(type) {
	case map[string]any:
		return typed
	case string:
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return map[string]any{}
		}
		return decoded
	case []byte:
		decoded := map[string]any{}
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return map[string]any{}
		}
		return decoded
	default:
		return map[string]any{}
	}
}

// chatSummaryPositiveInt 对齐 Python _coerce_positive_int:692
func chatSummaryPositiveInt(value any) int {
	number := 0
	switch typed := value.(type) {
	case int:
		number = typed
	case int32:
		number = int(typed)
	case int64:
		number = int(typed)
	case float32:
		number = int(typed)
	case float64:
		number = int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			number = parsed
		}
	}
	if number < 0 {
		return 0
	}
	return number
}

func chatSummaryString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

// chatSummaryTruthy 对齐 Python bool(result.get("success"))
func chatSummaryTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case nil:
		return false
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		return trimmed != "" && trimmed != "0" && trimmed != "false" && trimmed != "no"
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	default:
		return true
	}
}

// effectiveChatSummaryContextLength 对齐 Python _effective_context_length:746
func effectiveChatSummaryContextLength(configured int, pending int) int {
	return minChatSummaryInt(maxChatSummaryInt(1, configured), maxChatSummaryInt(1, pending))
}

func minChatSummaryInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxChatSummaryInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

// memoryWritebackBoolPtr 返回 bool 指针，供内核参数表达「显式设置」
func memoryWritebackBoolPtr(value bool) *bool {
	return &value
}
