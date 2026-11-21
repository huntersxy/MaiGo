package qq_official

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"MaiGo/internal/platform_io/drivers/snowluma"
)

// IdentityBridge 调用 snowluma LocalAPI；未就绪则跳过，绝不落到 Runner
type IdentityBridge struct {
	mu         sync.Mutex
	invoker    LocalAPIInvoker
	owner      string
	target     string
	cancelOnly context.CancelFunc
	cancelSync context.CancelFunc
	syncDone   chan struct{}
	syncOnce   func(context.Context) error
}

func NewIdentityBridge(invoker LocalAPIInvoker) *IdentityBridge {
	return &IdentityBridge{
		invoker: invoker,
		owner:   strings.ReplaceAll(uuid.NewString(), "-", ""),
	}
}

func (b *IdentityBridge) Start(ctx context.Context, settings Settings, readyFn func() bool) {
	if b == nil {
		return
	}
	b.Stop()
	if settings.QQOfficial.SnowlumaIdentityOnly {
		runCtx, cancel := context.WithCancel(context.Background())
		b.mu.Lock()
		b.cancelOnly = cancel
		b.mu.Unlock()
		go b.identityOnlyLoop(runCtx, settings, readyFn)
	}
	if settings.QQOfficial.SilentUserSync && isDecimal(settings.QQOfficial.UnifiedAccountID) && b.syncOnce != nil {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		b.mu.Lock()
		b.cancelSync = cancel
		b.syncDone = done
		b.mu.Unlock()
		go func() {
			defer close(done)
			b.silentSyncLoop(runCtx, readyFn)
		}()
	}
}

func (b *IdentityBridge) Stop() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.cancelOnly != nil {
		b.cancelOnly()
		b.cancelOnly = nil
	}
	if b.cancelSync != nil {
		b.cancelSync()
		b.cancelSync = nil
	}
	done := b.syncDone
	b.syncDone = nil
	b.mu.Unlock()
	if done != nil {
		<-done
	}
	_ = b.Release(context.Background())
}

func (b *IdentityBridge) Release(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	target := b.target
	b.target = ""
	owner := b.owner
	b.mu.Unlock()
	if target == "" {
		return nil
	}
	_, err := b.callLease(ctx, target, owner, false)
	if err != nil {
		zap.L().Warn("SL 身份查询模式释放失败，将在最后续期后 15 秒自动恢复", zap.Error(err))
	}
	return nil
}

func (b *IdentityBridge) Renew(ctx context.Context, settings Settings, ready, connecting bool) error {
	if b == nil {
		return nil
	}
	target := strings.TrimSpace(settings.QQOfficial.UnifiedAccountID)
	if !settings.QQOfficial.SnowlumaIdentityOnly || !settings.Plugin.Enabled || !isDecimal(target) {
		return b.Release(ctx)
	}
	b.mu.Lock()
	if b.target != "" && b.target != target {
		b.mu.Unlock()
		_ = b.Release(ctx)
		b.mu.Lock()
	}
	b.mu.Unlock()
	if !ready && !connecting {
		return nil
	}
	b.mu.Lock()
	b.target = target
	owner := b.owner
	b.mu.Unlock()
	result, err := b.callLease(ctx, target, owner, true)
	if err != nil {
		return err
	}
	if active, _ := result["active"].(bool); !active {
		return fmt.Errorf("SL 身份查询模式未激活，请确认关联账号在线并已更新 SL 适配器")
	}
	return nil
}

func (b *IdentityBridge) identityOnlyLoop(ctx context.Context, settings Settings, readyFn func() bool) {
	failed := false
	for {
		err := b.Renew(ctx, settings, readyFn != nil && readyFn(), false)
		if err != nil {
			if !failed {
				zap.L().Warn("SL 身份查询模式续期失败，将在最后续期后 15 秒自动恢复", zap.Error(err))
			}
			failed = true
		} else {
			failed = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (b *IdentityBridge) silentSyncLoop(ctx context.Context, readyFn func() bool) {
	failed := false
	for {
		delay := 2 * time.Second
		if failed {
			delay = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if readyFn == nil || !readyFn() {
			continue
		}
		err := b.syncOnce(ctx)
		if err != nil {
			if !failed {
				zap.L().Warn("QQ 静默身份同步暂不可用", zap.Error(err))
			}
			failed = true
			continue
		}
		failed = false
	}
}

func (b *IdentityBridge) callLease(ctx context.Context, accountID, owner string, enabled bool) (map[string]any, error) {
	if b == nil || b.invoker == nil {
		return nil, fmt.Errorf("snowluma LocalAPI 未就绪")
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return b.invoker.InvokeAPI(callCtx, snowluma.PluginID, "identity_only_lease", map[string]any{
		"account_id": accountID,
		"owner":      owner,
		"enabled":    enabled,
		"version":    "1",
	}, 2000)
}

func (b *IdentityBridge) callObservations(ctx context.Context, accountID string, groupIDs []string) (map[string]any, error) {
	if b == nil || b.invoker == nil {
		return nil, fmt.Errorf("snowluma LocalAPI 未就绪")
	}
	if groupIDs == nil {
		groupIDs = []string{}
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return b.invoker.InvokeAPI(callCtx, snowluma.PluginID, "identity_observations", map[string]any{
		"account_id": accountID,
		"group_ids":  groupIDs,
		"version":    "1",
	}, 5000)
}
