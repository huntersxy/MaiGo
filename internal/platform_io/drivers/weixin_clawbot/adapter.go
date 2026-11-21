package weixin_clawbot

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// Orchestrator 多账号生命周期；NativeRuntime 只持有这一个 Adapter
type Orchestrator struct {
	platformio.BaseAdapter
	mu        sync.Mutex
	lifecycle sync.Mutex
	desc      platformio.DriverDescriptor
	settings  Settings
	hostHooks *HostHooks
	slots     map[int]*AccountConnection
	adapters  map[int]*SlotAdapter
	started   bool
	dialed    bool
	cancel    context.CancelFunc
}

func NewOrchestrator(settings Settings, hooks *HostHooks) *Orchestrator {
	desc := platformio.BuildNativeAdapterDescriptor(
		PluginID,
		GatewayBaseName,
		PlatformWeixin,
		"",
		"",
		map[string]any{
			"route_type": RouteTypeDuplex,
			"protocol":   ProtocolILink,
		},
	)
	return &Orchestrator{
		BaseAdapter: platformio.NewBaseAdapter(desc),
		desc:        desc,
		settings:    settings,
		hostHooks:   hooks,
		slots:       map[int]*AccountConnection{},
		adapters:    map[int]*SlotAdapter{},
	}
}

func (o *Orchestrator) Descriptor() platformio.DriverDescriptor {
	if o == nil {
		return platformio.DriverDescriptor{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.desc
}

func (o *Orchestrator) Start(ctx context.Context) error {
	if o == nil {
		return fmt.Errorf("weixin orchestrator 为空")
	}
	o.lifecycle.Lock()
	defer o.lifecycle.Unlock()
	o.mu.Lock()
	var connections []*AccountConnection
	defer func() {
		o.mu.Unlock()
		if o.started {
			for _, conn := range connections {
				conn.Start(false)
			}
		}
	}()
	if o.started {
		return nil
	}
	if !o.settings.Plugin.Enabled {
		return nil
	}
	dataDir := resolveDataDir(o.hostHooks)
	for slot := 0; slot < MaxAccounts; slot++ {
		store := NewStore(filepath.Join(dataDir, AccountFileName(slot)))
		if err := store.Load(); err != nil {
			return err
		}
		conn := NewAccountConnection(o, slot, store)
		adapter := NewSlotAdapter(o, slot, conn)
		o.slots[slot] = conn
		o.adapters[slot] = adapter
		if o.hostHooks != nil && o.hostHooks.RegisterDriver != nil {
			if err := o.hostHooks.RegisterDriver(ctx, GatewayName(slot), adapter); err != nil {
				return err
			}
			// RegisterNativeGatewayDriver 写死 Ready=true；未连通槽先压回 false
			if o.hostHooks.ReportGatewayState != nil {
				st := store.State()
				_ = o.hostHooks.ReportGatewayState(ctx, GatewayName(slot), false, st.AccountID, st.Remark)
			}
		}
		connections = append(connections, conn)
	}
	if o.hostHooks != nil && o.hostHooks.APIs != nil {
		RegisterLocalAPIs(o.hostHooks.APIs, o)
	}
	_, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.started = true
	zap.L().Info("原生微信适配器已启动", zap.Int("slots", MaxAccounts))
	return nil
}

func (o *Orchestrator) Stop(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.lifecycle.Lock()
	defer o.lifecycle.Unlock()
	o.mu.Lock()
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	connections := make([]*AccountConnection, 0, len(o.slots))
	for _, conn := range o.slots {
		connections = append(connections, conn)
	}
	o.started, o.dialed = false, false
	o.mu.Unlock()
	for _, conn := range connections {
		if err := conn.Stop(ctx); err != nil {
			return err
		}
	}
	if o.hostHooks != nil && o.hostHooks.APIs != nil {
		o.hostHooks.APIs.RemoveAPIsByPlugin(PluginID)
	}
	return nil
}

// Send Orchestrator 自身不接出站；Host 按 DriverID 打到 SlotAdapter
func (o *Orchestrator) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey) (platformio.DeliveryReceipt, error) {
	_ = ctx
	receipt := platformio.DeliveryReceipt{
		InternalMessageID: outbound.InternalMessageID,
		RouteKey:          routeKey,
		Status:            platformio.DeliveryStatusFailed,
		DriverID:          o.Descriptor().DriverID,
		DriverKind:        platformio.DriverKindPlugin,
		Error:             "请通过对应账号网关出站",
	}
	return receipt, fmt.Errorf("%s", receipt.Error)
}

func (o *Orchestrator) Dialed() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dialed
}

func (o *Orchestrator) Settings() Settings {
	if o == nil {
		return DefaultSettings()
	}
	return o.settings
}

func (o *Orchestrator) Slot(slot int) (*AccountConnection, *SlotAdapter, bool) {
	if o == nil {
		return nil, nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	conn, ok := o.slots[slot]
	if !ok {
		return nil, nil, false
	}
	return conn, o.adapters[slot], true
}

func (o *Orchestrator) markDialed() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dialed = true
}

func resolveDataDir(hooks *HostHooks) string {
	if hooks != nil && hooks.DataDir != nil {
		if dir := hooks.DataDir(); dir != "" {
			return dir
		}
	}
	return filepath.Join("data", "plugins", PluginID)
}
