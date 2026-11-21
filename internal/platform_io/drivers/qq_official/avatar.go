package qq_official

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

type botAvatarEntry struct {
	account string
	url     string
	expires time.Time
}

func (a *Adapter) avatar(ctx context.Context, _, _ string, args map[string]any) (any, error) {
	if args["platform"] != PlatformQQ {
		return map[string]any{"status": "unsupported"}, nil
	}
	kind := "user"
	if value, exists := args["target_type"]; exists {
		kind, _ = value.(string)
	}
	if kind != "user" && kind != "group" {
		return nil, fmt.Errorf("target_type 必须为 user 或 group")
	}
	target, _ := args["target_id"].(string)
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("target_id 必须非空")
	}
	a.mu.Lock()
	client := a.client
	mapping := a.mapping.User
	if kind == "group" {
		mapping = a.mapping.Group
	}
	target = firstNonEmpty(strings.TrimSpace(mapping[target]), target)
	account, unified := a.runtime.AccountID(), a.settings.QQOfficial.UnifiedAccountID
	appID := strings.TrimSpace(a.settings.QQOfficial.AppID)
	a.mu.Unlock()
	if kind == "user" && (target == account || target == unified) {
		return a.fetchBotAvatar(ctx, client, account), nil
	}
	if strings.Trim(target, "0123456789") == "" {
		id := strings.TrimLeft(target, "0")
		if id == "" {
			return nil, fmt.Errorf("target_id 必须为正整数")
		}
		address := "https://q1.qlogo.cn/g?b=qq&nk=" + id + "&s=640"
		if kind == "group" {
			address = "https://p.qlogo.cn/gh/" + id + "/" + id + "/640"
		}
		return availableAvatar(address, 86400), nil
	}
	if kind == "group" || appID == "" {
		return map[string]any{"status": "unsupported"}, nil
	}
	pathPart := func(value string) string { return strings.ReplaceAll(url.QueryEscape(value), "+", "%20") }
	return availableAvatar("https://q.qlogo.cn/qqapp/"+pathPart(appID)+"/"+pathPart(target)+"/100", 86400), nil
}

func (a *Adapter) fetchBotAvatar(ctx context.Context, client *Client, account string) map[string]any {
	a.profileMu.Lock()
	defer a.profileMu.Unlock()
	if client != nil {
		cached := a.botProfile
		if cached.account != account || !time.Now().Before(cached.expires) {
			profile, err := client.apiRequest(ctx, http.MethodGet, "/users/@me", nil, 0)
			if err != nil {
				zap.L().Warn("QQ 机器人详情查询失败", zap.Error(err))
			} else if address := strings.TrimSpace(asString(profile["avatar"])); address != "" {
				a.botProfile = botAvatarEntry{account: account, url: address, expires: time.Now().Add(time.Hour)}
			}
		}
		cached = a.botProfile
		if cached.account == account && time.Now().Before(cached.expires) {
			return availableAvatar(cached.url, max(1, int(time.Until(cached.expires).Seconds())))
		}
	}
	return map[string]any{"status": "missing"}
}

func availableAvatar(address string, ttl int) map[string]any {
	return map[string]any{"status": "available", "url": address, "expires_in": ttl}
}
