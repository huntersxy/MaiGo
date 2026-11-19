package weixin_clawbot

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// AccountConnection 单账号轮询与发送
type AccountConnection struct {
	mu      sync.Mutex
	owner   *Orchestrator
	slot    int
	Store   *Store
	gateway string
	client  *Client
	cancel  context.CancelFunc
	ready   bool
	status  string
	qr      string
	seen    map[string]struct{}
}

func NewAccountConnection(owner *Orchestrator, slot int, store *Store) *AccountConnection {
	return &AccountConnection{
		owner:   owner,
		slot:    slot,
		Store:   store,
		gateway: GatewayName(slot),
		status:  "等待扫码",
		seen:    map[string]struct{}{},
	}
}

func (c *AccountConnection) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *AccountConnection) Status() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *AccountConnection) QR() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.qr
}

func (c *AccountConnection) Start(login bool) {
	if c == nil || c.owner == nil {
		return
	}
	if !c.owner.settings.Plugin.Enabled {
		c.setStatus("未启用：请先打开插件配置中的启用开关")
		return
	}
	state := c.Store.State()
	if !login && state.Token == "" {
		c.setStatus("等待扫码或重新登录")
		return
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.mu.Unlock()
	c.owner.markDialed()
	go c.run(runCtx, login)
}

func (c *AccountConnection) Stop(ctx context.Context) error {
	_ = ctx
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.ready = false
	c.qr = ""
	c.mu.Unlock()
	if c.owner != nil && c.owner.hostHooks != nil && c.owner.hostHooks.ReportGatewayState != nil {
		_ = c.owner.hostHooks.ReportGatewayState(context.Background(), c.gateway, false, c.Store.State().AccountID, c.Store.State().Remark)
	}
	return nil
}

func (c *AccountConnection) setStatus(status string) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func (c *AccountConnection) run(ctx context.Context, login bool) {
	retry := time.Duration(c.owner.settings.Weixin.RetrySeconds) * time.Second
	if retry <= 0 {
		retry = time.Duration(DefaultRetrySeconds) * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.runOnce(ctx, login)
		login = false
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.ready = false
		c.mu.Unlock()
		if c.owner != nil && c.owner.hostHooks != nil && c.owner.hostHooks.ReportGatewayState != nil {
			_ = c.owner.hostHooks.ReportGatewayState(context.Background(), c.gateway, false, c.Store.State().AccountID, c.Store.State().Remark)
		}
		if err != nil {
			zap.L().Warn("微信连接暂时不可用，稍后重连", zap.Int("slot", c.slot), zap.Error(err))
			c.setStatus("连接异常，重试中")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func (c *AccountConnection) runOnce(ctx context.Context, login bool) error {
	state := c.Store.State()
	baseURL := state.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	maxBytes := c.owner.settings.Weixin.MaxImageMB * 1024 * 1024
	client, err := NewClient(baseURL, state.Token, maxBytes)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.client = client
	c.mu.Unlock()
	defer client.Close()

	if login || state.Token == "" {
		if err := c.login(ctx, client); err != nil {
			return err
		}
		state = c.Store.State()
		client.SetToken(state.Token)
	}

	if err := c.markReady(true); err != nil {
		return err
	}
	c.setStatus("已连接")
	cursor := state.Cursor
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		batch, err := client.Updates(ctx, cursor)
		if err != nil {
			if apiErr, ok := err.(*APIError); ok && (apiErr.Code == -14 || apiErr.Code == 401) {
				c.setStatus("登录已失效，请重新扫码")
			}
			return err
		}
		if next := asString(batch["get_updates_buf"]); next != "" {
			cursor = next
			_ = c.Store.Update(func(st *AccountState) { st.Cursor = next })
			_ = c.Store.Save()
		}
		if err := c.processBatch(ctx, client, batch); err != nil {
			return err
		}
	}
}

func (c *AccountConnection) login(ctx context.Context, client *Client) error {
	// P0：登录二维码流程以状态文案暴露；完整扫码循环属 remaining
	c.setStatus("等待扫码（Go 侧扫码管线 P1）")
	_ = ctx
	_ = client
	state := c.Store.State()
	if state.Token == "" {
		return fmt.Errorf("微信尚未登录：请先用 Python 适配器完成扫码或等待扫码管线补齐")
	}
	return nil
}

func (c *AccountConnection) markReady(ready bool) error {
	state := c.Store.State()
	c.mu.Lock()
	c.ready = ready
	c.mu.Unlock()
	if adapter, ok := c.owner.adapters[c.slot]; ok && adapter != nil {
		adapter.refreshDescriptor(state.AccountID)
	}
	if c.owner != nil && c.owner.hostHooks != nil && c.owner.hostHooks.ReportGatewayState != nil {
		return c.owner.hostHooks.ReportGatewayState(context.Background(), c.gateway, ready, state.AccountID, state.Remark)
	}
	return nil
}

func (c *AccountConnection) processBatch(ctx context.Context, client *Client, batch map[string]any) error {
	_ = client
	msgs, _ := batch["msgs"].([]any)
	allowed := map[string]struct{}{}
	for _, user := range c.owner.settings.Weixin.AllowedUsers {
		allowed[user] = struct{}{}
	}
	for _, raw := range msgs {
		msg, ok := asMap(raw)
		if !ok {
			continue
		}
		if n, ok := asInt(msg["message_type"]); ok && n != 1 {
			continue
		}
		from := asString(msg["from_user_id"])
		if len(allowed) > 0 {
			if _, ok := allowed[from]; !ok {
				continue
			}
		}
		key := MessageID(c.Store.State().AccountID, msg)
		c.mu.Lock()
		if _, seen := c.seen[key]; seen {
			c.mu.Unlock()
			continue
		}
		c.seen[key] = struct{}{}
		c.mu.Unlock()

		if contextToken := asString(msg["context_token"]); contextToken != "" && from != "" {
			_ = c.Store.Update(func(st *AccountState) {
				if st.Contexts == nil {
					st.Contexts = map[string]string{}
				}
				st.Contexts[from] = contextToken
				if st.UserID == "" {
					st.UserID = from
				}
			})
			_ = c.Store.Save()
		}

		segments := decodeInboundItems(msg)
		nickname := c.nickname(from)
		message := Inbound(c.Store.State().AccountID, msg, segments, nickname)
		var sink InboundSink
		if c.owner != nil && c.owner.hostHooks != nil {
			sink = c.owner.hostHooks.Inbound
		}
		if err := deliverInbound(ctx, sink, c.gateway, c.Store.State().AccountID, message); err != nil {
			return err
		}
	}
	return nil
}

func decodeInboundItems(msg map[string]any) []map[string]any {
	segments := make([]map[string]any, 0)
	items, _ := msg["item_list"].([]any)
	for _, raw := range items {
		item, ok := asMap(raw)
		if !ok {
			continue
		}
		switch asIntOrZero(item["type"]) {
		case 1:
			text := asString(item["text"])
			if text == "" {
				if nested, ok := asMap(item["text_item"]); ok {
					text = asString(nested["text"])
				}
			}
			if text != "" {
				segments = append(segments, map[string]any{"type": "text", "data": text})
			}
		case 2:
			segments = append(segments, map[string]any{"type": "text", "data": "[image]"})
		default:
			segments = append(segments, map[string]any{"type": "text", "data": "[unsupported]"})
		}
	}
	if len(segments) == 0 {
		if text := asString(msg["text"]); text != "" {
			segments = append(segments, map[string]any{"type": "text", "data": text})
		}
	}
	return segments
}

func asIntOrZero(v any) int {
	n, _ := asInt(v)
	return n
}

func (c *AccountConnection) nickname(user string) string {
	state := c.Store.State()
	bound := state.UserID
	if bound == "" && len(state.Contexts) == 1 {
		for k := range state.Contexts {
			bound = k
		}
	}
	if user == bound && state.Remark != "" {
		return state.Remark
	}
	return user
}

func (c *AccountConnection) Send(ctx context.Context, outbound platformio.OutboundMessageEnvelope, routeKey platformio.RouteKey, receipt platformio.DeliveryReceipt) (platformio.DeliveryReceipt, error) {
	if !c.Ready() {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "微信网关尚未就绪"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	state := c.Store.State()
	if routeKey.AccountID != "" && state.AccountID != "" && routeKey.AccountID != state.AccountID {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "出站消息与微信账号归属不一致"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	maxBytes := c.owner.settings.Weixin.MaxImageMB * 1024 * 1024
	target, parts, err := EncodeOutbound(outbound, maxBytes)
	if err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	contextToken := state.Contexts[target]
	if contextToken == "" {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "请先从微信发消息"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = "微信客户端未连接"
		return receipt, fmt.Errorf("%s", receipt.Error)
	}
	items := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case "text":
			items = append(items, map[string]any{"type": 1, "text_item": map[string]any{"text": part.Text}})
		case "image":
			// P0：图片上传未接，降级文本
			items = append(items, map[string]any{"type": 1, "text_item": map[string]any{"text": "[image]"}})
		}
	}
	clientID := fmt.Sprintf("maigo-%d", time.Now().UnixNano())
	if err := client.Send(ctx, target, contextToken, items, clientID); err != nil {
		receipt.Status = platformio.DeliveryStatusFailed
		receipt.Error = err.Error()
		return receipt, err
	}
	receipt.Status = platformio.DeliveryStatusSent
	receipt.ExternalMessageID = clientID
	return receipt, nil
}
