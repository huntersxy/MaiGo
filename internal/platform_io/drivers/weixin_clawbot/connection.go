package weixin_clawbot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/vmihailenco/msgpack/v5"
	"go.uber.org/zap"

	"MaiGo/internal/platform_io"
)

// AccountConnection 单账号轮询与发送
type AccountConnection struct {
	mu        sync.Mutex
	lifecycle sync.Mutex
	sendMu    sync.Mutex
	owner     *Orchestrator
	slot      int
	Store     *Store
	gateway   string
	client    *Client
	cancel    context.CancelFunc
	ready     bool
	expired   bool
	busy      bool
	status    string
	qr        string
	done      chan struct{}
}

func NewAccountConnection(owner *Orchestrator, slot int, store *Store) *AccountConnection {
	return &AccountConnection{
		owner:   owner,
		slot:    slot,
		Store:   store,
		gateway: GatewayName(slot),
		status:  "等待扫码",
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
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if !c.owner.settings.Plugin.Enabled {
		c.setStatus("未启用：请先打开插件配置中的启用开关")
		return
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	if err := c.stop(stopCtx); err != nil {
		return
	}
	if !login && c.Store.State().Token == "" {
		c.setStatus("等待扫码或重新登录")
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.cancel, c.done = cancel, done
	c.owner.markDialed()
	go func() {
		defer close(done)
		c.run(runCtx, login)
	}()
}

func (c *AccountConnection) Stop(ctx context.Context) error {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	return c.stop(ctx)
}

func (c *AccountConnection) stop(ctx context.Context) error {
	if c.done == nil {
		return nil
	}
	c.cancel()
	select {
	case <-c.done:
		c.cancel, c.done = nil, nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *AccountConnection) setStatus(status string) {
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
}

func (c *AccountConnection) run(ctx context.Context, login bool) {
	state := c.Store.State()
	client, err := NewClient(state.BaseURL, state.Token, c.owner.settings.Weixin.MaxImageMB*1024*1024)
	if err != nil {
		c.setStatus(err.Error())
		return
	}
	c.mu.Lock()
	c.client, c.expired = client, false
	c.mu.Unlock()
	defer func() {
		c.sendMu.Lock()
		defer c.sendMu.Unlock()
		_ = c.markReady(false)
		client.Close()
		c.mu.Lock()
		c.client, c.qr = nil, ""
		c.mu.Unlock()
	}()
	if login {
		ok, err := c.login(ctx, client)
		if err != nil && ctx.Err() == nil {
			c.setStatus(err.Error())
		}
		if err != nil || !ok {
			return
		}
	}
	c.setStatus("正在连接微信")
	for ctx.Err() == nil {
		batch, err := client.Updates(ctx, c.Store.State().Cursor)
		c.mu.Lock()
		expired := c.expired
		c.mu.Unlock()
		if expired {
			err = &APIError{Code: -14, Endpoint: "session"}
		}
		if err == nil {
			err = c.markReady(true)
		}
		if err == nil {
			c.setStatus("已连接：微信私聊")
			err = c.processBatch(ctx, client, batch)
		}
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		_ = c.markReady(false)
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Code == -14 || apiErr.Code == 401) {
			c.sendMu.Lock()
			persistErr := c.Store.invalidate()
			c.sendMu.Unlock()
			if persistErr != nil {
				zap.L().Error("微信凭证失效状态保存失败", zap.Error(persistErr))
			}
			c.setStatus("微信登录已失效，请重新扫码")
			return
		}
		c.setStatus("连接或消息处理失败，等待重试；详见日志")
		zap.L().Warn("微信接收失败", zap.Int("slot", c.slot), zap.String("error_type", fmt.Sprintf("%T", err)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(c.owner.settings.Weixin.RetrySeconds) * time.Second):
		}
	}
}

func (c *AccountConnection) login(ctx context.Context, client *Client) (bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		result, err := client.Call(ctx, "get_bot_qrcode", nil, map[string]string{"bot_type": "3"}, true)
		if err != nil {
			return false, fmt.Errorf("连接启动失败，请重新扫码")
		}
		code, content := asString(result["qrcode"]), asString(result["qrcode_img_content"])
		if code == "" || content == "" {
			return false, fmt.Errorf("微信未返回登录二维码")
		}
		image, err := qrcode.Encode(content, qrcode.Medium, 256)
		if err != nil {
			return false, err
		}
		c.mu.Lock()
		c.qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
		c.status = "等待微信扫码确认"
		c.mu.Unlock()
		for ctx.Err() == nil {
			state, err := client.Call(ctx, "get_qrcode_status", nil, map[string]string{"qrcode": code}, true)
			if err != nil {
				var timeout net.Error
				if errors.As(err, &timeout) && timeout.Timeout() {
					continue
				}
				return false, fmt.Errorf("连接启动失败，请重新扫码")
			}
			status := asString(state["status"])
			switch status {
			case "confirmed":
				return c.bindAccount(client, state)
			case "cancel", "canceled", "denied":
				c.setStatus("微信登录已取消，请重新扫码")
				return false, nil
			case "expired":
				c.mu.Lock()
				c.qr = ""
				c.mu.Unlock()
			}
			if status == "expired" {
				break
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
	c.setStatus("二维码已过期，请点击刷新二维码")
	return false, nil
}

func (c *AccountConnection) bindAccount(client *Client, fields map[string]any) (bool, error) {
	token, account := asString(fields["bot_token"]), asString(fields["ilink_bot_id"])
	if token == "" || account == "" {
		return false, fmt.Errorf("微信登录成功但缺少账号凭证")
	}
	base, err := trustedURL(firstNonEmpty(asString(fields["baseurl"]), client.baseURL))
	if err != nil {
		return false, err
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if previous := c.Store.State().AccountID; previous != "" && previous != account {
		return false, fmt.Errorf("扫码账号与原绑定不同，请添加新账号")
	}
	for _, slot := range c.owner.slots {
		if slot != c && slot.Store.State().AccountID == account {
			return false, fmt.Errorf("该微信账号已绑定，请在原账号卡片重新扫码")
		}
	}
	if err := c.Store.updateAndSave(func(state *AccountState) {
		state.Token, state.AccountID, state.BaseURL = token, account, base
		state.UserID = messageIdentity(fields["ilink_user_id"])
	}); err != nil {
		return false, err
	}
	client.SetToken(token)
	client.baseURL = base
	c.mu.Lock()
	c.qr = ""
	c.mu.Unlock()
	return true, nil
}

func (c *AccountConnection) markReady(ready bool) error {
	state := c.Store.State()
	c.mu.Lock()
	if !ready {
		c.ready = false
	}
	c.mu.Unlock()
	if _, adapter, ok := c.owner.Slot(c.slot); ok && adapter != nil {
		adapter.refreshDescriptor(state.AccountID)
	}
	if c.owner != nil && c.owner.hostHooks != nil && c.owner.hostHooks.ReportGatewayState != nil {
		if err := c.owner.hostHooks.ReportGatewayState(context.Background(), c.gateway, ready, state.AccountID, state.Remark); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.ready = ready && !c.expired
	c.mu.Unlock()
	return nil
}

func (c *AccountConnection) processBatch(ctx context.Context, client *Client, batch map[string]any) error {
	msgs, ok := batch["msgs"].([]any)
	if batch["msgs"] != nil && !ok {
		return fmt.Errorf("微信消息列表格式无效")
	}
	for _, raw := range msgs {
		msg, ok := asMap(raw)
		if !ok {
			continue
		}
		from := messageIdentity(msg["from_user_id"])
		if from == "" {
			continue
		}
		if value, exists := msg["message_type"]; exists {
			if number, _ := value.(int64); number != 1 {
				continue
			}
		}
		allowed := c.owner.settings.Weixin.AllowedUsers
		if len(allowed) > 0 && !slices.Contains(allowed, from) {
			continue
		}
		state := c.Store.State()
		key := MessageID(state.AccountID, msg)
		if token := asString(msg["context_token"]); token != "" {
			if err := c.Store.updateAndSave(func(st *AccountState) { st.Contexts[from] = token }); err != nil {
				return err
			}
		}
		if slices.Contains(state.Seen, key) {
			continue
		}
		segments, err := decodeInboundItems(ctx, client, msg)
		if err != nil {
			return err
		}
		if len(segments) > 0 {
			var message map[string]any
			_, stampErr := messageStamp(msg)
			if stampErr == nil {
				message = Inbound(state.AccountID, msg, segments, c.nickname(from))
			}
			raw, err := msgpack.Marshal(message)
			if err != nil {
				return err
			}
			if stampErr != nil || len(raw) > 15<<20 {
				fallback := map[string]any{"from_user_id": from}
				body := []map[string]any{{"type": "text", "data": "[消息过大或格式无效，适配器未读取内容，请拆分后重新发送]"}}
				message = Inbound(state.AccountID, fallback, body, c.nickname(from))
				message["message_id"] = key
			}
			var sink InboundSink
			if c.owner.hostHooks != nil {
				sink = c.owner.hostHooks.Inbound
			}
			if err := deliverInbound(ctx, sink, c.gateway, state.AccountID, message); err != nil {
				return err
			}
		}
		if err := c.Store.updateAndSave(func(st *AccountState) {
			st.Seen = append(st.Seen, key)
			if len(st.Seen) > 2000 {
				st.Seen = st.Seen[len(st.Seen)-2000:]
			}
		}); err != nil {
			return err
		}
	}
	if cursor, exists := batch["get_updates_buf"]; exists {
		text, ok := cursor.(string)
		if !ok {
			return fmt.Errorf("微信更新游标格式无效")
		}
		return c.Store.updateAndSave(func(st *AccountState) { st.Cursor = text })
	}
	return nil
}

func decodeInboundItems(ctx context.Context, client *Client, msg map[string]any) ([]map[string]any, error) {
	items, _ := msg["item_list"].([]any)
	segments := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := asMap(raw)
		if !ok {
			return nil, fmt.Errorf("微信消息段格式无效")
		}
		kind, _ := item["type"].(int64)
		var text string
		switch kind {
		case 1:
			nested, _ := asMap(item["text_item"])
			text = asString(nested["text"])
		case 2:
			nested, _ := asMap(item["image_item"])
			data, err := client.downloadImage(ctx, nested)
			if err == nil {
				segments = append(segments, map[string]any{"type": "image", "data": "[图片]", "binary_data_base64": base64.StdEncoding.EncodeToString(data)})
				continue
			}
			var apiErr *APIError
			var networkErr net.Error
			if errors.As(err, &apiErr) || errors.As(err, &networkErr) || ctx.Err() != nil {
				return nil, err
			}
			text = "[图片无法读取或超过适配器大小限制]"
		case 3:
			nested, _ := asMap(item["voice_item"])
			text = firstNonEmpty(asString(nested["text"]), "[语音未提供转写，暂无法识别]")
		case 4:
			text = "[适配器暂不支持读取此文件]"
		case 5:
			text = "[适配器暂不支持读取此视频]"
		default:
			text = "[适配器暂不支持读取此消息类型]"
		}
		segments = append(segments, map[string]any{"type": "text", "data": text})
	}
	return segments, nil
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
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	fail := func(err error) (platformio.DeliveryReceipt, error) {
		receipt.Status, receipt.Error = platformio.DeliveryStatusFailed, err.Error()
		return receipt, err
	}
	c.mu.Lock()
	client, ready := c.client, c.ready
	c.mu.Unlock()
	if !ready || client == nil {
		return fail(fmt.Errorf("微信网关尚未就绪"))
	}
	state := c.Store.State()
	if routeKey.AccountID != "" && routeKey.AccountID != state.AccountID {
		return fail(fmt.Errorf("出站消息不属于当前微信账号"))
	}
	target, parts, err := encodeOutbound(ctx, outbound, client.maxBytes)
	if err != nil {
		return fail(err)
	}
	if allowed := c.owner.settings.Weixin.AllowedUsers; len(allowed) > 0 && !slices.Contains(allowed, target) {
		return fail(fmt.Errorf("目标用户不在允许列表"))
	}
	contextToken := state.Contexts[target]
	if contextToken == "" {
		return fail(fmt.Errorf("没有该用户的会话凭证，请先从微信发送消息"))
	}
	for index, part := range parts {
		item := map[string]any{"type": 1, "text_item": map[string]any{"text": part.Text}}
		if part.Kind == "image" {
			item, err = client.uploadImage(ctx, target, part.Raw)
		}
		var id [16]byte
		rand.Read(id[:])
		clientID := hex.EncodeToString(id[:])
		if err == nil {
			err = client.Send(ctx, target, contextToken, []map[string]any{item}, clientID)
		}
		if err != nil {
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				if apiErr.Code == -14 || apiErr.Code == 401 {
					c.mu.Lock()
					c.expired = true
					c.mu.Unlock()
					_ = c.markReady(false)
					c.setStatus("微信登录已失效，请重新扫码")
				}
				return fail(fmt.Errorf("微信发送失败（错误码 %d），已发送 %d 段；未自动重试", apiErr.Code, index))
			}
			return fail(fmt.Errorf("微信发送结果不确定，已确认 %d 段；为避免重复不自动重试", index))
		}
		receipt.ExternalMessageID = clientID
	}
	receipt.Status = platformio.DeliveryStatusSent
	return receipt, nil
}
