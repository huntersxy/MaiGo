package personinfo

import (
	"context"
	"sync"
	"time"

	"MaiGo/internal/common/database"
	"MaiGo/internal/llm_models"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Registry 人物画像注册表，进程内单例
type Registry struct {
	mu              sync.RWMutex
	mem             *memory.Manager
	factRouter      *llm.Router
	botSelf         map[string]struct{}
	botSelfResolver func(platform, userID string) bool

	writebackOnce  sync.Once
	writebackQueue *WritebackQueue

	reclassifyOnce sync.Once
	reclassifier   *HistoricalReclassifyRunner
}

var defaultRegistry = &Registry{}

// DefaultRegistry 返回全局 Registry
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// SetMemory 在记忆管理器就绪后注入
func (r *Registry) SetMemory(mem *memory.Manager) {
	if mem == nil {
		return
	}
	r.mu.Lock()
	r.mem = mem
	r.mu.Unlock()
}

func (r *Registry) manager() *memory.Manager {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mem
}

func (r *Registry) db(ctx context.Context) *gorm.DB {
	mem := r.manager()
	if mem == nil {
		return nil
	}
	return mem.WithContext(ctx).GetDB()
}

// RegisterInput 对齐 MaiBot Person.register_person 入参
type RegisterInput struct {
	Platform      string
	UserID        string
	Nickname      string
	GroupID       int64  // QQ 数值群号；非 QQ 平台为 0
	GroupKey      string // 非 QQ 外部群 ID 字符串
	GroupNickName string
	SeenAt        time.Time
}

// RegisterPerson 注册或刷新已知用户，并同步 member_profiles / member_names
func (r *Registry) RegisterPerson(ctx context.Context, in RegisterInput) (*Profile, error) {
	mem := r.manager()
	if mem == nil {
		return nil, errMemoryUnavailable
	}
	rawPlatform := stringsTrim(in.Platform)
	platform := normalizePlatform(rawPlatform)
	userID := stringsTrim(in.UserID)
	nickname := stringsTrim(in.Nickname)
	if platform == "" || userID == "" || nickname == "" {
		return nil, errInvalidRegisterInput
	}
	seenAt := in.SeenAt
	if seenAt.IsZero() {
		seenAt = timeNow()
	}

	pid := PersonID(platform, userID)
	// member_profiles / member_names 只承载 QQ 数值账号；非 QQ 平台走 person_info
	if platform == PlatformQQ {
		qqID := ParseQQUserID(userID)
		if qqID > 0 {
			if _, err := mem.GetOrCreateMemberProfile(qqID, nickname, seenAt); err != nil {
				return nil, err
			}
			if in.GroupID > 0 && stringsTrim(in.GroupNickName) != "" {
				if err := mem.RecordMemberName(qqID, in.GroupID, in.GroupNickName, seenAt); err != nil {
					zap.L().Warn("更新群名片失败", zap.Error(err))
				}
			}
		}
	}

	rec, created, err := r.upsertPersonOnRegister(ctx, pid, rawPlatform, userID, nickname, in)
	if err != nil {
		return nil, err
	}
	if created {
		zap.L().Info("成功注册新用户",
			zap.String("person_id", pid),
			zap.String("platform", platform),
			zap.String("nickname", nickname),
		)
	}
	p := profileFromRecord(rec)
	return &p, nil
}
