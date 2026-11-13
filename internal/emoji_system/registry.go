package emojisystem

import (
	"context"
	"strings"
	"sync"

	"maibot-go/internal/common/database"
	"maibot-go/internal/llm_models"
	"maibot-go/internal/prompt"

	"go.uber.org/zap"
)

// Registry 进程内 Emoji 子系统入口
type Registry struct {
	mu               sync.RWMutex
	mem              *memory.Manager
	manager          *Manager
	stickerDir       string
	prompts          *prompt.Manager
	router           *llm.Router
	filtration       bool
	settings         EmojiSettings
	settingsSet      bool
	settingsProvider func() EmojiSettings
	descTasks        sync.Map
	cacheLoopOnce    sync.Once
	settingsChanged  chan struct{}
}

var defaultRegistry = &Registry{}

// DefaultRegistry 返回全局 Registry
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// SetMemory 注入记忆管理器并构建 Manager
func (r *Registry) SetMemory(mem *memory.Manager) {
	if mem == nil {
		return
	}
	r.mu.Lock()
	r.mem = mem
	manager := NewManager(mem)
	manager.SetStickerDir(r.stickerDir)
	r.manager = manager
	r.mu.Unlock()
}

// SetManager 注入现成的 Manager，传 nil 清空
// 供测试与离线快照场景替换，生产初始化仍走 SetMemory
func (r *Registry) SetManager(mgr *Manager) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.manager = mgr
	if mgr != nil && r.stickerDir != "" {
		mgr.SetStickerDir(r.stickerDir)
	}
	r.mu.Unlock()
}

// Manager 返回当前 Manager（可能为 nil）
func (r *Registry) Manager() *Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.manager
}

// SetDescribeDeps 注入 VLM 描述链依赖（sticker 目录、prompt、TaskVLM 路由器）
func (r *Registry) SetDescribeDeps(stickerDir string, prompts *prompt.Manager, router *llm.Router) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.stickerDir = strings.TrimSpace(stickerDir)
	r.prompts = prompts
	r.router = router
	if r.manager != nil {
		r.manager.SetStickerDir(r.stickerDir)
	}
	r.mu.Unlock()
	// 启动早期 Reload 时 sticker 目录尚未注入，这里补一次校验以清理破损注册记录
	if r.manager != nil && r.manager.mem != nil {
		if err := r.Reload(context.Background()); err != nil {
			zap.L().Warn("表情包描述依赖注入后刷新内存池失败", zap.Error(err))
		}
	}
}

// Reload 启动时或后台维护后刷新内存池
func (r *Registry) Reload(ctx context.Context) error {
	r.mu.RLock()
	mgr := r.manager
	r.mu.RUnlock()
	if mgr == nil {
		return errMemoryUnavailable
	}
	return mgr.ReloadFromDB(ctx)
}

// SetContentFiltration 设置注册前内容过滤开关，对齐 global_config.emoji.content_filtration
func (r *Registry) SetContentFiltration(enabled bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.filtration = enabled
	r.mu.Unlock()
}

func (r *Registry) filtrationEnabled() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	enabled := r.filtration
	r.mu.RUnlock()
	if enabled {
		return true
	}
	// 未显式注入时回退实例配置，避免 WebUI 未初始化时绕过内容审核
	return r.Settings().ContentFiltration
}
