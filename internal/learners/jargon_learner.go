package learners

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"maibot-go/internal/llm_models"
	"maibot-go/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// LearningRunner 黑话学习 LLM 批次执行器
type LearningRunner struct {
	Router  *llm.Router
	Prompts *prompt.Manager
}

// SourceItem 黑话学习素材，对齐 JargonLearningSourceItem
type SourceItem struct {
	MessageID  string
	Content    string
	Platform   string
	UserID     string
	Name       string
	GroupName  string
	SessionID  string
	IsBotSelf  bool
	SourceKind string
}

// JargonLearner 黑话学习器，对齐 src/learners/jargon_learner.py JargonLearner
type JargonLearner struct {
	SessionID                string
	MinMessagesForExtraction int
	pending                  *PendingLearningBuffer[SourceItem]
	LearningStatus           string
	LearningDetail           string
	LearningRetryBlocked     bool
	miner                    *JargonMiner
	registry                 *Registry

	mu               sync.Mutex
	learningContents []string
}

func NewJargonLearner(sessionID string) *JargonLearner {
	return &JargonLearner{
		SessionID:                sessionID,
		MinMessagesForExtraction: DefaultMinMessagesForExtraction,
		pending:                  NewPendingLearningBuffer(func(item SourceItem) string { return item.MessageID }),
		LearningStatus:           "pending",
	}
}

// NewLearnerForRegistry 绑定指定 Registry，并复用同一会话的黑话矿工缓存
func NewJargonLearnerForRegistry(reg *Registry, sessionID string) *JargonLearner {
	if reg == nil {
		reg = DefaultRegistry()
	}
	learner := NewJargonLearner(sessionID)
	learner.registry = reg
	learner.SetMiner(NewJargonMinerForRegistry(reg, sessionID, sessionID))
	return learner
}

// SetMiner 复用调用方的黑话矿工缓存，对齐 learn_from_context_messages(jargon_miner)
func (l *JargonLearner) SetMiner(miner *JargonMiner) {
	if l == nil {
		return
	}
	l.miner = miner
}

// LearningContents 返回最近一次批次实际处理的词条，供裁切学习进度卡片展示
func (l *JargonLearner) LearningContents() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.learningContents...)
}

func (l *JargonLearner) resetLearningContents() {
	l.mu.Lock()
	l.learningContents = nil
	l.mu.Unlock()
}

func (l *JargonLearner) appendLearningContents(contents []string) {
	if len(contents) == 0 {
		return
	}
	l.mu.Lock()
	l.learningContents = append(l.learningContents, contents...)
	l.mu.Unlock()
}

// CollectContextMessages 收集裁切上下文中的真实聊天消息
func (l *JargonLearner) CollectContextMessages(messages []ContextMessage) {
	items := make([]SourceItem, 0, len(messages))
	for _, message := range messages {
		if !isLearnableContextMessage(message) {
			continue
		}
		text := strings.TrimSpace(message.Text)
		if text == "" || IsJargonReferenceText(text) {
			continue
		}
		// 黑话学习只接受真实消息携带的 session_id，缺失时保留空值交给解析阶段拒绝，
		// 不能回退到学习器会话，否则会把多个聊天流的素材记到错误会话
		sessionID := strings.TrimSpace(message.SessionID)
		item := SourceItem{
			MessageID:  strings.TrimSpace(message.MessageID),
			Content:    text,
			Platform:   strings.TrimSpace(message.Platform),
			UserID:     strings.TrimSpace(message.UserID),
			Name:       strings.TrimSpace(message.Name),
			GroupName:  strings.TrimSpace(message.GroupName),
			SessionID:  sessionID,
			SourceKind: message.SourceKind,
		}
		if item.MessageID == "" {
			continue
		}
		items = append(items, item)
	}
	l.pending.Extend(items)
}

func isLearnableContextMessage(message ContextMessage) bool {
	// 对齐 Python _is_learnable_session_message：机器人自身与通知消息都不作为学习素材
	if message.IsBotSelf || message.IsNotify || message.SourceKind != "user" {
		return false
	}
	return true
}

// Observe 入站单条用户消息写入待学习缓冲
func (l *JargonLearner) Observe(messageID, text string) {
	l.ObserveContext(ContextMessage{
		MessageID:  messageID,
		SourceKind: "user",
		Text:       text,
	})
}

// ObserveContext 写入带平台与用户身份的入站消息，供黑名单与来源校验使用
func (l *JargonLearner) ObserveContext(message ContextMessage) {
	if l == nil {
		return
	}
	message.Text = strings.TrimSpace(message.Text)
	if message.Text == "" || strings.TrimSpace(message.MessageID) == "" {
		return
	}
	if message.SourceKind == "" {
		message.SourceKind = "user"
	}
	if message.IsBotSelf || message.IsNotify || message.SourceKind != "user" {
		return
	}
	sessionID := strings.TrimSpace(message.SessionID)
	if sessionID == "" {
		sessionID = l.SessionID
	}
	_, enableLearning := DefaultRegistry().ChatConfig().GetJargonConfig(sessionID)
	if !enableLearning {
		return
	}
	l.pending.Extend([]SourceItem{{
		MessageID:  strings.TrimSpace(message.MessageID),
		Content:    message.Text,
		Platform:   strings.TrimSpace(message.Platform),
		UserID:     strings.TrimSpace(message.UserID),
		Name:       strings.TrimSpace(message.Name),
		GroupName:  strings.TrimSpace(message.GroupName),
		SessionID:  sessionID,
		SourceKind: message.SourceKind,
	}})
}

// LearningState 返回最近一次学习状态与说明
func (l *JargonLearner) LearningState() (string, string) {
	if l == nil {
		return "skipped", ""
	}
	return l.LearningStatus, l.LearningDetail
}

// PendingCount 返回待学习素材数量
func (l *JargonLearner) PendingCount() int {
	if l == nil {
		return 0
	}
	return l.pending.Len()
}

// DroppedCount 返回因超过缓冲上限被淘汰的素材数量
func (l *JargonLearner) DroppedCount() int {
	if l == nil {
		return 0
	}
	return l.pending.DroppedCount()
}

// LearnFromContextMessages 收集素材并执行 LLM 黑话学习批次
func (l *JargonLearner) LearnFromContextMessages(ctx context.Context, messages []ContextMessage) (bool, error) {
	l.LearningRetryBlocked = false
	l.resetLearningContents()
	reg := l.registry
	if reg == nil {
		reg = DefaultRegistry()
	}
	// 对齐 Python 收集阶段：先按当前会话判定 learn，再决定是否收集素材
	if _, enabled := reg.ChatConfig().GetJargonConfig(l.SessionID); !enabled {
		l.pending.Clear()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("黑话学习开关已关闭，跳过收集",
			zap.String("session_id", l.SessionID))
		return false, nil
	}
	l.CollectContextMessages(messages)
	snapshot := l.pending.Snapshot()
	if len(snapshot) < l.MinMessagesForExtraction {
		if len(snapshot) > 0 {
			l.LearningStatus = "waiting"
			l.LearningDetail = l.pending.WaitingDetail(l.MinMessagesForExtraction)
		} else {
			l.LearningStatus = "skipped"
			l.LearningDetail = "裁切历史中没有可学习的真实聊天消息"
		}
		return false, nil
	}

	sessionID, resolveErr := resolveLearningSessionID(ctx, reg, snapshot)
	if resolveErr != nil {
		l.LearningRetryBlocked = true
		l.LearningStatus = "skipped"
		l.LearningDetail = "无法解析到有效聊天流，素材已保留"
		zap.L().Warn("黑话学习无法解析有效聊天流",
			zap.String("learner_session_id", l.SessionID),
			zap.Error(resolveErr))
		return false, nil
	}
	if sessionID != l.SessionID {
		zap.L().Info("黑话学习会话 ID 已按真实消息修正",
			zap.String("learner_session_id", l.SessionID),
			zap.String("learning_session_id", sessionID))
	}
	if _, enable := reg.ChatConfig().GetJargonConfig(sessionID); !enable {
		l.pending.Clear()
		l.LearningStatus = "skipped"
		l.LearningDetail = "学习开关已关闭，已清空待学习素材"
		zap.L().Info("黑话学习开关已关闭，已清空待学习素材",
			zap.String("session_id", sessionID))
		return false, nil
	}
	runner := l.resolveLearningRunner(reg.JargonRouter())
	if runner == nil {
		l.LearningStatus = "waiting"
		l.LearningDetail = "黑话学习 LLM 未配置 Router"
		return false, nil
	}
	settings := reg.LearningSettings()
	gate := defaultJargonBatchGate
	acquired := gate.acquire(sessionID, settings.MaxExpressionLearners)
	if !acquired.Acquired {
		l.LearningRetryBlocked = acquired.Max <= 0
		l.LearningStatus = "waiting"
		if l.LearningRetryBlocked {
			l.LearningStatus = "skipped"
		}
		l.LearningDetail = jargonGateDetail(acquired, l.pending.Len())
		return false, nil
	}
	defer gate.release(sessionID)

	l.pending.Discard(snapshot)
	l.LearningStatus = "running"
	l.LearningDetail = ""
	displayName := reg.LearningSessionDisplayName(ctx, sessionID, jargonSessionNameHint(snapshot, sessionID))
	miner := l.miner
	if miner == nil {
		miner = NewJargonMinerForRegistry(reg, sessionID, displayName)
	} else if originalSession, originalName := miner.SessionInfo(); originalSession != sessionID || originalName != displayName {
		miner.SetSession(sessionID, displayName)
		defer miner.SetSession(originalSession, originalName)
	}
	return l.runLearningBatch(ctx, snapshot, miner, runner, settings)
}

func (l *JargonLearner) resolveLearningRunner(router *llm.Router) *LearningRunner {
	if router == nil {
		return nil
	}
	return &LearningRunner{Router: router}
}

func jargonSessionNameHint(items []SourceItem, sessionID string) LearningSessionNameHint {
	hint := LearningSessionNameHint{}
	for _, item := range items {
		itemSessionID := strings.TrimSpace(item.SessionID)
		if sessionID != "" && itemSessionID != "" && itemSessionID != sessionID {
			continue
		}
		if hint.GroupName == "" {
			hint.GroupName = strings.TrimSpace(item.GroupName)
		}
		if hint.Nickname == "" {
			hint.Nickname = strings.TrimSpace(item.Name)
		}
	}
	return hint
}

// resolveLearningSessionID 按素材真实 session_id 频次选择已注册聊天流
// 对齐 Python jargon_learner：候选全部未注册时不回退学习器会话，保留素材等待重试
func resolveLearningSessionID(ctx context.Context, reg *Registry, items []SourceItem) (string, error) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	candidates := make([]string, 0, len(items))
	for _, item := range items {
		candidates = append(candidates, item.SessionID)
	}
	return reg.ResolveLearningSessionID(ctx, candidates, "")
}

func (l *JargonLearner) runLearningBatch(ctx context.Context, items []SourceItem, miner *JargonMiner, runner *LearningRunner, settings LearningSettings) (bool, error) {
	if len(items) == 0 {
		l.logJargonUpdateProcess("empty_input", miner, nil, 0, 0, nil, 0, 0)
		l.LearningStatus = "skipped"
		l.LearningDetail = "没有可用于黑话学习的消息素材"
		return false, nil
	}
	pm := runner.Prompts
	if pm == nil {
		pm = defaultPromptManager()
	}
	if pm == nil {
		if defaultPromptErr != nil {
			return false, defaultPromptErr
		}
		return false, fmt.Errorf("黑话学习 prompt 管理器不可用")
	}
	rendered, err := pm.Render("learn_jargon", map[string]string{
		"chat_str": "聊天上下文将在后续多条 user message 中给出；请以每条消息中的 id 作为来源行编号。",
	})
	if err != nil {
		return false, err
	}
	systemPrompt := rendered + "\n\n注意：聊天上下文会在后续多条 user message 中给出。真实聊天消息会带有 <message id=...> 属性，id 是本轮学习的来源编号。"
	users, userTexts := buildJargonLearningUserMessages(items)
	response, modelName, err := generateLearningResponse(ctx, miner.SessionID, runner.Router, systemPrompt, users)
	if err != nil {
		return false, err
	}
	saveJargonLearningPromptPreview(miner.SessionID, l.SessionID, len(items), len(users)+1, systemPrompt, userTexts, response, modelName)

	entries := ParseJargonResponse(response)
	if cached := cachedJargonCandidates(items, miner.GetCachedJargons()); len(cached) > 0 {
		seen := map[string]struct{}{}
		for _, entry := range entries {
			seen[strings.TrimSpace(entry.Content)] = struct{}{}
		}
		for _, entry := range cached {
			if _, ok := seen[strings.TrimSpace(entry.Content)]; ok {
				continue
			}
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		l.logJargonUpdateProcess("no_extracted_entries", miner, items, 0, 0, nil, 0, 0)
		l.LearningStatus = "no_results"
		l.LearningDetail = "模型未提取出黑话候选，已有黑话缓存也未命中"
		return false, nil
	}

	result, err := l.processJargonEntries(ctx, entries, items, miner, settings)
	if err != nil {
		return false, err
	}
	l.appendLearningContents(result.Contents)
	total := result.Saved + result.Updated
	if result.Accepted == 0 {
		l.LearningStatus = "no_results"
		l.LearningDetail = jargonItoa(result.Parsed) + " 个黑话候选均未通过内容或来源校验"
		return false, nil
	}
	if total > 0 {
		l.LearningStatus = "completed"
	} else {
		l.LearningStatus = "no_results"
	}
	l.LearningDetail = "处理 " + jargonItoa(result.Accepted) + " 个候选，新增 " + jargonItoa(result.Saved) + " 个、更新 " + jargonItoa(result.Updated) + " 个词条"
	return total > 0, nil
}

// buildLearningUserMessages 对齐 _build_multi_learning_messages（<message id=... user=...>）
func buildJargonLearningUserMessages(items []SourceItem) ([]*schema.AgenticMessage, []string) {
	users := make([]*schema.AgenticMessage, 0, len(items)+1)
	userTexts := make([]string, 0, len(items)+1)
	for index, item := range items {
		body := stripMessageOpenTag(item.Content)
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.UserID)
		}
		content := fmt.Sprintf("<message id=\"%d\" user=\"%s\">\n%s", index+1, escapeLearningUserName(name), body)
		users = append(users, schema.UserAgenticMessage(content))
		userTexts = append(userTexts, content)
	}
	instruction := "请根据以上聊天消息输出 JSON。"
	users = append(users, schema.UserAgenticMessage(instruction))
	userTexts = append(userTexts, instruction)
	return users, userTexts
}

var learningUserNameEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\"", "&quot;",
	"'", "&#x27;",
)

// escapeLearningUserName 对齐 Python html.escape(quote=True)，避免昵称破坏 message 标签
func escapeLearningUserName(name string) string {
	return learningUserNameEscaper.Replace(name)
}

func stripMessageOpenTag(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "<message") {
		return content
	}
	if end := strings.Index(content, ">"); end >= 0 {
		return strings.TrimSpace(content[end+1:])
	}
	return content
}

func generateLearningResponse(ctx context.Context, sessionID string, router *llm.Router, systemPrompt string, users []*schema.AgenticMessage) (string, string, error) {
	if router == nil {
		return "", "", fmt.Errorf("黑话学习 LLM 未配置 Router")
	}
	resolved, client, err := router.NewClientForTask(ctx, llm.TaskLearner)
	if err != nil {
		resolved, client, err = router.NewClientForTask(ctx, llm.TaskUtils)
	}
	if err != nil {
		return "", "", err
	}
	_ = sessionID
	modelName := learningModelName(resolved)
	// llm_usage 的 request_type 对齐 MaiBot 的 jargon.learner
	ctx = llm.WithTask(ctx, "jargon.learner", modelName)
	messages := make([]*schema.AgenticMessage, 0, len(users)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		messages = append(messages, schema.SystemAgenticMessage(systemPrompt))
	}
	messages = append(messages, users...)
	msg, err := client.Generate(ctx, messages)
	if err != nil {
		return "", "", err
	}
	return agenticPlainText(msg), modelName, nil
}

// cachedJargonCandidates 对齐 _check_cached_jargons_in_messages：按消息顺序命中缓存词条
func cachedJargonCandidates(items []SourceItem, cached []string) []JargonLearningCandidate {
	out := make([]JargonLearningCandidate, 0)
	for index, item := range items {
		text := strings.ToLower(strings.TrimSpace(item.Content))
		if text == "" {
			continue
		}
		for _, term := range cached {
			term = strings.TrimSpace(term)
			if term == "" {
				continue
			}
			if jargonTextContains(text, strings.ToLower(term)) {
				out = append(out, JargonLearningCandidate{
					Content:  term,
					SourceID: jargonItoa(index + 1),
				})
			}
		}
	}
	return out
}

func jargonTextContains(text, term string) bool {
	if term == "" {
		return false
	}
	if containsHan(term) {
		return strings.Contains(text, term)
	}
	// Python 对非中文词条使用 \b 边界，避免 "gg" 命中 "egg" 或 "gg中"
	for offset := 0; offset <= len(text)-len(term); {
		index := strings.Index(text[offset:], term)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(term)
		if jargonBoundaryAt(text, index, end) {
			return true
		}
		offset = index + 1
	}
	return false
}

func jargonBoundaryAt(text string, start, end int) bool {
	if start > 0 {
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		if isPythonWordRune(before) {
			return false
		}
	}
	if end < len(text) {
		after, _ := utf8.DecodeRuneInString(text[end:])
		if isPythonWordRune(after) {
			return false
		}
	}
	return true
}

// isPythonWordRune 对齐 Python re 的 Unicode \w（字母、数字、下划线）
func isPythonWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

func containsHan(text string) bool {
	for _, r := range text {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}

func learningModelName(resolved llm.ResolvedRoute) string {
	if name := strings.TrimSpace(resolved.MaiBotModel); name != "" {
		return name
	}
	if name := strings.TrimSpace(resolved.Profile.ModelName); name != "" {
		return name
	}
	return strings.TrimSpace(resolved.Config.Model)
}

func jargonGateDetail(result jargonBatchAcquireResult, pending int) string {
	var reason string
	switch result.Reason {
	case "session_busy":
		reason = "当前会话已有学习批次正在运行"
	case "global_limit":
		reason = "全局学习并发已满（" + jargonItoa(result.Active) + "/" + jargonItoa(result.Max) + "）"
	case "max_expression_learner <= 0":
		reason = "学习并发配置小于或等于 0"
	default:
		reason = result.Reason
	}
	return reason + "，已保留 " + jargonItoa(pending) + " 条素材等待重试"
}
