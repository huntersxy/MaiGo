package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/maisaka/focus"
	"MaiGo/internal/prompt"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// SetMaisakaBackgroundLoop 运行时切换会话级后台 RunLoop；默认使用单轮同步执行
func (a *Agent) SetMaisakaBackgroundLoop(enabled bool) {
	if a == nil {
		return
	}
	a.maisakaBackgroundLoop.Store(enabled)
}

// MaisakaBackgroundLoopEnabled 当前是否启用后台 RunLoop
func (a *Agent) MaisakaBackgroundLoopEnabled() bool {
	return a != nil && a.maisakaBackgroundLoop.Load()
}

// maisakaPromptSource 把仓库 prompt 模板接到 Maisaka 对话循环
type maisakaPromptSource struct {
	once    sync.Once
	manager *prompt.Manager
	err     error
	BotName string
	// Dir 实例目录，用于加载 data/custom_prompts 覆盖
	Dir string
}

func (s *maisakaPromptSource) initialize() error {
	s.once.Do(func() {
		manager, err := prompt.NewManager()
		if err != nil {
			s.err = err
			return
		}
		if dir := strings.TrimSpace(s.Dir); dir != "" {
			if loadErr := manager.LoadFromDir(prompt.CustomPromptsRoot(dir)); loadErr != nil {
				zap.L().Warn("加载实例 Maisaka 提示词失败", zap.Error(loadErr))
			}
		}
		s.manager = manager
	})
	return s.err
}

// Get 读取一个已合并的 prompt 模板
func (s *maisakaPromptSource) Get(name string) (string, error) {
	if name == "bot_name" && strings.TrimSpace(s.BotName) != "" {
		return strings.TrimSpace(s.BotName), nil
	}
	if err := s.initialize(); err != nil {
		return "", err
	}
	if s.manager == nil {
		return "", fmt.Errorf("Maisaka prompt 管理器未初始化")
	}
	return s.manager.Get(name)
}

// Render 按模板变量渲染，支持 {{ }} 转义与嵌套模板
func (s *maisakaPromptSource) Render(name string, vars map[string]string) (string, error) {
	if err := s.initialize(); err != nil {
		return "", err
	}
	if s.manager == nil {
		return "", fmt.Errorf("Maisaka prompt 管理器未初始化")
	}
	if _, exists := vars["bot_name"]; !exists && strings.TrimSpace(s.BotName) != "" {
		copied := make(map[string]string, len(vars)+1)
		for key, value := range vars {
			copied[key] = value
		}
		copied["bot_name"] = strings.TrimSpace(s.BotName)
		vars = copied
	}
	return s.manager.Render(name, vars)
}

// newMaisakaPromptSource 构造带实例自定义提示词的 Maisaka prompt 来源
func (a *Agent) newMaisakaPromptSource() *maisakaPromptSource {
	source := &maisakaPromptSource{BotName: a.botName()}
	if instance := a.instanceConfigSnapshot(); instance != nil {
		source.Dir = instance.Dir
	}
	return source
}

// runMaisakaTurn 使用 Maisaka 运行时执行一轮完整思考，返回是否已接管
func (a *Agent) runMaisakaTurn(ctx context.Context, kind string, targetID int64, session *Session, messages []*schema.AgenticMessage) bool {
	runtime := session.MaisakaRuntime()
	generation := generationFrom(ctx)
	if runtime == nil || runtime.Engine() == nil || generation == nil || generation.model == nil {
		return false
	}
	lightweight := a.isLightweightPrivateSession(kind, targetID)
	if lightweight {
		// 轻量私聊优先于 Focus，不进入共享上下文。
		a.detachFocusSession(ctx, session, runtime)
	} else if a.routeFocusMessage(ctx, kind, targetID, session, runtime) {
		return true
	}
	session.mu.Lock()
	session.maisakaFocusGroup = nil
	session.mu.Unlock()
	runtime.SetLightweightPrivate(lightweight)
	if a.maisakaBackgroundLoop.Load() {
		if loop := a.ensureMaisakaTurnLoop(session); loop != nil {
			jobGeneration := generation
			job := func(context.Context) error {
				if jobGeneration != nil {
					jobGeneration.wg.Add(1)
					defer jobGeneration.wg.Done()
				}
				if a.stopping.Load() || a.ctx.Err() != nil {
					return nil
				}
				jobCtx, cancel := a.persistenceContext()
				defer cancel()
				if jobGeneration != nil {
					jobCtx = withGeneration(jobCtx, jobGeneration)
				}
				handled := a.executeMaisakaTurn(jobCtx, kind, targetID, session, messages)
				if kind == memory.ConversationKindGroup {
					replied := false
					if tc := tools.GetToolContext(jobCtx); tc != nil && tc.RepliedThisTurn != nil {
						replied = *tc.RepliedThisTurn
					}
					a.turnSchedule.recordGroupCycleResult(targetID, replied)
				}
				if !handled {
					return fmt.Errorf("Maisaka 后台轮次执行前置条件失效")
				}
				return nil
			}
			if loop.Submit(ctx, job) {
				return true
			}
		}
	}
	return a.executeMaisakaTurn(ctx, kind, targetID, session, messages)
}

// stopMaisakaTurnLoops 停机时等待所有会话后台 RunLoop 结束
func (a *Agent) stopMaisakaTurnLoops() {
	if a == nil || a.sessions == nil {
		return
	}
	for _, session := range a.sessions.all() {
		if session == nil {
			continue
		}
		session.mu.Lock()
		loop := session.maisakaLoop
		session.mu.Unlock()
		if loop != nil {
			loop.Stop()
		}
	}
}

// ensureMaisakaTurnLoop 返回会话级后台 RunLoop，首次使用时创建并启动
func (a *Agent) ensureMaisakaTurnLoop(session *Session) *maisaka.TurnLoop {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	loop := session.maisakaLoop
	if loop == nil {
		loop = maisaka.NewTurnLoop(a.ctx, 4)
		session.maisakaLoop = loop
	}
	session.mu.Unlock()
	loop.Start()
	return loop
}

// executeMaisakaTurn 同步执行一轮 Maisaka 思考并写回可见水位
func (a *Agent) executeMaisakaTurn(ctx context.Context, kind string, targetID int64, session *Session, messages []*schema.AgenticMessage) bool {
	runtime := session.MaisakaRuntime()
	generation := generationFrom(ctx)
	if runtime == nil || runtime.Engine() == nil || generation == nil || generation.model == nil {
		return false
	}
	history := agenticMessagesToMaisakaHistory(messages)
	history = a.injectMaisakaSummary(session, history)
	if len(history) == 0 {
		return false
	}
	runtime.SetLightweightPrivate(a.isLightweightPrivateSession(kind, targetID))
	promptSource := a.newMaisakaPromptSource()
	plannerModel := generation.plannerModel
	plannerName := generation.plannerModelName
	if plannerModel == nil {
		plannerModel = generation.model
		plannerName = generation.modelName
	}
	loop := maisaka.NewChatLoopService(runtime.SessionID(), runtime.IsGroupSession(), promptSource, &maisakaChatClient{
		model:     plannerModel,
		modelName: plannerName,
	})
	if generation.fastModel != nil {
		loop.JudgeClient = &maisakaChatClient{model: generation.fastModel, modelName: generation.fastModelName}
	}
	loop.MaxContextSize = runtime.MaxContextSize()
	// 对齐 Python _resolve_enable_visual_message("planner")：按 planner 任务的模型视觉能力决定
	loop.EnableVisualMessage = a.plannerVisualEnabled()
	loop.MeasureSections = a.measurePlannerSections
	loop.AttentionDriftBlock = a.buildAttentionDriftBlock(runtime.IsGroupSession())
	a.configureMaisakaChatLoop(loop, runtime.IsGroupSession(), kind, targetID)
	runtime.SetChatLoop(loop)

	runtime.SetHistory(history)
	pending := session.PendingMessages()
	trigger := history[len(history)-1]
	runtime.BeginTurnTranscript()
	if err := runtime.Engine().RunTurnOnce(ctx, &trigger, nil, ""); err != nil {
		runtime.DrainTurnTranscript()
		zap.L().Error("Maisaka 主循环思考失败",
			zap.String("conversation_kind", kind),
			zap.Int64("target_id", targetID),
			zap.Error(err))
		return true
	}
	persistedIDs := a.persistMaisakaHistory(ctx, session, runtime.DrainTurnTranscript())
	a.captureMaisakaSummary(session, runtime.History())
	if summaryID := a.persistMaisakaSummary(ctx, session); summaryID != 0 {
		persistedIDs = append(persistedIDs, summaryID)
	}
	a.markMaisakaTurnVisible(ctx, session, pending, persistedIDs)
	if session != nil {
		a.saveFocusCheckpoint(ctx, session)
	}
	return true
}

// persistMaisakaSummary 把最新上下文摘要写入 transcript，跨重启恢复
func (a *Agent) persistMaisakaSummary(ctx context.Context, session *Session) uint {
	if session == nil {
		return 0
	}
	session.mu.Lock()
	summary := session.maisakaSummary
	persisted := session.maisakaSummaryPersisted
	session.mu.Unlock()
	if summary == nil || strings.TrimSpace(summary.Content) == "" || summary.Content == persisted {
		return 0
	}
	id, err := session.AppendSummary(ctx, maisakaTranscriptRunID, *summary)
	if err != nil {
		zap.L().Warn("Maisaka 上下文摘要持久化失败", zap.Error(err))
		return 0
	}
	return id
}

// injectMaisakaSummary 把上一轮压缩摘要前置到装配历史，避免跨轮丢失
func (a *Agent) injectMaisakaSummary(session *Session, history []maisaka.ContextMessage) []maisaka.ContextMessage {
	if session == nil {
		return history
	}
	session.mu.Lock()
	summary := session.maisakaSummary
	session.mu.Unlock()
	if summary == nil {
		return history
	}
	for _, message := range history {
		if message.Source == maisaka.ContextSummarySource && strings.TrimSpace(message.Content) == strings.TrimSpace(summary.Content) {
			return history
		}
	}
	out := make([]maisaka.ContextMessage, 0, len(history)+1)
	out = append(out, *summary)
	out = append(out, history...)
	return out
}

// captureMaisakaSummary 缓存本轮最新的上下文摘要，供下一次装配继续使用
func (a *Agent) captureMaisakaSummary(session *Session, history []maisaka.ContextMessage) {
	if session == nil {
		return
	}
	var latest *maisaka.ContextMessage
	for i := range history {
		if history[i].Source == maisaka.ContextSummarySource {
			message := history[i]
			latest = &message
		}
	}
	if latest == nil {
		return
	}
	session.mu.Lock()
	session.maisakaSummary = latest
	session.mu.Unlock()
}

// maisakaTranscriptRunID Maisaka 工具轨迹统一使用的 run 编号，保证 assistant/tool 配对
const maisakaTranscriptRunID = "maisaka"

// persistMaisakaHistory 把本轮新增的 assistant 与工具结果按顺序写回 transcript
func (a *Agent) persistMaisakaHistory(ctx context.Context, session *Session, history []maisaka.ContextMessage) []uint {
	ids := make([]uint, 0, 4)
	if session == nil || len(history) == 0 || session.memory == nil {
		return ids
	}
	for _, message := range history {
		switch message.Role {
		case maisaka.RoleAssistant:
			if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
				continue
			}
			msg := maisakaAssistantSchemaMessage(message)
			if msg == nil {
				continue
			}
			entry, err := session.AppendAssistant(ctx, maisakaTranscriptRunID, msg)
			if err != nil {
				zap.L().Warn("保存 Maisaka assistant 消息失败", zap.Error(err))
				continue
			}
			if entry != nil {
				ids = append(ids, entry.ID)
			}
		case maisaka.RoleTool:
			if strings.TrimSpace(message.ToolCallID) == "" {
				continue
			}
			payload, err := sonic.Marshal(&schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				Extra: map[string]any{
					"source": "tool_result", "tool_status": "succeeded",
				},
				ContentBlocks: []*schema.ContentBlock{{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: message.ToolCallID,
						Name:   message.ToolName,
						Content: []*schema.FunctionToolResultContentBlock{{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: message.Content},
						}},
					},
				}},
			})
			if err != nil {
				continue
			}
			entry := &memory.TranscriptEntry{
				ConversationKind: session.kind, TargetID: session.targetID, EntryKind: "tool",
				RunID: maisakaTranscriptRunID, ToolCallID: message.ToolCallID, ToolName: message.ToolName,
				ToolStatus: "succeeded", Payload: payload,
			}
			if err := session.memory.AppendTranscript(ctx, entry); err != nil {
				zap.L().Warn("保存 Maisaka 工具结果失败", zap.Error(err))
				continue
			}
			ids = append(ids, entry.ID)
			session.mu.Lock()
			session.seen = append(session.seen, sessionEntry{msg: &schema.AgenticMessage{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{{
					Type: schema.ContentBlockTypeFunctionToolResult,
					FunctionToolResult: &schema.FunctionToolResult{
						CallID: message.ToolCallID, Name: message.ToolName,
						Content: []*schema.FunctionToolResultContentBlock{{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: message.Content},
						}},
					},
				}},
			}, transcriptID: entry.ID})
			session.mu.Unlock()
		}
	}
	return ids
}

// markMaisakaTurnVisible 提交本批输入与 assistant 的可见水位，消费待处理队列
func (a *Agent) markMaisakaTurnVisible(ctx context.Context, session *Session, pending []*sessionUser, assistantIDs []uint) {
	if session == nil || session.memory == nil {
		return
	}
	ids := make([]uint, 0, len(pending)+len(assistantIDs))
	ids = append(ids, assistantIDs...)
	for _, user := range pending {
		if user != nil && user.TranscriptID != 0 {
			ids = append(ids, user.TranscriptID)
		}
	}
	if len(ids) == 0 {
		return
	}
	seqs, err := session.memory.MarkVisibleAndPersistReceipts(ctx, session.kind, session.targetID, ids, nil)
	if err != nil {
		zap.L().Warn("标记 Maisaka 轮次可见失败", zap.Error(err))
		return
	}
	session.ApplyVisibleSeqs(seqs)
	session.MarkInjected(maxPendingSeq(pending))
}

func maisakaAssistantSchemaMessage(message maisaka.ContextMessage) *schema.AgenticMessage {
	blocks := make([]*schema.ContentBlock, 0, len(message.ToolCalls)+1)
	if strings.TrimSpace(message.Content) != "" {
		blocks = append(blocks, schema.NewContentBlock(&schema.AssistantGenText{Text: message.Content}))
	}
	for _, call := range message.ToolCalls {
		blocks = append(blocks, schema.NewContentBlock(&schema.FunctionToolCall{
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: encodeToolArguments(call.Arguments),
		}))
	}
	if len(blocks) == 0 {
		return nil
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks}
}

// agenticMessagesToMaisakaHistory 把会话装配结果转换为 Maisaka 内部历史
func agenticMessagesToMaisakaHistory(messages []*schema.AgenticMessage) []maisaka.ContextMessage {
	history := make([]maisaka.ContextMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		converted, ok := agenticMessageToMaisaka(message)
		if ok {
			history = append(history, converted...)
		}
	}
	return history
}

func agenticMessageToMaisaka(message *schema.AgenticMessage) ([]maisaka.ContextMessage, bool) {
	if message == nil {
		return nil, false
	}
	// 非 assemble 出站口也先解析 deferred 占位，避免 ImageComponent 只拿到路径标记
	message = resolveDeferredMediaBlocks([]*schema.AgenticMessage{message})[0]
	if message == nil {
		return nil, false
	}
	switch message.Role {
	case schema.AgenticRoleTypeSystem:
		return nil, false
	case schema.AgenticRoleTypeAssistant:
		// 模型输出不计入真实聊天条数，对齐 Python ModelOutputContextMessage
		out := maisaka.ContextMessage{Role: maisaka.RoleAssistant}
		for _, block := range message.ContentBlocks {
			if block == nil {
				continue
			}
			switch block.Type {
			case schema.ContentBlockTypeAssistantGenText:
				if block.AssistantGenText != nil {
					out.Content += block.AssistantGenText.Text
				}
			case schema.ContentBlockTypeReasoning:
				if block.Reasoning != nil {
					out.Reasoning += block.Reasoning.Text
				}
			case schema.ContentBlockTypeFunctionToolCall:
				if block.FunctionToolCall != nil {
					out.ToolCalls = append(out.ToolCalls, maisaka.ToolCall{
						CallID:    block.FunctionToolCall.CallID,
						Name:      block.FunctionToolCall.Name,
						Arguments: parseToolArguments(block.FunctionToolCall.Arguments),
					})
				}
			}
		}
		out.Content = strings.TrimSpace(out.Content)
		if out.Content == "" && len(out.ToolCalls) == 0 {
			return nil, false
		}
		return []maisaka.ContextMessage{out}, true
	default:
		converted := make([]maisaka.ContextMessage, 0, 1)
		textParts := make([]string, 0, 1)
		images := make([]maisaka.ImageComponent, 0, 1)
		for _, block := range message.ContentBlocks {
			if block == nil {
				continue
			}
			if block.FunctionToolResult != nil {
				result := block.FunctionToolResult
				// 工具结果不计入真实聊天条数，对齐 Python ToolResultMessage
				converted = append(converted, maisaka.ContextMessage{
					Role:       maisaka.RoleTool,
					Content:    toolResultContentText(result),
					ToolCallID: result.CallID,
					ToolName:   result.Name,
					Source:     maisaka.SourceToolResult,
				})
				continue
			}
			if block.UserInputText != nil {
				textParts = append(textParts, block.UserInputText.Text)
			}
			if block.UserInputImage != nil {
				image := block.UserInputImage
				hash := strings.TrimSpace(image.Base64Data)
				if hash == "" {
					hash = strings.TrimSpace(image.URL)
				}
				if hash != "" {
					images = append(images, maisaka.ImageComponent{
						MediaRef: hash, Format: strings.TrimSpace(image.MIMEType),
					})
				}
			}
		}
		text := strings.TrimSpace(strings.Join(textParts, "\n"))
		if text != "" || len(images) > 0 {
			convertedMessage := maisaka.ContextMessage{
				Role:           maisaka.RoleUser,
				Content:        text,
				Source:         maisaka.SourceUser,
				CountInContext: true,
				Metadata:       sessionExtraMetadata(message.Extra),
				Images:         images,
			}
			if raw, ok := convertedMessage.Metadata["message_id"].(string); ok {
				convertedMessage.MessageID = raw
			}
			if raw, ok := convertedMessage.Metadata["sender_id"].(string); ok {
				convertedMessage.UserID = raw
			}
			if raw, ok := convertedMessage.Metadata["platform"].(string); ok {
				convertedMessage.Platform = raw
			}
			if raw, ok := extraInt64(message.Extra, "timestamp_unix_ms"); ok {
				convertedMessage.Timestamp = time.UnixMilli(raw)
			}
			if raw, ok := extraInt64(message.Extra, "received_at_unix_ms"); ok {
				received := time.UnixMilli(raw)
				convertedMessage.ReceivedAt = &received
			}
			converted = append(converted, convertedMessage)
		}
		return converted, len(converted) > 0
	}
}

// sessionExtraMetadata 把模型消息 Extra 投影成 Maisaka 工具可见的元数据
func sessionExtraMetadata(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]any, len(extra))
	for _, key := range []string{"message_id", "sender_name", "sender_cardname", "nickname", "platform", "is_mentioned", "is_notify", "bot_platform_nickname", "quote_ids", "reply_to", "quote_sender", "quote_content"} {
		if value, ok := extra[key]; ok {
			out[key] = value
		}
	}
	if raw, ok := extraInt64(extra, "sender_id"); ok {
		out["sender_id"] = strconv.FormatInt(raw, 10)
	}
	return out
}

func extraInt64(extra map[string]any, key string) (int64, bool) {
	raw, ok := extra[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func toolResultContentText(result *schema.FunctionToolResult) string {
	if result == nil {
		return ""
	}
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block == nil {
			continue
		}
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// focusGroupRegistry 每个 Agent 的 Focus 共享组管理器与成员登记
type focusGroupRegistry struct {
	agent    *Agent
	manager  *focus.FocusModeManager
	mu       sync.Mutex
	sessions map[string]*Session
}

var agentFocusGroups sync.Map // *Agent -> *focusGroupRegistry

func (a *Agent) focusGroupRegistry() *focusGroupRegistry {
	if value, ok := agentFocusGroups.Load(a); ok {
		return value.(*focusGroupRegistry)
	}
	registry := &focusGroupRegistry{agent: a, sessions: map[string]*Session{}}
	registry.manager = focus.NewFocusModeManager(focus.ManagerOptions{
		Settings: func() focus.Settings {
			settings, _, ok := a.focusExperimentalSnapshot()
			if !ok {
				return focus.Settings{}
			}
			return settings
		},
		Lookup:      registry,
		NewGroup:    func(scopeID string) *focus.GroupRuntime { return a.newFocusGroupRuntime(scopeID) },
		OpenSession: registry.OpenSession,
	})
	actual, _ := agentFocusGroups.LoadOrStore(a, registry)
	return actual.(*focusGroupRegistry)
}

func (r *focusGroupRegistry) register(sessionID string, session *Session) {
	if sessionID == "" || session == nil {
		return
	}
	r.mu.Lock()
	r.sessions[sessionID] = session
	r.mu.Unlock()
}

// SessionInfo 实现 focus.SessionLookup
func (r *focusGroupRegistry) SessionInfo(sessionID string) (focus.SessionInfo, bool) {
	r.mu.Lock()
	session := r.sessions[sessionID]
	r.mu.Unlock()
	if session == nil {
		return focus.SessionInfo{}, false
	}
	platform := ""
	if r.agent != nil {
		platform = r.agent.conversationPlatform(session.kind, session.targetID)
	}
	return session.focusInfoSnapshot(platform), true
}

// focusInfoSnapshot 使用会话自身字段构造 Focus 投影，避免后台协程依赖请求上下文
func (s *Session) focusInfoSnapshot(platform string) focus.SessionInfo {
	info := focus.SessionInfo{SessionID: s.MaisakaRuntimeSessionID(), IsGroup: s.kind == memory.ConversationKindGroup, Platform: platform}
	if s.kind == memory.ConversationKindGroup {
		info.GroupID = strconv.FormatInt(s.targetID, 10)
	} else {
		info.UserID = strconv.FormatInt(s.targetID, 10)
	}
	return info
}

// MaisakaRuntimeSessionID 返回已缓存运行时的会话编号
func (s *Session) MaisakaRuntimeSessionID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	runtime := s.maisakaRuntime
	s.mu.Unlock()
	if runtime == nil {
		return ""
	}
	return runtime.SessionID()
}

// MaisakaRuntimeSessionName 返回已缓存运行时的会话名
func (s *Session) MaisakaRuntimeSessionName() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	runtime := s.maisakaRuntime
	s.mu.Unlock()
	if runtime == nil {
		return ""
	}
	return runtime.SessionName()
}

// newFocusGroupRuntime 构造共享组 Planner、工具路由与检查点
func (a *Agent) newFocusGroupRuntime(scopeID string) *focus.GroupRuntime {
	options := focus.GroupOptions{
		MaxContextSize: func() int { return a.maisakaMaxContextSize(true) },
		Summary:        a.buildFocusSummary,
		StoreRoot:      focus.DefaultCheckpointRoot,
		Prompts:        a.newMaisakaPromptSource(),
		ToolSearchTailLoading: func() bool {
			settings, _, ok := a.focusExperimentalSnapshot()
			return ok && settings.ToolSearchTailLoading
		},
		Planner: func(ctx context.Context, request focus.PlannerRequest) (maisaka.ChatResponse, error) {
			generation := a.currentGeneration()
			if generation == nil || generation.model == nil {
				return maisaka.ChatResponse{}, fmt.Errorf("Focus 共享组缺少 Planner 模型")
			}
			loop := maisaka.NewChatLoopService(scopeID, true, a.newMaisakaPromptSource(), &maisakaChatClient{
				model: generation.model, modelName: generation.modelName,
			})
			if generation.fastModel != nil {
				loop.JudgeClient = &maisakaChatClient{model: generation.fastModel, modelName: generation.fastModelName}
			}
			loop.LogicalTurnID = request.LogicalTurnID
			loop.MaxContextSize = request.MaxContextSize
			loop.FocusMode = true
			loop.EnableVisualMessage = a.plannerVisualEnabled()
			loop.MeasureSections = a.measurePlannerSections
			loop.AttentionDriftBlock = a.buildAttentionDriftBlock(true)
			a.configureMaisakaChatLoop(loop, true, "", 0)
			response, err := loop.ChatLoopStep(ctx, request.History, "planner", request.MaxContextSize, request.Tools, "", loop.BuildPlannerFinalUserReminder(), request.InjectedUserMessages, request.TailUserMessages)
			if err == nil && request.CaptureSnapshot != nil {
				request.CaptureSnapshot(loop.PlannerSnapshot())
			}
			return response, err
		},
		Emitter: focusEmitterAdapter{},
	}
	return focus.NewGroupRuntime(scopeID, options)
}

type focusEmitterAdapter struct{}

func (focusEmitterAdapter) PlannerSnapshot(eventType string, info focus.PlannerSnapshotInfo) {
	zap.L().Debug("Focus 共享组 Planner 快照", zap.String("scope_id", info.ScopeID), zap.String("event", eventType), zap.String("end_reason", info.EndReason), zap.Int("cycle", info.Cycle))
}

func (focusEmitterAdapter) FlowStep(sessionID, runID string, cycle int, step, title, detail string) {
	zap.L().Debug("Focus 共享组流程", zap.String("session_id", sessionID), zap.String("step", step), zap.String("title", title))
}

// focusGroupMember 把 Maisaka Runtime 适配为 Focus 共享组成员
type focusGroupMember struct {
	agent     *Agent
	runtime   *maisaka.Runtime
	session   *Session
	sessionID string
	group     *focus.GroupRuntime
}

func (m *focusGroupMember) SessionID() string                     { return m.sessionID }
func (m *focusGroupMember) SessionName() string                   { return m.runtime.SessionName() }
func (m *focusGroupMember) IsGroupSession() bool                  { return m.runtime.IsGroupSession() }
func (m *focusGroupMember) IsRunning() bool                       { return m.runtime.IsRunning() }
func (m *focusGroupMember) MaxContextSize() int                   { return m.runtime.MaxContextSize() }
func (m *focusGroupMember) ChatHistory() []maisaka.ContextMessage { return m.runtime.History() }
func (m *focusGroupMember) TrimChatHistory(keep int)              { m.runtime.TrimHistory(keep) }
func (m *focusGroupMember) CollectPendingMessages() []maisaka.ContextMessage {
	return m.runtime.CollectPendingMessages()
}
func (m *focusGroupMember) ClearForcedTurnState()      { m.runtime.ClearForcedTurnState() }
func (m *focusGroupMember) EnterStopState()            { m.runtime.EnterStopState() }
func (m *focusGroupMember) EnterRunningState()         { m.runtime.EnterRunningState() }
func (m *focusGroupMember) SetAgentState(state string) { m.runtime.SetAgentState(state) }
func (m *focusGroupMember) UpdateStageStatus(stage, detail string) {
	m.runtime.UpdateStageStatus(stage, detail)
}
func (m *focusGroupMember) WaitForMessageQuietPeriod(ctx context.Context) {
	m.runtime.WaitForMessageQuietPeriod(ctx)
}
func (m *focusGroupMember) AppendChatHistory(messages ...maisaka.ContextMessage) {
	for _, message := range messages {
		m.runtime.AppendHistory(message)
	}
}
func (m *focusGroupMember) FindSourceMessageByID(messageID string) (maisaka.ContextMessage, bool) {
	return m.runtime.FindSourceMessageByID(messageID)
}
func (m *focusGroupMember) ListTools(ctx context.Context) ([]maisaka.ToolSpec, error) {
	return m.runtime.ListTools(ctx)
}
func (m *focusGroupMember) GetToolSpec(ctx context.Context, name string) (*maisaka.ToolSpec, bool, error) {
	return m.runtime.GetToolSpec(ctx, name)
}
func (m *focusGroupMember) InvokeTool(ctx context.Context, invocation maisaka.ToolInvocation) maisaka.ToolExecutionResult {
	return m.runtime.InvokeTool(ctx, invocation)
}
func (m *focusGroupMember) ExpandMessageIDAliases(arguments map[string]any) map[string]any {
	return m.runtime.ExpandMessageIDAliases(arguments)
}
func (m *focusGroupMember) UpdateDeferredToolSpecs(specs []maisaka.ToolSpec) {
	m.runtime.UpdateDeferredToolSpecs(specs)
}
func (m *focusGroupMember) DiscoveredToolNames() map[string]struct{} {
	return m.runtime.DiscoveredToolNames()
}
func (m *focusGroupMember) SetFocusGroup(group *focus.GroupRuntime) { m.group = group }
func (m *focusGroupMember) FocusGroup() *focus.GroupRuntime         { return m.group }

// CondenseToolResult 对齐 Python focus/group_runtime.py：共享组工具结果按成员运行时配置整理
func (m *focusGroupMember) CondenseToolResult(ctx context.Context, call maisaka.ToolCall, result maisaka.ToolExecutionResult, latestThought string) string {
	if m.runtime == nil {
		return ""
	}
	alreadyPreviewed, _ := result.Metadata["web_result_preview"].(bool)
	if !m.runtime.ShouldCondense(call.Name, result.Content, result.Success, alreadyPreviewed) {
		return ""
	}
	m.runtime.UpdateStageStatus("工具结果整理 · "+call.Name, fmt.Sprintf("原始结果 %d 字", len([]rune(result.Content))))
	condensed := maisaka.CondenseToolResult(ctx, m.runtime, call, result.Content, latestThought)
	if condensed == result.Content {
		return ""
	}
	return condensed
}

// CheckpointMember 供 Focus 检查点保存重建会话所需的平台与目标
func (m *focusGroupMember) CheckpointMember() focus.CheckpointMember {
	kind := memory.ConversationKindPrivate
	if m.runtime != nil && m.runtime.IsGroupSession() {
		kind = memory.ConversationKindGroup
	}
	platform := ""
	targetID := int64(0)
	if m.session != nil {
		targetID = m.session.targetID
		if m.agent != nil {
			platform = m.agent.conversationPlatform(m.session.kind, m.session.targetID)
		}
	}
	return focus.CheckpointMember{
		SessionID: m.sessionID, SessionName: m.SessionName(), Kind: kind,
		Platform: platform, TargetID: targetID, IsGroup: kind == memory.ConversationKindGroup,
	}
}

// routeFocusMessage 把启用 Focus 的共享组成员消息交给唯一共享 Planner
func (a *Agent) routeFocusMessage(ctx context.Context, kind string, targetID int64, session *Session, runtime *maisaka.Runtime) bool {
	if a == nil || session == nil || runtime == nil {
		return false
	}
	settings, _, ok := a.focusExperimentalSnapshot()
	if !ok {
		a.detachFocusSession(ctx, session, runtime)
		return false
	}
	info, infoOK := a.focusSessionInfo(ctx, session)
	enabled := infoOK && focus.IsEnabledForSession(settings, info)
	registry := a.focusGroupRegistry()
	registry.register(runtime.SessionID(), session)
	session.mu.Lock()
	oldGroup := session.maisakaFocusGroup
	session.mu.Unlock()
	member := &focusGroupMember{agent: a, runtime: runtime, session: session, sessionID: runtime.SessionID(), group: oldGroup}
	var carried []maisaka.ContextMessage
	if oldGroup != nil {
		if !enabled {
			if err := registry.manager.Detach(ctx, member, true); err != nil {
				zap.L().Warn("退出 Focus 共享组失败", zap.Error(err))
				return false
			}
			a.setFocusSessionGroup(session, nil)
			return false
		}
		scopeID, scopeErr := registry.manager.GetFocusScopeKey(runtime.SessionID())
		if scopeErr != nil {
			zap.L().Warn("解析 Focus 共享组失败", zap.Error(scopeErr))
			return false
		}
		if oldGroup.ScopeID() != scopeID {
			carried, _ = oldGroup.UnprocessedMessages(runtime.SessionID())
			if err := registry.manager.Detach(ctx, member, true); err != nil {
				zap.L().Warn("Focus 共享组切换时退出旧组失败", zap.Error(err))
				return false
			}
			a.setFocusSessionGroup(session, nil)
		}
	}
	if !enabled {
		return false
	}
	if err := registry.manager.Attach(ctx, member); err != nil {
		zap.L().Warn("接入 Focus 共享组失败", zap.Error(err))
		return false
	}
	group := member.FocusGroup()
	if group == nil {
		return false
	}
	pending := session.PendingMessages()
	seen := map[string]struct{}{}
	register := func(message maisaka.ContextMessage) {
		if message.MessageID != "" {
			if _, dup := seen[message.MessageID]; dup {
				return
			}
			seen[message.MessageID] = struct{}{}
		}
		if err := group.RegisterMessage(ctx, member, message); err != nil {
			zap.L().Warn("登记 Focus 消息失败", zap.Error(err))
		}
	}
	for _, message := range carried {
		register(message)
	}
	for _, user := range pending {
		if user == nil || user.IsAgentMessage {
			continue
		}
		messageID := ""
		if user.MessageID != 0 {
			messageID = strconv.FormatInt(user.MessageID, 10)
		}
		received := user.ReceivedAt
		register(maisaka.ContextMessage{
			Role: maisaka.RoleUser, Content: firstNonEmptyString(user.Text, user.Content),
			Source: maisaka.SourceUser, Timestamp: user.At, ReceivedAt: &received,
			CountInContext: true, IsCommand: user.IsCommand, MessageID: messageID, SessionID: runtime.SessionID(),
			UserID: strconv.FormatInt(user.SenderID, 10), Platform: a.conversationPlatform(kind, targetID),
			Metadata: map[string]any{"is_at": user.Mentioned, "sender_name": user.Nickname, "sender_id": strconv.FormatInt(user.SenderID, 10)},
		})
	}
	a.markMaisakaTurnVisible(ctx, session, pending, nil)
	a.setFocusSessionGroup(session, group)
	return true
}

// detachFocusSession 在 Focus 关闭或配置失效时退出旧共享组
func (a *Agent) detachFocusSession(ctx context.Context, session *Session, runtime *maisaka.Runtime) {
	if a == nil || session == nil || runtime == nil {
		return
	}
	session.mu.Lock()
	group := session.maisakaFocusGroup
	session.mu.Unlock()
	if group == nil {
		return
	}
	member := &focusGroupMember{agent: a, runtime: runtime, session: session, sessionID: runtime.SessionID(), group: group}
	registry := a.focusGroupRegistry()
	if err := registry.manager.Detach(ctx, member, true); err != nil {
		zap.L().Warn("退出 Focus 共享组失败", zap.Error(err))
		return
	}
	a.setFocusSessionGroup(session, nil)
}

// setFocusSessionGroup 更新会话当前接入的共享组
func (a *Agent) setFocusSessionGroup(session *Session, group *focus.GroupRuntime) {
	if session == nil {
		return
	}
	session.mu.Lock()
	session.maisakaFocusGroup = group
	session.mu.Unlock()
}
