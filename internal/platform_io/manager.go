package platformio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// InboundDispatcher 已通过 Broker 审核的入站封装继续送入 Core
type InboundDispatcher func(ctx context.Context, envelope InboundMessageEnvelope) error

// Router 路由与入站分发门面
type Router interface {
	Register(adapter Adapter) error
	RouteInbound(ctx context.Context, envelope InboundMessageEnvelope) (bool, error)
}

// Deduplicator 入站去重门面
type Deduplicator interface {
	MarkSeen(dedupeKey string) bool
	Clear()
}

// LegacySendAccount 旧链 fallback 驱动对应的平台账号
type LegacySendAccount struct {
	Platform  string
	AccountID string
}

// defaultWebUIBotUserID WebUI/Internal 平台在 legacy 通道中的机器人账号
// 对齐 MaiBot bot_account_service.WEBUI_BOT_USER_ID
const defaultWebUIBotUserID = "__maibot_webui_bot__"

// SetLegacySender 为指定平台注入旧链发送通道；平台驱动不存在时返回错误
func (m *Manager) SetLegacySender(platform string, sender PreparedMessageSender) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	driver, ok := m.legacySendDrivers[platform]
	if !ok {
		return fmt.Errorf("平台 %s 未注册 legacy 发送驱动", platform)
	}
	typed, ok := driver.(*LegacyPlatformDriver)
	if !ok {
		return fmt.Errorf("平台 %s 的驱动不是 legacy 驱动", platform)
	}
	typed.SetSender(sender)
	return nil
}

// Manager 统一协调 Platform IO 的路由、去重、出站跟踪与适配器生命周期，对齐 MaiBot PlatformIOManager
type Manager struct {
	mu sync.RWMutex

	registry             *AdapterRegistry
	sendRouteTable       *RouteTable
	receiveRouteTable    *RouteTable
	deduplicator         *MessageDeduplicator
	outboundTracker      *OutboundTracker
	inboundDispatcher    InboundDispatcher
	legacySendDrivers    map[string]Adapter
	defaultLegacySender  PreparedMessageSender
	pluginInvoker        PluginGatewayInvoker
	pluginFileURLInvoker PluginFileURLInvoker
	started              bool

	// 入站后台分发任务：对齐 Python _inbound_dispatch_tasks 的取消/回收语义
	inboundMu       sync.Mutex
	inboundWG       sync.WaitGroup
	inboundCtx      context.Context
	inboundCancel   context.CancelFunc
	inboundStopping bool
}

// NewManager 创建 Broker 管理器
func NewManager() (*Manager, error) {
	dedupe, err := DefaultDeduplicator()
	if err != nil {
		return nil, err
	}
	inboundCtx, inboundCancel := context.WithCancel(context.Background())
	return &Manager{
		registry:          NewAdapterRegistry(),
		sendRouteTable:    NewRouteTable(),
		receiveRouteTable: NewRouteTable(),
		deduplicator:      dedupe,
		outboundTracker:   DefaultOutboundTracker(),
		inboundCtx:        inboundCtx,
		inboundCancel:     inboundCancel,
		legacySendDrivers: make(map[string]Adapter),
	}, nil
}

// OutboundTracker 返回出站跟踪器
func (m *Manager) OutboundTracker() *OutboundTracker {
	return m.outboundTracker
}

// IsStarted 返回 Broker 是否处于运行态
func (m *Manager) IsStarted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.started
}

// Register 注册适配器并挂接 accept_inbound 回调；运行态下拒绝，请改用 AddDriver
func (m *Manager) Register(adapter Adapter) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errManagerRunning
	}
	return m.registerLocked(adapter)
}

func (m *Manager) registerLocked(adapter Adapter) error {
	if err := m.registry.Register(adapter); err != nil {
		return err
	}
	m.applyDriverDefaultsLocked(adapter)
	adapter.OnMessage(m.acceptInbound)
	return nil
}

// applyDriverDefaultsLocked 为未显式注入通道的旧链/插件驱动补默认发送面
func (m *Manager) applyDriverDefaultsLocked(adapter Adapter) {
	switch typed := adapter.(type) {
	case *LegacyPlatformDriver:
		if !typed.SenderConfigured() && m.defaultLegacySender != nil {
			typed.SetSender(m.defaultLegacySender)
		}
	case *PluginPlatformDriver:
		if !typed.InvokerConfigured() && m.pluginInvoker != nil {
			typed.SetInvoker(m.pluginInvoker)
		}
		if !typed.FileURLInvokerConfigured() && m.pluginFileURLInvoker != nil {
			typed.SetFileURLInvoker(m.pluginFileURLInvoker)
		}
	}
}

// SetDefaultLegacySender 注入旧链兜底发送通道；已显式配置的驱动不被覆盖
func (m *Manager) SetDefaultLegacySender(sender PreparedMessageSender) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultLegacySender = sender
	if sender == nil {
		return
	}
	for _, adapter := range m.registry.List(nil, "") {
		if legacy, ok := adapter.(*LegacyPlatformDriver); ok && !legacy.SenderConfigured() {
			legacy.SetSender(sender)
		}
	}
}

// SetPluginGatewayInvoker 注入插件消息网关调用通道；后续注册的插件驱动自动继承
func (m *Manager) SetPluginGatewayInvoker(invoker PluginGatewayInvoker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pluginInvoker = invoker
	if invoker == nil {
		return
	}
	for _, adapter := range m.registry.List(nil, "") {
		if plugin, ok := adapter.(*PluginPlatformDriver); ok && !plugin.InvokerConfigured() {
			plugin.SetInvoker(invoker)
		}
	}
}

// SetPluginFileURLInvoker 注入插件 API 文件解析通道；后续注册的插件驱动自动继承
func (m *Manager) SetPluginFileURLInvoker(invoker PluginFileURLInvoker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pluginFileURLInvoker = invoker
	if invoker == nil {
		return
	}
	for _, adapter := range m.registry.List(nil, "") {
		if plugin, ok := adapter.(*PluginPlatformDriver); ok && !plugin.FileURLInvokerConfigured() {
			plugin.SetFileURLInvoker(invoker)
		}
	}
}

// AddDriver 向运行中的 Broker 注册并启动驱动；未运行时等价于 Register
func (m *Manager) AddDriver(ctx context.Context, adapter Adapter) error {
	m.mu.Lock()
	if err := m.registerLocked(adapter); err != nil {
		m.mu.Unlock()
		return err
	}
	running := m.started
	m.mu.Unlock()
	if !running {
		return nil
	}
	if err := adapter.Start(ctx); err != nil {
		m.mu.Lock()
		m.unregisterLocked(adapter.Descriptor().DriverID)
		m.mu.Unlock()
		return err
	}
	return nil
}

// Unregister 注销适配器并清理路由绑定；运行态下拒绝，请改用 RemoveDriver
func (m *Manager) Unregister(driverID string) {
	_ = m.UnregisterChecked(driverID)
}

// UnregisterChecked 与 Python unregister_driver 一致：运行态返回错误，避免绕过 Stop
func (m *Manager) UnregisterChecked(driverID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errManagerRunningUnregister
	}
	m.unregisterLocked(driverID)
	return nil
}

func (m *Manager) unregisterLocked(driverID string) Adapter {
	adapter := m.registry.Unregister(driverID)
	if adapter != nil {
		if clearable, ok := adapter.(interface{ ClearOnMessage() }); ok {
			clearable.ClearOnMessage()
		}
	}
	m.sendRouteTable.RemoveBindingsByDriver(driverID)
	m.receiveRouteTable.RemoveBindingsByDriver(driverID)
	for platform, legacy := range m.legacySendDrivers {
		if legacy != nil && legacy.Descriptor().DriverID == driverID {
			delete(m.legacySendDrivers, platform)
		}
	}
	return adapter
}

// RemoveDriver 从运行中的 Broker 停止并移除驱动；未运行时等价于 Unregister
func (m *Manager) RemoveDriver(ctx context.Context, driverID string) (Adapter, error) {
	m.mu.RLock()
	running := m.started
	adapter := m.registry.Get(driverID)
	m.mu.RUnlock()
	if !running {
		m.Unregister(driverID)
		return adapter, nil
	}
	if adapter == nil {
		return nil, nil
	}
	if err := adapter.Stop(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.unregisterLocked(driverID)
	m.mu.Unlock()
	return adapter, nil
}

// Start 启动全部已注册适配器；启动前同步旧链 fallback 驱动，对齐 ensure_send_pipeline_ready
func (m *Manager) Start(ctx context.Context) error {
	m.mu.RLock()
	running := m.started
	m.mu.RUnlock()
	if running {
		return nil
	}
	// 每次启动重建后台分发上下文；Stop 会取消并等待上一轮任务
	m.resetInboundDispatch()
	// 主程序只调用 Start 时也要把 legacy 通道接回，避免 webui/internal 等平台失去 fallback
	if err := m.SyncLegacySendDrivers(ctx, nil); err != nil {
		m.stopInboundDispatch()
		return err
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	adapters := m.registry.List(nil, "")
	m.mu.Unlock()

	started := make([]Adapter, 0, len(adapters))
	for _, adapter := range adapters {
		if err := adapter.Start(ctx); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].Stop(ctx)
			}
			m.stopInboundDispatch()
			return err
		}
		started = append(started, adapter)
	}

	m.mu.Lock()
	m.started = true
	m.mu.Unlock()
	return nil
}

// EnsureSendPipelineReady 同步旧链 fallback 驱动并在需要时启动 Broker
func (m *Manager) EnsureSendPipelineReady(ctx context.Context) error {
	if err := m.SyncLegacySendDrivers(ctx, nil); err != nil {
		return err
	}
	return m.Start(ctx)
}

// Stop 停止全部适配器并清空运行期去重与出站跟踪状态
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		m.stopInboundDispatch()
		return nil
	}
	adapters := m.registry.List(nil, "")
	m.mu.Unlock()

	var errs []error
	for i := len(adapters) - 1; i >= 0; i-- {
		if err := adapters[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", adapters[i].Descriptor().DriverID, err))
		}
	}

	// 先停驱动，再取消并等待在途入站任务，最后清运行期状态，对齐 Python stop 顺序
	m.stopInboundDispatch()
	m.mu.Lock()
	m.started = false
	m.deduplicator.Clear()
	if m.outboundTracker != nil {
		m.outboundTracker.Clear()
	}
	m.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", errStopPartial, errors.Join(errs...))
	}
	return nil
}

// SetInboundDispatcher 设置统一入站分发回调
func (m *Manager) SetInboundDispatcher(dispatcher InboundDispatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inboundDispatcher = dispatcher
}

// ClearInboundDispatcher 清除统一入站分发回调
func (m *Manager) ClearInboundDispatcher() {
	m.SetInboundDispatcher(nil)
}

// HasInboundDispatcher 是否已配置入站分发回调
func (m *Manager) HasInboundDispatcher() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.inboundDispatcher != nil
}

// BindSendRoute 绑定发送路由
func (m *Manager) BindSendRoute(binding RouteBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	driver := m.registry.Get(binding.DriverID)
	if driver == nil {
		return errAdapterNotFound
	}
	if err := validateBindingAgainstAdapter(binding, driver.Descriptor()); err != nil {
		return err
	}
	m.sendRouteTable.Bind(binding)
	return nil
}

// BindReceiveRoute 绑定接收路由
func (m *Manager) BindReceiveRoute(binding RouteBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	driver := m.registry.Get(binding.DriverID)
	if driver == nil {
		return errAdapterNotFound
	}
	if err := validateBindingAgainstAdapter(binding, driver.Descriptor()); err != nil {
		return err
	}
	m.receiveRouteTable.Bind(binding)
	return nil
}

// UnbindSendRoute 移除发送路由绑定
func (m *Manager) UnbindSendRoute(routeKey RouteKey, driverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendRouteTable.Unbind(routeKey, driverID)
}

// UnbindReceiveRoute 移除接收路由绑定
func (m *Manager) UnbindReceiveRoute(routeKey RouteKey, driverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.receiveRouteTable.Unbind(routeKey, driverID)
}

// ResolveSendAdapters 解析发送路由命中的适配器；无显式绑定时回退平台级 legacy 驱动
func (m *Manager) ResolveSendAdapters(routeKey RouteKey) []Adapter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.resolveSendAdaptersLocked(routeKey)
}

func (m *Manager) resolveSendAdaptersLocked(routeKey RouteKey) []Adapter {
	var adapters []Adapter
	seen := make(map[string]struct{})
	for _, binding := range m.sendRouteTable.ResolveBindings(routeKey) {
		adapter := m.registry.Get(binding.DriverID)
		if adapter == nil {
			continue
		}
		if _, ok := seen[binding.DriverID]; ok {
			continue
		}
		seen[binding.DriverID] = struct{}{}
		adapters = append(adapters, adapter)
	}
	if len(adapters) > 0 {
		return adapters
	}
	fallback := m.legacySendDrivers[routeKey.Platform]
	if fallback != nil {
		desc := fallback.Descriptor()
		accountMatches := desc.AccountID == "" || routeKey.AccountID == "" || routeKey.AccountID == desc.AccountID
		scopeMatches := desc.Scope == "" || routeKey.Scope == "" || routeKey.Scope == desc.Scope
		if accountMatches && scopeMatches {
			adapters = append(adapters, fallback)
		}
	}
	if len(adapters) > 0 {
		return adapters
	}
	// 多平台汇聚驱动（如 maim_message WS）不绑定固定 platform，显式声明 allow_any_platform 后按路由键兜底
	for _, adapter := range m.registry.List(nil, "") {
		desc := adapter.Descriptor()
		if desc.Platform != "" {
			continue
		}
		allowAny, _ := desc.Metadata["allow_any_platform"].(bool)
		if !allowAny {
			continue
		}
		accountMatches := desc.AccountID == "" || routeKey.AccountID == "" || routeKey.AccountID == desc.AccountID
		scopeMatches := desc.Scope == "" || routeKey.Scope == "" || routeKey.Scope == desc.Scope
		if accountMatches && scopeMatches {
			adapters = append(adapters, adapter)
		}
	}
	return adapters
}

// IsTargetAllowed 判断路由键下是否存在允许该出站目标的驱动
func (m *Manager) IsTargetAllowed(routeKey RouteKey, chatType, targetID string) bool {
	for _, adapter := range m.ResolveSendAdapters(routeKey) {
		result, err := m.evaluateTargetPolicy(adapter, routeKey, chatType, targetID)
		if err != nil {
			continue
		}
		if result.Allowed {
			return true
		}
	}
	return false
}

// SendMessage 向命中的全部发送适配器广播出站消息
func (m *Manager) SendMessage(ctx context.Context, outbound OutboundMessageEnvelope, routeKey RouteKey) DeliveryBatch {
	// 对齐 Python send_service 的 send_api_<ms>：出站消息必须有内部编号，
	// 适配器回执、消息跟踪与插件网关 message_id 都依赖它
	if strings.TrimSpace(outbound.InternalMessageID) == "" {
		outbound.InternalMessageID = fmt.Sprintf("send_api_%d", time.Now().UnixMilli())
	}
	adapters := m.ResolveSendAdapters(routeKey)
	batch := DeliveryBatch{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
	}
	chatType := string(outbound.ChatType)
	if chatType == "" {
		chatType = string(ChatTypePrivate)
	}
	for _, adapter := range adapters {
		desc := adapter.Descriptor()
		policyResult, policyErr := m.evaluateTargetPolicy(adapter, routeKey, chatType, outbound.TargetID)
		if policyErr != nil {
			batch.Receipts = append(batch.Receipts, DeliveryReceipt{
				InternalMessageID: outbound.InternalMessageID,
				RouteKey:          routeKey,
				Status:            DeliveryStatusFailed,
				DriverID:          desc.DriverID,
				DriverKind:        desc.Kind,
				Error:             "adapter_policy: " + policyErr.Error(),
			})
			continue
		}
		if !policyResult.Allowed {
			batch.Receipts = append(batch.Receipts, DeliveryReceipt{
				InternalMessageID: outbound.InternalMessageID,
				RouteKey:          routeKey,
				Status:            DeliveryStatusDropped,
				DriverID:          desc.DriverID,
				DriverKind:        desc.Kind,
				Error:             "adapter_policy: " + policyResult.Reason,
				Metadata:          map[string]any{"adapter_policy": policyResult.ToMap()},
			})
			continue
		}
		if m.outboundTracker != nil {
			if _, err := m.outboundTracker.BeginTracking(outbound.InternalMessageID, routeKey, desc.DriverID, outbound.Metadata); err != nil {
				batch.Receipts = append(batch.Receipts, DeliveryReceipt{
					InternalMessageID: outbound.InternalMessageID,
					RouteKey:          routeKey,
					Status:            DeliveryStatusFailed,
					DriverID:          desc.DriverID,
					DriverKind:        desc.Kind,
					Error:             err.Error(),
				})
				continue
			}
		}
		receipt, err := adapter.Send(ctx, outbound, routeKey)
		if err != nil {
			if receipt.Status == "" {
				// 驱动未返回可用回执时才兜底构造，避免覆盖驱动给出的精确错误
				receipt = DeliveryReceipt{
					InternalMessageID: outbound.InternalMessageID,
					RouteKey:          routeKey,
					Status:            DeliveryStatusFailed,
					DriverID:          desc.DriverID,
					DriverKind:        desc.Kind,
					Error:             err.Error(),
				}
			} else if receipt.Error == "" {
				receipt.Error = err.Error()
			}
		}
		if m.outboundTracker != nil {
			m.outboundTracker.FinishTracking(receipt)
		}
		batch.Receipts = append(batch.Receipts, receipt)
	}
	return batch
}

func (m *Manager) evaluateTargetPolicy(adapter Adapter, routeKey RouteKey, chatType, targetID string) (AdapterPolicyResult, error) {
	desc := adapter.Descriptor()
	manager, err := GetAdapterPolicyManager()
	if err != nil {
		return AdapterPolicyResult{}, err
	}
	gatewayName := ""
	if desc.Metadata != nil {
		gatewayName = stringValue(desc.Metadata["gateway_name"])
	}
	accountID := routeKey.AccountID
	if accountID == "" {
		accountID = desc.AccountID
	}
	scope := routeKey.Scope
	if scope == "" {
		scope = desc.Scope
	}
	identity := AdapterIdentity{
		AdapterID:   desc.DriverID,
		PluginID:    desc.PluginID,
		GatewayName: gatewayName,
		Platform:    routeKey.Platform,
		AccountID:   accountID,
		Scope:       scope,
	}
	return manager.Evaluate(identity, chatType, targetID)
}

// SyncLegacySendDrivers 按传入账号（空则按平台本地账号）同步旧链 fallback 驱动
func (m *Manager) SyncLegacySendDrivers(ctx context.Context, accounts []LegacySendAccount) error {
	desired := make(map[string]string)
	for _, account := range accounts {
		platform := strings.TrimSpace(account.Platform)
		if platform == "" || IsInternalPlatform(platform) {
			continue
		}
		desired[platform] = strings.TrimSpace(account.AccountID)
	}
	// WebUI 与 internal 平台不走外部适配器配置，但仍通过 legacy 通道进入广播链路
	for _, platform := range []string{"webui", "internal"} {
		if _, ok := desired[platform]; !ok {
			desired[platform] = defaultWebUIBotUserID
		}
	}

	m.mu.Lock()
	current := make(map[string]Adapter, len(m.legacySendDrivers))
	for platform, adapter := range m.legacySendDrivers {
		current[platform] = adapter
	}
	defaultSender := m.defaultLegacySender
	m.mu.Unlock()

	for platform, adapter := range current {
		if _, ok := desired[platform]; ok {
			continue
		}
		// 未显式传入账号清单时保留已注入 sender 的手工 legacy 驱动，避免 Start 误删
		if len(accounts) == 0 {
			if legacy, ok := adapter.(*LegacyPlatformDriver); ok && legacy.SenderConfigured() {
				desired[platform] = adapter.Descriptor().AccountID
				continue
			}
		}
		if err := m.removeLegacySendDriver(ctx, platform); err != nil {
			return err
		}
	}
	for platform, accountID := range desired {
		m.mu.RLock()
		existing := m.legacySendDrivers[platform]
		running := m.started
		m.mu.RUnlock()
		if existing != nil && existing.Descriptor().AccountID == accountID {
			continue
		}
		if existing != nil {
			if err := m.removeLegacySendDriver(ctx, platform); err != nil {
				return err
			}
		}
		driver := NewLegacyPlatformDriver(LegacyDriverOptions{
			Platform:  platform,
			AccountID: accountID,
			Sender:    defaultSender,
		})
		if running {
			if err := m.AddDriver(ctx, driver); err != nil {
				return err
			}
		} else if err := m.Register(driver); err != nil {
			return err
		}
		m.mu.Lock()
		m.legacySendDrivers[platform] = driver
		m.mu.Unlock()
	}
	return nil
}

func (m *Manager) removeLegacySendDriver(ctx context.Context, platform string) error {
	m.mu.RLock()
	driver := m.legacySendDrivers[platform]
	running := m.started
	m.mu.RUnlock()
	if driver == nil {
		return nil
	}
	if running {
		if _, err := m.RemoveDriver(ctx, driver.Descriptor().DriverID); err != nil {
			return err
		}
	} else {
		m.Unregister(driver.Descriptor().DriverID)
	}
	m.mu.Lock()
	delete(m.legacySendDrivers, platform)
	m.mu.Unlock()
	return nil
}

// RegisterLegacySendDriver 手工注册平台级旧链 fallback 驱动并注入发送通道
func (m *Manager) RegisterLegacySendDriver(ctx context.Context, platform, accountID string, sender PreparedMessageSender) error {
	if sender == nil {
		m.mu.RLock()
		sender = m.defaultLegacySender
		m.mu.RUnlock()
	}
	driver := NewLegacyPlatformDriver(LegacyDriverOptions{
		Platform:  platform,
		AccountID: accountID,
		Sender:    sender,
	})
	if m.IsStarted() {
		if err := m.AddDriver(ctx, driver); err != nil {
			return err
		}
	} else if err := m.Register(driver); err != nil {
		return err
	}
	m.mu.Lock()
	m.legacySendDrivers[strings.TrimSpace(platform)] = driver
	m.mu.Unlock()
	return nil
}

// RouteInbound 对外暴露的入站路由入口
func (m *Manager) RouteInbound(ctx context.Context, envelope InboundMessageEnvelope) (bool, error) {
	return m.routeInbound(ctx, envelope)
}

// Dedupe 暴露去重器供测试或扩展
func (m *Manager) Dedupe() Deduplicator {
	return m.deduplicator
}

// HasReceiveBinding 判断入站路由是否已绑定指定驱动（用于 legacy 直通判定）
func (m *Manager) HasReceiveBinding(routeKey RouteKey, driverID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.receiveRouteTable.HasBindingForDriver(routeKey, driverID)
}

func (m *Manager) acceptInbound(ctx context.Context, envelope InboundMessageEnvelope) error {
	_, err := m.routeInbound(ctx, envelope)
	return err
}

func (m *Manager) routeInbound(ctx context.Context, envelope InboundMessageEnvelope) (bool, error) {
	m.mu.RLock()
	receiveTable := m.receiveRouteTable
	dispatcher := m.inboundDispatcher
	dedupe := m.deduplicator
	m.mu.RUnlock()

	if envelope.ReceivedAt.IsZero() {
		envelope.ReceivedAt = time.Now()
	}
	if envelope.ChatMessage != nil && envelope.ChatMessage.ReceivedAt.IsZero() {
		envelope.ChatMessage.ReceivedAt = envelope.ReceivedAt
	}

	if !receiveTable.HasBindingForDriver(envelope.RouteKey, envelope.DriverID) {
		return false, nil
	}
	if dispatcher == nil {
		return false, nil
	}

	dedupeKey := buildInboundDedupeKey(envelope)
	if dedupeKey != "" {
		if !dedupe.MarkSeen(dedupeKey) {
			return false, nil
		}
	}

	if !m.scheduleInboundDispatch(ctx, envelope, dispatcher) {
		return false, nil
	}
	return true, nil
}

func buildInboundDedupeKey(envelope InboundMessageEnvelope) string {
	raw := envelope.DedupeKey
	if raw == "" {
		raw = envelope.ExternalMessageID
	}
	if raw == "" && envelope.ChatMessage != nil {
		raw = envelope.ChatMessage.MessageID
	}
	raw = trimNonEmpty(raw)
	if raw == "" {
		return ""
	}
	// 多平台汇聚驱动共用 driver id，去重键必须带上 platform/account/scope，避免跨平台同 ID 误杀
	route := envelope.RouteKey
	return envelope.DriverID + ":" + route.Platform + ":" + route.AccountID + ":" + route.Scope + ":" + raw
}

func validateBindingAgainstAdapter(binding RouteBinding, desc DriverDescriptor) error {
	if binding.DriverKind != desc.Kind {
		return errBindingKindMismatch
	}
	// legacy.maimmsg 单驱动按连接 platform 动态绑路由，描述符 Platform 为空
	if desc.DriverID != "legacy.maimmsg" && binding.RouteKey.Platform != desc.Platform {
		return errBindingPlatformMismatch
	}
	if desc.AccountID != "" && binding.RouteKey.AccountID != "" && binding.RouteKey.AccountID != desc.AccountID {
		return errBindingAccountMismatch
	}
	if desc.Scope != "" && binding.RouteKey.Scope != "" && binding.RouteKey.Scope != desc.Scope {
		return errBindingScopeMismatch
	}
	return nil
}
