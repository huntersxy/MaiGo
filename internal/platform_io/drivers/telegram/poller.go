package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"go.uber.org/zap"
)

// Poller 长轮询 getUpdates；传输失败不推进 offset
type Poller struct {
	settings  TelegramOptions
	client    *Client
	store     *Store
	runtime   *RuntimeState
	onReady   func(ctx context.Context, accountID, username string) error
	onUpdate  func(ctx context.Context, update map[string]any) error
	onUnready func(ctx context.Context) error
}

func NewPoller(
	settings TelegramOptions,
	client *Client,
	store *Store,
	runtime *RuntimeState,
	onReady func(ctx context.Context, accountID, username string) error,
	onUpdate func(ctx context.Context, update map[string]any) error,
	onUnready func(ctx context.Context) error,
) *Poller {
	return &Poller{
		settings:  settings,
		client:    client,
		store:     store,
		runtime:   runtime,
		onReady:   onReady,
		onUpdate:  onUpdate,
		onUnready: onUnready,
	}
}

func (p *Poller) Run(ctx context.Context) {
	if p == nil || p.client == nil {
		return
	}
	reconnect := time.Duration(p.settings.ReconnectDelaySec * float64(time.Second))
	if reconnect < time.Second {
		reconnect = 5 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.pollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			continue
		}
		if p.onUnready != nil {
			_ = p.onUnready(ctx)
		}
		if apiErr, ok := err.(*APIError); ok {
			if apiErr.Code < 500 && apiErr.Code != 429 {
				zap.L().Error("Telegram 接收任务停止，未确认的更新将保留", zap.Error(err))
				return
			}
		}
		if _, ok := err.(*TransportError); ok || isRetryableAPI(err) {
			zap.L().Warn("Telegram 连接暂时不可用，稍后重连", zap.Error(err))
		} else {
			zap.L().Error("Telegram 接收任务停止，未确认的更新将保留", zap.Error(err))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnect):
		}
	}
}

func isRetryableAPI(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	return apiErr.Code >= 500 || apiErr.Code == 429
}

func (p *Poller) pollOnce(ctx context.Context) error {
	meAny, err := p.client.Call(ctx, "getMe", nil, nil)
	if err != nil {
		return err
	}
	me, ok := asMap(meAny)
	if !ok {
		return fmt.Errorf("getMe 响应无效")
	}
	id, valid := asInt64(me["id"])
	accountID := asString(id)
	username := asString(me["username"])
	if !valid || id <= 0 {
		return fmt.Errorf("getMe 缺少 id")
	}
	webhookAny, err := p.client.Call(ctx, "getWebhookInfo", nil, nil)
	if err != nil {
		return err
	}
	if webhook, ok := asMap(webhookAny); ok {
		if url := asString(webhook["url"]); url != "" {
			return fmt.Errorf("该机器人已经配置 Webhook，请先通过 deleteWebhook 移除后再启用长轮询")
		}
	}
	if p.onReady != nil {
		if err := p.onReady(ctx, accountID, username); err != nil {
			return err
		}
	}
	zap.L().Info("Telegram 网关已就绪", zap.String("username", username), zap.String("account", accountID))
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		offset, err := p.store.Offset(accountID)
		if err != nil {
			return err
		}
		updatesAny, err := p.client.Call(ctx, "getUpdates", map[string]any{
			"offset":          offset,
			"timeout":         p.settings.PollTimeoutSec,
			"allowed_updates": []string{"message", "message_reaction", "message_reaction_count"},
		}, nil)
		if err != nil {
			return err
		}
		updates, ok := updatesAny.([]any)
		if !ok {
			continue
		}
		for _, raw := range updates {
			update, ok := asMap(raw)
			if !ok {
				continue
			}
			updateID, ok := asInt64(update["update_id"])
			if !ok || updateID < offset {
				continue
			}
			if err := p.handleUpdate(ctx, accountID, update); err != nil {
				return err
			}
			offset = updateID + 1
		}
	}
}

func (p *Poller) handleUpdate(ctx context.Context, accountID string, update map[string]any) error {
	err := p.onUpdate(ctx, update)
	if err != nil {
		if _, ok := err.(*TransportError); ok {
			return err
		}
		if apiErr, ok := err.(*APIError); ok && (apiErr.Code >= 500 || apiErr.Code == 401 || apiErr.Code == 409 || apiErr.Code == 429) {
			return err
		}
		if saveErr := p.store.SaveFailedUpdate(accountID, update, err.Error(), ""); saveErr != nil {
			return saveErr
		}
		zap.L().Error("Telegram 单条更新处理失败，已保存至 failed_updates，继续接收",
			zap.Any("update_id", update["update_id"]), zap.Error(err))
		return nil
	}
	updateID, _ := asInt64(update["update_id"])
	return p.store.SaveOffset(accountID, updateID+1)
}

func storePath(dataDir string) string {
	return filepath.Join(dataDir, "telegram.sqlite3")
}
