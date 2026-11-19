package runtime

import (
	"context"
	"strconv"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"MaiGo/internal/agent"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/media"
	"MaiGo/internal/config"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/files"
	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"
	maisakautils "MaiGo/internal/maisaka/utils"
	"MaiGo/internal/mcp_module"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/plugin_runtime"
	"MaiGo/internal/runtime/execution/policy"

	"github.com/jellydator/ttlcache/v3"
	"go.uber.org/zap"
)

const (
	replyCacheTTL      = 30 * time.Minute
	replyCacheCapacity = 1024
	// externalIdentityTTL 外部字符串 ID 反查缓存时长，覆盖单次会话内的回复与 @ 映射
	externalIdentityTTL      = 24 * time.Hour
	externalIdentityCapacity = 4096
)

type Agent struct {
	ctx            context.Context
	cancel         context.CancelFunc
	memory         *memory.Manager
	platformIO     *platformio.Manager
	acpService     *agent.Service
	fileService    *filesvc.Service
	modelStore     *modelprofiles.Store
	mcpStore       *mcp.Store
	llmRouter      *llm.Router
	instanceMu     sync.RWMutex
	instanceHolder *maibotcfg.InstanceHolder
	committer      atomic.Pointer[committerHolder]
	arrivalSeq     sync.Map // 会话键 -> *atomic.Uint64，入站到达序号
	sessions       *sessionStore
	chatSessions   *chatsession.Manager
	gates          *gateStore
	turnSchedule   *conversationTurnScheduleStore

	groupConcurrency   *maisakautils.ThinkConcurrency
	privateConcurrency *maisakautils.ThinkConcurrency

	generationMu sync.RWMutex
	generation   *Generation
	applyMu      sync.Mutex

	selfMu      sync.Mutex
	selfIDValue int64
	selfExpires time.Time

	replyCache         *ttlcache.Cache[string, onebot.ReplyInfo]
	externalIdentities *ttlcache.Cache[string, string] // 外部数字代理 ID -> 原始字符串 ID

	recallMu       sync.Mutex
	pendingRecalls map[recallCommit]pendingRecall

	pendingGroupThinks map[int64]*pendingThink
	pendingGroupMu     sync.Mutex

	pendingPrivateThinks map[int64]*pendingThink
	privateMu            sync.Mutex
	privateStopped       bool

	waitTimeoutMu sync.Mutex
	waitTimeouts  map[string]*waitTimeoutHandle

	stopping atomic.Bool
	wg       sync.WaitGroup

	// maisakaBackgroundLoop 开启后，每轮思考交给会话级后台 RunLoop 串行执行
	maisakaBackgroundLoop atomic.Bool

	// sessionLearners 按会话缓存表达/黑话学习器，构造与复用逻辑在 learners 包
	sessionLearnersMu sync.Mutex
	sessionLearners   *learners.SessionLearnerRegistry
	trimmedLearning   sync.Map // session_id -> struct{}，裁切学习运行中标记

	pluginHost          *pluginruntime.Host
	pluginReloadPending atomic.Bool

	visualOnce sync.Once
	visual     *agentVisualBridge

	// callbacks 宿主注入的回调端口集合（webchat/思考通知/文件登记/发送后自动化/外部文件/账号对）
	callbacksMu sync.RWMutex
	callbacks   AgentCallbacks

	// index 汇总会话维度的运行期索引：平台、出站路由、session_id、内部目标、虚拟群 ID
	indexMu sync.Mutex
	index   *sessionIndex
}

type pendingThink struct {
	timer      *time.Timer
	generation uint64
}

// New 创建稳定 Agent；模型/MCP 在 generation 中延迟构建
func New(mem *memory.Manager, models *modelprofiles.Store, mcpServers *mcp.Store) (*Agent, error) {
	if mem == nil {
		return nil, fmt.Errorf("记忆系统未就绪")
	}
	if models == nil || mcpServers == nil {
		return nil, fmt.Errorf("模型或 MCP 存储未初始化")
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	a := &Agent{
		ctx:                  rootCtx,
		cancel:               cancel,
		memory:               mem,
		modelStore:           models,
		mcpStore:             mcpServers,
		llmRouter:            llm.NewRouter(models, nil),
		sessions:             newSessionStore(mem),
		chatSessions:         chatsession.NewManager(mem.GetDB()),
		gates:                newGateStore(),
		turnSchedule:         newConversationTurnScheduleStore(),
		pendingGroupThinks:   make(map[int64]*pendingThink),
		pendingPrivateThinks: make(map[int64]*pendingThink),
		pendingRecalls:       make(map[recallCommit]pendingRecall),
		replyCache:           newAgentTTLCache[string, onebot.ReplyInfo](replyCacheCapacity, replyCacheTTL),
		externalIdentities:   newAgentTTLCache[string, string](externalIdentityCapacity, externalIdentityTTL),
	}
	cfg := config.Snapshot()
	a.groupConcurrency = maisakautils.NewThinkConcurrency(a.ctx, cfg.Agent.MaxGroupConcurrency, a.thinkGroup)
	a.privateConcurrency = maisakautils.NewThinkConcurrency(a.ctx, cfg.Agent.MaxPrivateConcurrency, a.thinkPrivate)
	a.committer.Store(&committerHolder{instance: policy.InlineCommitter{}})
	if enabled := strings.TrimSpace(os.Getenv("MAIBOT_MAISAKA_BACKGROUND_LOOP")); enabled != "" {
		switch strings.ToLower(enabled) {
		case "1", "true", "yes", "on":
			a.maisakaBackgroundLoop.Store(true)
		}
	}
	go a.replyCache.Start()
	go a.externalIdentities.Start()
	return a, nil
}

// SetPlatformIOManager 注入 Platform IO 管理器，供发言等出站优先走多适配器 SendMessage
func (a *Agent) SetPlatformIOManager(mgr *platformio.Manager) {
	a.platformIO = mgr
}

// SetMessageCommitter 装配入站提交策略；未设置时按 compatibility 就地执行
func (a *Agent) SetMessageCommitter(committer policy.MessageCommitter) {
	if committer == nil {
		committer = policy.InlineCommitter{}
	}
	if old := a.committer.Swap(&committerHolder{instance: committer}); old != nil && old.instance != nil {
		old.instance.Shutdown()
	}
}

// messageCommitter 返回当前提交策略；未装配时符合 compatibility 语义
func (a *Agent) messageCommitter() policy.MessageCommitter {
	if holder := a.committer.Load(); holder != nil && holder.instance != nil {
		return holder.instance
	}
	return policy.InlineCommitter{}
}

type committerHolder struct {
	instance policy.MessageCommitter
}

// Start 启动回调与调度循环；没有模型时保持事件门关闭
func (a *Agent) Start() error {
	if a.chatSessions != nil {
		if err := a.chatSessions.LoadAllFromDB(context.Background()); err != nil {
			zap.L().Warn("加载 chat_sessions 失败，业务模块可能无法解析真实 session_id", zap.Error(err))
		}
	}
	a.wireLearnersEmbedding(context.Background())
	a.wireNativeImageEmbedding()
	a.wireLearnersJargonRouter()
	a.wireLearnersExpressionRouter()
	a.wirePersonFactRouter()

	a.wg.Add(1)
	go a.conversationThinkLoop()
	zap.L().Info("Agent 已启动")
	return nil
}

// Stop 停止 Agent 并等待运行任务退出
func (a *Agent) Stop() {
	a.shutdown()
	zap.L().Info("Agent 已停止")
}

func (a *Agent) shutdown() {
	a.stopping.Store(true)
	a.cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := learners.DefaultRegistry().StopExpressionVectorBackfill(stopCtx); err != nil {
		zap.L().Warn("停止表达向量历史补建超时", zap.Error(err))
	}
	stopCancel()
	a.stopMaisakaTurnLoops()
	a.clearPendingThinks()
	a.cancelAllWaitTimeouts()
	a.stopPrivateTimers()
	if a.groupConcurrency != nil {
		a.groupConcurrency.Close()
	}
	if a.privateConcurrency != nil {
		a.privateConcurrency.Close()
	}
	if a.committer.Load() != nil {
		if holder := a.committer.Swap(nil); holder != nil && holder.instance != nil {
			holder.instance.Shutdown()
		}
	}
	a.wg.Wait()
	a.stopCaches()
	a.closeGeneration()
}

func (a *Agent) closeGeneration() {
	a.generationMu.Lock()
	generation := a.generation
	a.generation = nil
	a.generationMu.Unlock()
	if generation != nil {
		generation.wg.Wait()
		generation.close()
	}
}

// BotSelfID 返回 QQ 平台 Bot 账号；账号由 Bot 平台账号配置提供
func (a *Agent) BotSelfID() int64 {
	return a.SelfID()
}

func newAgentTTLCache[K comparable, V any](capacity int, ttl time.Duration) *ttlcache.Cache[K, V] {
	return ttlcache.New(
		ttlcache.WithTTL[K, V](ttl),
		ttlcache.WithCapacity[K, V](uint64(capacity)),
		ttlcache.WithDisableTouchOnHit[K, V](),
	)
}

func (a *Agent) stopCaches() {
	if a.replyCache != nil {
		a.replyCache.Stop()
	}
	if a.externalIdentities != nil {
		a.externalIdentities.Stop()
	}
}

// MCPToolCount 返回当前 generation 的 MCP 工具数
func (a *Agent) MCPToolCount() int {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return 0
	}
	return len(generation.mcp.GetTools())
}

// MCPStatus 返回当前 generation 的 MCP 连接快照（WebUI 只读）
func (a *Agent) MCPStatus() mcp.StatusSnapshot {
	generation := a.currentGeneration()
	if generation == nil || generation.mcp == nil {
		return mcp.StatusSnapshot{}
	}
	return generation.mcp.StatusSnapshot()
}

// Ready 返回是否已构建 generation
// BotName 返回机器人账号昵称，供后台展示
func (a *Agent) BotName() string { return a.botName() }

// SelfID 返回 QQ 平台 Bot 账号的数值 ID，未知时返回 0。
// 账号来自 Bot 平台账号配置，缓存 30 秒避免每条消息查库。
func (a *Agent) SelfID() int64 {
	if a == nil {
		return 0
	}
	a.selfMu.Lock()
	defer a.selfMu.Unlock()
	if time.Now().Before(a.selfExpires) {
		return a.selfIDValue
	}
	a.selfExpires = time.Now().Add(30 * time.Second)
	a.selfIDValue = 0
	a.callbacksMu.RLock()
	provider := a.callbacks.BotAccountPairs
	a.callbacksMu.RUnlock()
	if provider != nil {
		ctx, cancel := a.persistenceContext()
		pairs, err := provider(ctx)
		cancel()
		if err == nil {
			for _, pair := range pairs {
				if strings.ToLower(strings.TrimSpace(pair[0])) != platformio.PlatformQQ {
					continue
				}
				if value, parseErr := strconv.ParseInt(strings.TrimSpace(pair[1]), 10, 64); parseErr == nil && value > 0 {
					a.selfIDValue = value
					break
				}
			}
		}
	}
	return a.selfIDValue
}

func (a *Agent) Ready() bool {
	return a.currentGeneration() != nil
}

// Activate 构建首个 generation、恢复会话并放行事件
func (a *Agent) Activate() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	if a.currentGeneration() != nil {
		return nil
	}
	generation, err := a.buildGeneration()
	if err != nil {
		return err
	}
	a.setGeneration(generation)
	if err := a.restoreSessions(); err != nil {
		a.setGeneration(nil)
		generation.wg.Wait()
		generation.close()
		return fmt.Errorf("恢复聊天上下文失败: %w", err)
	}
	return nil
}

// Reload 构建并替换 generation；没有当前代时等同首次激活
func (a *Agent) Reload() error {
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	generation, err := a.buildGeneration()
	if err != nil {
		return err
	}
	first := a.currentGeneration() == nil
	a.setGeneration(generation)
	if first {
		if err := a.restoreSessions(); err != nil {
			a.setGeneration(nil)
			generation.wg.Wait()
			generation.close()
			return fmt.Errorf("恢复聊天上下文失败: %w", err)
		}
	}
	return nil
}

func (a *Agent) currentGeneration() *Generation {
	a.generationMu.RLock()
	defer a.generationMu.RUnlock()
	return a.generation
}

func (a *Agent) acquireGeneration() *Generation {
	a.generationMu.RLock()
	generation := a.generation
	if generation != nil {
		generation.wg.Add(1)
	}
	a.generationMu.RUnlock()
	return generation
}

func (a *Agent) setGeneration(next *Generation) {
	a.generationMu.Lock()
	old := a.generation
	a.generation = next
	a.generationMu.Unlock()
	if old != nil {
		go func() {
			old.wg.Wait()
			old.close()
		}()
	}
}

func (a *Agent) currentMedia() *media.Manager {
	generation := a.currentGeneration()
	if generation != nil && generation.media != nil {
		return generation.media
	}
	return nil
}

// SetConcurrencyLimits 热更新群聊与私聊并发上限
func (a *Agent) SetConcurrencyLimits(group, private int) {
	if a.groupConcurrency != nil {
		a.groupConcurrency.SetMax(group)
	}
	if a.privateConcurrency != nil {
		a.privateConcurrency.SetMax(private)
	}
}
