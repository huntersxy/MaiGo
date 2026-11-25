package maibot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	defaultWatchPollInterval = 500 * time.Millisecond
	defaultWatchDebounce     = 600 * time.Millisecond
	defaultReloadMinInterval = time.Second
	// defaultCallbackTimeout 对齐 Python config.py 传给 FileWatcher 的 callback_timeout_s=15
	defaultCallbackTimeout = 15 * time.Second
	// defaultCallbackFailureThreshold 连续失败多少次后进入冷却
	defaultCallbackFailureThreshold = 3
	// defaultCallbackCooldown 对齐 Python config.py 的 callback_cooldown_s=30
	defaultCallbackCooldown = 30 * time.Second
)

type fileSnap struct {
	mod  time.Time
	size int64
}

// ConfigWatcherStats 是配置监听回调的累计计数，供关停日志和诊断读取
type ConfigWatcherStats struct {
	BatchesSeen              int64
	ChangesSeen              int64
	CallbacksSucceeded       int64
	CallbacksFailed          int64
	CallbacksTimedOut        int64
	CallbacksSkippedCooldown int64
}

type configWatcherStats struct {
	batchesSeen              atomic.Int64
	changesSeen              atomic.Int64
	callbacksSucceeded       atomic.Int64
	callbacksFailed          atomic.Int64
	callbacksTimedOut        atomic.Int64
	callbacksSkippedCooldown atomic.Int64
}

func (s *configWatcherStats) snapshot() ConfigWatcherStats {
	if s == nil {
		return ConfigWatcherStats{}
	}
	return ConfigWatcherStats{
		BatchesSeen:              s.batchesSeen.Load(),
		ChangesSeen:              s.changesSeen.Load(),
		CallbacksSucceeded:       s.callbacksSucceeded.Load(),
		CallbacksFailed:          s.callbacksFailed.Load(),
		CallbacksTimedOut:        s.callbacksTimedOut.Load(),
		CallbacksSkippedCooldown: s.callbacksSkippedCooldown.Load(),
	}
}

// ConfigWatcherOptions 控制轮询监视参数
type ConfigWatcherOptions struct {
	PollInterval      time.Duration
	Debounce          time.Duration
	MinReloadInterval time.Duration
	PolicyPath        string
	// OnPolicyChanged 是同步兼容回调；与 OnPolicyChangedContext 同时设置时按顺序调用
	OnPolicyChanged func(path string)
	// OnPolicyChangedContext 在受超时预算保护的上下文中处理适配器策略变更
	OnPolicyChangedContext func(ctx context.Context, path string) error
	// ClosePolicyBlockedChats 对齐 Python heartflow_manager.close_policy_blocked_chats
	// 策略文件变化时先关闭命中禁用名单的聊天，再通知策略管理器
	ClosePolicyBlockedChats func(ctx context.Context) error
	// CallbackTimeout 单批文件变更处理预算，<=0 使用 15 秒
	CallbackTimeout time.Duration
	// CallbackFailureThreshold 连续失败阈值，<=0 使用 3
	CallbackFailureThreshold int
	// CallbackCooldown 连续失败后的冷却时长，<=0 使用 30 秒
	CallbackCooldown time.Duration
	Logger           *zap.Logger
}

func (o *ConfigWatcherOptions) applyDefaults() {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultWatchPollInterval
	}
	if o.Debounce <= 0 {
		o.Debounce = defaultWatchDebounce
	}
	if o.MinReloadInterval <= 0 {
		o.MinReloadInterval = defaultReloadMinInterval
	}
	if o.CallbackTimeout <= 0 {
		o.CallbackTimeout = defaultCallbackTimeout
	}
	if o.CallbackFailureThreshold <= 0 {
		o.CallbackFailureThreshold = defaultCallbackFailureThreshold
	}
	if o.CallbackCooldown <= 0 {
		o.CallbackCooldown = defaultCallbackCooldown
	}
	if o.Logger == nil {
		o.Logger = zap.L()
	}
}

// StartConfigWatcher 在后台轮询 bot/model TOML、共享模型目录与 adapter policy，防抖后热重载
//
// ctx 取消时退出；单批回调受 CallbackTimeout 限制，连续失败达到阈值后进入冷却，
// 与 Python FileWatcher 一致。重载失败只打日志，保留上一版配置快照
func (h *InstanceHolder) StartConfigWatcher(ctx context.Context, opts ConfigWatcherOptions) {
	opts.applyDefaults()
	log := opts.Logger
	stats := &configWatcherStats{}
	h.setConfigWatcherStats(stats)

	botPath := h.botConfigPath()
	modelPath := h.modelConfigPath()
	catalogPath := SharedCatalogPath()
	policyPath := opts.PolicyPath
	if policyPath == "" {
		policyPath = filepath.Join(h.dir, "config", "adapter_policy.toml")
	}

	committedBot, _ := statSnap(botPath)
	committedModel, _ := statSnap(modelPath)
	committedCatalog, catalogErr := statSnap(catalogPath)
	committedDigestBot := digestFile(botPath)
	committedDigestModel := digestModelConfigInput(modelPath, catalogPath)
	committedPolicy, policyErr := statSnap(policyPath)
	haveCatalog := catalogPath != "" && catalogErr == nil
	havePolicy := policyErr == nil

	go func() {
		var pending []ReloadScope
		var policyPending bool
		var debounceUntil time.Time
		var lastReload time.Time
		var settlingBot, settlingModel, settlingPolicy fileSnap
		var settlingCatalog fileSnap
		var haveSettlingBot, haveSettlingModel, haveSettlingCatalog, haveSettlingPolicy bool
		var consecutiveFailures int
		var cooldownUntil time.Time

		// consumeObserved 在批次被处理或跳过冷却后消费文件快照，避免同一变更反复触发
		consumeObserved := func() {
			pending = nil
			policyPending = false
			debounceUntil = time.Time{}
			if snap, err := statSnap(botPath); err == nil {
				committedBot = snap
				haveSettlingBot = false
			}
			if snap, err := statSnap(modelPath); err == nil {
				committedModel = snap
				haveSettlingModel = false
			}
			if catalogPath != "" {
				if snap, err := statSnap(catalogPath); err == nil {
					committedCatalog = snap
					haveCatalog = true
				} else {
					haveCatalog = false
				}
				haveSettlingCatalog = false
			}
			if snap, err := statSnap(policyPath); err == nil {
				committedPolicy = snap
				havePolicy = true
			} else {
				havePolicy = false
			}
			haveSettlingPolicy = false
		}

		ticker := time.NewTicker(opts.PollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if snap, err := statSnap(botPath); err == nil && snap != committedBot {
					pending = mergeScope(pending, ReloadScopeBot)
					if !haveSettlingBot || snap != settlingBot {
						settlingBot = snap
						haveSettlingBot = true
						stats.changesSeen.Add(1)
						debounceUntil = time.Now().Add(opts.Debounce)
					}
				}
				if snap, err := statSnap(modelPath); err == nil && snap != committedModel {
					pending = mergeScope(pending, ReloadScopeModel)
					if !haveSettlingModel || snap != settlingModel {
						settlingModel = snap
						haveSettlingModel = true
						stats.changesSeen.Add(1)
						debounceUntil = time.Now().Add(opts.Debounce)
					}
				}
				if catalogPath != "" {
					snap, err := statSnap(catalogPath)
					catalogChanged := (err == nil) != haveCatalog || (err == nil && snap != committedCatalog)
					if catalogChanged {
						pending = mergeScope(pending, ReloadScopeModel)
						if !haveSettlingCatalog || snap != settlingCatalog {
							settlingCatalog = snap
							haveSettlingCatalog = true
							stats.changesSeen.Add(1)
							debounceUntil = time.Now().Add(opts.Debounce)
						}
					}
				}
				if snap, err := statSnap(policyPath); (err == nil) != havePolicy || (err == nil && snap != committedPolicy) {
					policyPending = true
					if !haveSettlingPolicy || snap != settlingPolicy {
						settlingPolicy = snap
						haveSettlingPolicy = true
						stats.changesSeen.Add(1)
						debounceUntil = time.Now().Add(opts.Debounce)
					}
				}

				if (len(pending) == 0 && !policyPending) || debounceUntil.IsZero() || time.Now().Before(debounceUntil) {
					continue
				}

				digestBot := digestFile(botPath)
				digestModel := digestModelConfigInput(modelPath, catalogPath)
				changedBot := digestBot != "" && digestBot != committedDigestBot
				changedModel := digestModel != "" && digestModel != committedDigestModel

				// 内容指纹未变时不调用回调，只消费 mtime/size 变化
				if !policyPending && !changedBot && !changedModel {
					consumeObserved()
					continue
				}

				now := time.Now()
				if !cooldownUntil.IsZero() && now.Before(cooldownUntil) {
					stats.callbacksSkippedCooldown.Add(1)
					log.Debug("配置变更回调处于冷却中，跳过本批变更",
						zap.String("instance_dir", h.dir),
						zap.Duration("remaining", cooldownUntil.Sub(now)))
					consumeObserved()
					continue
				}

				if !policyPending && !lastReload.IsZero() && time.Since(lastReload) < opts.MinReloadInterval {
					debounceUntil = time.Now().Add(opts.MinReloadInterval - time.Since(lastReload))
					continue
				}

				stats.batchesSeen.Add(1)
				scopes := append([]ReloadScope(nil), pending...)
				policyChanged := policyPending
				policyForCallback := policyPending
				reloadAttempted := len(scopes) > 0 && (changedBot || changedModel)
				callback := func(callbackCtx context.Context) error {
					var firstErr error
					if policyForCallback {
						if opts.ClosePolicyBlockedChats != nil {
							if err := opts.ClosePolicyBlockedChats(callbackCtx); err != nil && firstErr == nil {
								firstErr = fmt.Errorf("关闭受策略限制的聊天失败: %w", err)
							}
						}
						if opts.OnPolicyChangedContext != nil {
							if err := opts.OnPolicyChangedContext(callbackCtx, policyPath); err != nil && firstErr == nil {
								firstErr = fmt.Errorf("处理适配器策略变更失败: %w", err)
							}
						}
						if opts.OnPolicyChanged != nil {
							opts.OnPolicyChanged(policyPath)
						}
					}
					if reloadAttempted {
						if err := h.ReloadFromDisk(scopes); err != nil && firstErr == nil {
							firstErr = err
						}
					}
					return firstErr
				}

				callbackErr, timedOut := runConfigWatcherCallback(ctx, opts.CallbackTimeout, callback)
				consumeObserved()
				if reloadAttempted {
					lastReload = time.Now()
				}

				switch {
				case timedOut:
					stats.callbacksTimedOut.Add(1)
					stats.callbacksFailed.Add(1)
					consecutiveFailures++
					log.Warn("配置变更回调执行超时",
						zap.String("instance_dir", h.dir),
						zap.Duration("timeout", opts.CallbackTimeout))
				case callbackErr != nil:
					stats.callbacksFailed.Add(1)
					consecutiveFailures++
					log.Warn("配置变更回调执行失败",
						zap.String("instance_dir", h.dir), zap.Error(callbackErr))
				default:
					stats.callbacksSucceeded.Add(1)
					consecutiveFailures = 0
				}

				if consecutiveFailures >= opts.CallbackFailureThreshold {
					// 与 Python _mark_callback_failure 一致：进入冷却后清零连续失败计数
					cooldownUntil = time.Now().Add(opts.CallbackCooldown)
					consecutiveFailures = 0
					log.Warn("配置变更回调连续失败，进入冷却",
						zap.String("instance_dir", h.dir),
						zap.Duration("cooldown", opts.CallbackCooldown))
				}

				if callbackErr != nil || timedOut {
					continue
				}
				if changedBot {
					committedDigestBot = digestBot
				}
				if changedModel {
					committedDigestModel = digestModel
				}
				if reloadAttempted {
					cfg := h.Config()
					fields := []zap.Field{
						zap.String("instance_dir", h.dir),
						zap.Strings("scopes", scopesToStrings(scopes)),
						zap.Bool("config_reloaded", reloadAttempted),
						zap.Bool("policy_changed", policyChanged),
					}
					if cfg != nil {
						fields = append(fields,
							zap.String("bot_config_version", cfg.BotInner.Version),
							zap.String("model_config_version", cfg.ModelInner.Version))
					}
					log.Info("MaiBot 实例 TOML 热重载成功", fields...)
				}
			}
		}
	}()
}

// runConfigWatcherCallback 在超时边界内执行一批文件变更回调
// 超时后回调协程无法被强制终止，和 Python to_thread 一样只能记录失败并让调用方继续
func runConfigWatcherCallback(parent context.Context, timeout time.Duration, callback func(context.Context) error) (error, bool) {
	if callback == nil {
		return nil, false
	}
	if timeout <= 0 {
		return callback(parent), false
	}
	callbackCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()
		done <- callback(callbackCtx)
	}()
	select {
	case err := <-done:
		return err, false
	case <-callbackCtx.Done():
		if parent.Err() != nil {
			return parent.Err(), false
		}
		return callbackCtx.Err(), true
	}
}

func statSnap(path string) (fileSnap, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSnap{}, err
	}
	return fileSnap{mod: fi.ModTime(), size: fi.Size()}, nil
}

func digestFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return string(sum[:])
}

// digestModelConfigInput 与 Python 配置指纹一致：本地模型配置与共享目录共同决定运行期模型定义
func digestModelConfigInput(modelPath, catalogPath string) string {
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		return ""
	}
	hash := sha256.New()
	_, _ = hash.Write(raw)
	if catalogPath != "" {
		_, _ = hash.Write([]byte{0})
		if catalog, err := os.ReadFile(catalogPath); err == nil {
			_, _ = hash.Write(catalog)
		}
	}
	return string(hash.Sum(nil))
}

func mergeScope(scopes []ReloadScope, scope ReloadScope) []ReloadScope {
	for _, s := range scopes {
		if s == scope {
			return scopes
		}
	}
	return append(scopes, scope)
}

func scopesToStrings(scopes []ReloadScope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
}

// ResolveReloadScopesFromPaths 根据变更文件路径推断 bot/model 范围（对齐 MaiBot _resolve_changed_scopes）
func ResolveReloadScopesFromPaths(paths ...string) []ReloadScope {
	var scopes []ReloadScope
	for _, p := range paths {
		switch filepath.Base(p) {
		case botConfigFileName:
			scopes = mergeScope(scopes, ReloadScopeBot)
		case modelConfigFileName, CatalogFilename:
			scopes = mergeScope(scopes, ReloadScopeModel)
		}
	}
	return scopes
}
