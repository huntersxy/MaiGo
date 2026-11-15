package hooks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/plugin_runtime/circuitbreaker"
)

// DispatchResult 一次命名 Hook 调用的聚合结果，对齐 host/hook_dispatcher.py HookDispatchResult
type DispatchResult struct {
	HookName      string
	Kwargs        map[string]any
	Aborted       bool
	StoppedBy     string
	CustomResults []any
	Errors        []string
}

// Registry 命名 Hook 订阅与分发
type Registry struct {
	mu            sync.RWMutex
	specs         *SpecRegistry
	subscriptions map[string][]Subscription

	timeoutMu            sync.RWMutex
	defaultTimeoutMS     int64
	defaultTimeoutGetter func() int64
}

func NewRegistry(specs *SpecRegistry) *Registry {
	if specs == nil {
		specs = NewSpecRegistry()
	}
	return &Registry{
		specs:            specs,
		subscriptions:    make(map[string][]Subscription),
		defaultTimeoutMS: 60000,
	}
}

// SetDefaultTimeoutMS 设置无 Hook/处理器级超时时的全局默认超时
func (r *Registry) SetDefaultTimeoutMS(timeoutMS int64) {
	if timeoutMS <= 0 {
		timeoutMS = 60000
	}
	r.timeoutMu.Lock()
	r.defaultTimeoutMS = timeoutMS
	r.timeoutMu.Unlock()
}

// SetDefaultTimeoutGetter 设置每次分发时读取的全局超时来源，对齐 Python 的每调用读取
//
// getter 返回 <=0 时回退 SetDefaultTimeoutMS 的默认值
func (r *Registry) SetDefaultTimeoutGetter(get func() int64) {
	r.timeoutMu.Lock()
	r.defaultTimeoutGetter = get
	r.timeoutMu.Unlock()
}

// DefaultTimeoutMS 返回当前生效的全局默认超时
func (r *Registry) DefaultTimeoutMS() int64 {
	r.timeoutMu.RLock()
	getter := r.defaultTimeoutGetter
	fallback := r.defaultTimeoutMS
	r.timeoutMu.RUnlock()
	if getter != nil {
		if timeoutMS := getter(); timeoutMS > 0 {
			return timeoutMS
		}
	}
	if fallback > 0 {
		return fallback
	}
	return 60000
}

// Subscribe 登记 Hook 订阅
func (r *Registry) Subscribe(sub Subscription) {
	normalized, ok := normalizeSubscription(sub)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscriptions[normalized.HookName] = append(r.subscriptions[normalized.HookName], normalized)
}

// ReplacePlugin 用新的订阅集合整体替换某个插件的全部 Hook 订阅
// 对齐 component_registry.register_plugin_components 的整插件替换语义，重复注册不会残留旧订阅
func (r *Registry) ReplacePlugin(pluginID string, subs []Subscription) int {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0
	}
	normalized := make([]Subscription, 0, len(subs))
	for _, sub := range subs {
		if strings.TrimSpace(sub.PluginID) == "" {
			sub.PluginID = pluginID
		}
		if sub.PluginID != pluginID {
			continue
		}
		item, ok := normalizeSubscription(sub)
		if !ok {
			continue
		}
		normalized = append(normalized, item)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for name, list := range r.subscriptions {
		kept := make([]Subscription, 0, len(list))
		for _, item := range list {
			if item.PluginID != pluginID {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(r.subscriptions, name)
			continue
		}
		r.subscriptions[name] = kept
	}
	for _, item := range normalized {
		r.subscriptions[item.HookName] = append(r.subscriptions[item.HookName], item)
	}
	return len(normalized)
}

// RemovePlugin 移除某个插件的全部 Hook 订阅，返回移除数量
func (r *Registry) RemovePlugin(pluginID string) int {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name, list := range r.subscriptions {
		kept := make([]Subscription, 0, len(list))
		for _, item := range list {
			if item.PluginID == pluginID {
				removed++
				continue
			}
			kept = append(kept, item)
		}
		if len(kept) == 0 {
			delete(r.subscriptions, name)
			continue
		}
		r.subscriptions[name] = kept
	}
	return removed
}

// Subscriptions 返回某个 Hook 已登记的订阅快照
func (r *Registry) Subscriptions(hookName string) []Subscription {
	name := strings.TrimSpace(hookName)
	if name == "" {
		return nil
	}
	return r.snapshotSubscriptions(name)
}

// Emit 触发命名 Hook：进程内回调 fan-out，Runner 已连接时经 bridge 发送 plugin.invoke_hook
func (r *Registry) Emit(ctx context.Context, bridge RunnerBridge, hookName string, kwargs map[string]any) DispatchResult {
	return r.EmitFiltered(ctx, bridge, hookName, kwargs, nil)
}

// EmitFiltered 与 Emit 相同，但分发前按 filter 过滤订阅；filter 为 nil 时不过滤
func (r *Registry) EmitFiltered(ctx context.Context, bridge RunnerBridge, hookName string, kwargs map[string]any, filter func(Subscription) bool) DispatchResult {
	name := strings.TrimSpace(hookName)
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	current := maps.Clone(kwargs)
	out := DispatchResult{HookName: name, Kwargs: maps.Clone(current)}

	spec, _ := r.specs.GetHookSpec(name)
	subs := r.snapshotSubscriptions(name)
	if filter != nil {
		filtered := make([]Subscription, 0, len(subs))
		for _, sub := range subs {
			if filter(sub) {
				filtered = append(filtered, sub)
			}
		}
		subs = filtered
	}
	if len(subs) == 0 {
		return out
	}
	sort.Slice(subs, func(i, j int) bool {
		return subscriptionLess(subs[i], subs[j])
	})

	for _, sub := range subs {
		if sub.Mode == HandlerModeObserve {
			if spec.AllowObserve() {
				r.scheduleObserve(ctx, bridge, spec, sub, name, maps.Clone(current))
			} else {
				zap.L().Warn("Hook 不允许 observe 处理器，已跳过",
					zap.String("hook", name), zap.String("handler", handlerLabel(sub)))
			}
			continue
		}
		if !spec.AllowBlocking() {
			out.Errors = append(out.Errors, fmt.Sprintf("Hook %s 不允许 blocking 处理器，已跳过 %s", name, handlerLabel(sub)))
			zap.L().Warn("Hook 不允许 blocking 处理器，已跳过",
				zap.String("hook", name), zap.String("handler", handlerLabel(sub)))
			continue
		}

		result, handlerLabel, err := r.invokeBlocking(ctx, bridge, spec, sub, name, current)
		if err != nil {
			msg := err.Error()
			if handlerLabel != "" {
				msg = "HookHandler " + handlerLabel + " 执行失败: " + msg
			}
			out.Errors = append(out.Errors, msg)
			zap.L().Warn("Hook 处理器失败", zap.String("hook", name), zap.Error(err))
			applyErrorPolicy(spec, sub, handlerLabel, &out)
			if out.Aborted {
				break
			}
			continue
		}
		r.mergeBlockingResult(spec, sub, handlerLabel, result, &out)
		current = maps.Clone(out.Kwargs)
		if out.Aborted {
			break
		}
	}
	return out
}

func (r *Registry) snapshotSubscriptions(hookName string) []Subscription {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.subscriptions[hookName]
	if len(src) == 0 {
		return nil
	}
	out := make([]Subscription, len(src))
	copy(out, src)
	return out
}

func (r *Registry) invokeBlocking(
	ctx context.Context,
	bridge RunnerBridge,
	spec HookSpec,
	sub Subscription,
	hookName string,
	kwargs map[string]any,
) (HandlerResult, string, error) {
	label := handlerLabel(sub)
	if sub.Handler != nil {
		return r.invokeLocalHandler(ctx, spec, sub, label, kwargs)
	}
	if sub.PluginID != "" && sub.HandlerName != "" && bridge != nil && bridge.HasActiveRunner() {
		permit, err := circuitbreaker.Default().TryAcquire(sub.PluginID, label, "hook:"+hookName)
		if err != nil {
			return HandlerResult{}, label, err
		}
		if !permit.Allowed {
			return HandlerResult{Success: true, Action: "continue"}, label, nil
		}
		res, err := bridge.InvokeHookHandler(ctx, sub, hookName, kwargs)
		if err != nil {
			if shouldRecordCircuitFailure(err) {
				circuitbreaker.Default().RecordFailure(permit, err.Error())
			}
			return HandlerResult{}, label, err
		}
		circuitbreaker.Default().RecordSuccess(permit)
		return normalizeHandlerResult(res), label, nil
	}
	return HandlerResult{Success: true, Action: "continue"}, label, nil
}

func (r *Registry) invokeLocalHandler(ctx context.Context, spec HookSpec, sub Subscription, label string, kwargs map[string]any) (HandlerResult, string, error) {
	timeoutMS := r.resolveHookHandlerTimeoutMS(spec, sub)
	handlerCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	type outcome struct {
		result HandlerResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := sub.Handler(handlerCtx, kwargs)
		done <- outcome{result: result, err: err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			if errors.Is(handlerCtx.Err(), context.DeadlineExceeded) {
				return HandlerResult{}, label, fmt.Errorf("HookHandler %s 执行超时，已超过 %dms", label, timeoutMS)
			}
			return HandlerResult{}, label, out.err
		}
		return normalizeHandlerResult(out.result), label, nil
	case <-handlerCtx.Done():
		if errors.Is(handlerCtx.Err(), context.DeadlineExceeded) {
			return HandlerResult{}, label, fmt.Errorf("HookHandler %s 执行超时，已超过 %dms", label, timeoutMS)
		}
		return HandlerResult{}, label, handlerCtx.Err()
	}
}

// resolveHookHandlerTimeoutMS 对齐 hook_dispatcher 的超时优先级：处理器 > Hook 默认 > 全局默认
func (r *Registry) resolveHookHandlerTimeoutMS(spec HookSpec, sub Subscription) int64 {
	if sub.TimeoutMS > 0 {
		return sub.TimeoutMS
	}
	if spec.DefaultTimeoutMS > 0 {
		return int64(spec.DefaultTimeoutMS)
	}
	return r.DefaultTimeoutMS()
}

func (r *Registry) scheduleObserve(
	ctx context.Context,
	bridge RunnerBridge,
	spec HookSpec,
	sub Subscription,
	hookName string,
	kwargs map[string]any,
) {
	go func() {
		_, label, err := r.invokeBlocking(context.WithoutCancel(ctx), bridge, spec, sub, hookName, kwargs)
		if err != nil {
			zap.L().Warn("observe Hook 处理器失败",
				zap.String("hook", hookName),
				zap.String("handler", label),
				zap.Error(err))
		}
	}()
}

func (r *Registry) mergeBlockingResult(spec HookSpec, sub Subscription, handlerLabel string, exec HandlerResult, out *DispatchResult) {
	if exec.CustomResult != nil {
		out.CustomResults = append(out.CustomResults, exec.CustomResult)
	}
	if !exec.Success {
		msg := exec.ErrorMessage
		if msg == "" && handlerLabel != "" {
			msg = "HookHandler " + handlerLabel + " 执行失败"
		}
		if msg != "" {
			out.Errors = append(out.Errors, msg)
		}
		applyErrorPolicy(spec, sub, handlerLabel, out)
		return
	}
	if exec.ModifiedKwargs != nil {
		if spec.AllowKwargsMutation() {
			out.Kwargs = maps.Clone(exec.ModifiedKwargs)
		} else {
			out.Errors = append(out.Errors,
				"Hook "+out.HookName+" 不允许修改 kwargs，已忽略 "+handlerLabel+" 的 modified_kwargs")
		}
	}
	action := normalizeAction(exec.Action)
	if action == "abort" {
		if spec.AllowAbort() {
			out.Aborted = true
			out.StoppedBy = handlerLabel
		} else {
			out.Errors = append(out.Errors,
				"Hook "+out.HookName+" 不允许 abort，已忽略 "+handlerLabel+" 的 abort 请求")
		}
	}
}

// applyErrorPolicy 对齐 hook_dispatcher._apply_error_policy：仅 abort 策略且 Hook 允许 abort 时中止本次调用，
// skip 与 log 在行为上没有差别，都只记录错误
func applyErrorPolicy(spec HookSpec, sub Subscription, handlerLabel string, out *DispatchResult) {
	if NormalizeErrorPolicy(sub.ErrorPolicy) != ErrorPolicyAbort {
		return
	}
	if !spec.AllowAbort() {
		out.Errors = append(out.Errors,
			"Hook "+out.HookName+" 不允许 abort，已忽略 "+handlerLabel+" 的 error_policy=abort")
		return
	}
	out.Aborted = true
	out.StoppedBy = handlerLabel
}

func handlerLabel(sub Subscription) string {
	if sub.PluginID != "" && sub.HandlerName != "" {
		return sub.PluginID + "." + sub.HandlerName
	}
	if sub.HandlerName != "" {
		return sub.HandlerName
	}
	if sub.PluginID != "" {
		return sub.PluginID
	}
	return "local"
}

func subscriptionLess(a, b Subscription) bool {
	if modeRank(a.Mode) != modeRank(b.Mode) {
		return modeRank(a.Mode) < modeRank(b.Mode)
	}
	if priorityRank(a.Priority) != priorityRank(b.Priority) {
		return priorityRank(a.Priority) < priorityRank(b.Priority)
	}
	if a.SourceRank != b.SourceRank {
		return a.SourceRank < b.SourceRank
	}
	if a.PluginID != b.PluginID {
		return a.PluginID < b.PluginID
	}
	return a.HandlerName < b.HandlerName
}

func modeRank(m HandlerMode) int {
	if m == HandlerModeObserve {
		return 1
	}
	return 0
}

func priorityRank(p HandlerPriority) int {
	switch p {
	case PriorityEarly:
		return 0
	case PriorityLate:
		return 2
	default:
		return 1
	}
}

func normalizeAction(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "abort" {
		return "abort"
	}
	return "continue"
}

func normalizeHandlerResult(res HandlerResult) HandlerResult {
	res.Action = normalizeAction(res.Action)
	if res.Action == "" {
		res.Action = "continue"
	}
	if res.ModifiedKwargs != nil {
		res.ModifiedKwargs = maps.Clone(res.ModifiedKwargs)
	}
	return res
}

// Specs 返回关联的 Hook 规格注册中心
func (r *Registry) Specs() *SpecRegistry {
	return r.specs
}

// shouldRecordCircuitFailure 判断 RPC 异常是否应计入插件熔断，对齐 hook_dispatcher._should_record_circuit_failure
func shouldRecordCircuitFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"超时", "timeout", "e_timeout", "e_plugin_crashed", "崩溃"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
