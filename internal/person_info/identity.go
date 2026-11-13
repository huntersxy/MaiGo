package personinfo

// 平台身份映射：observe/resolve identity 与 bot 自身排除

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// IdentityObservation 平台身份观察，对齐 _observe_sender_identity
type IdentityObservation struct {
	Platform string
	UserID   string
	Aliases  []string
	GroupID  string
}

// PlatformIdentity 别名解析结果
type PlatformIdentity struct {
	PersonID   string         `json:"person_id"`
	PersonName string         `json:"person_name,omitempty"`
	Platform   string         `json:"platform,omitempty"`
	UserID     string         `json:"user_id,omitempty"`
	Aliases    []string       `json:"aliases,omitempty"`
	GroupID    string         `json:"group_id,omitempty"`
	Success    bool           `json:"success"`
	Error      string         `json:"error,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

// SetBotSelf 登记机器人自身账号，事实写回与画像观察时排除
func (r *Registry) SetBotSelf(platform, userID string) {
	key := platformIdentityKey(platform, userID)
	if key == "" {
		return
	}
	r.mu.Lock()
	if r.botSelf == nil {
		r.botSelf = map[string]struct{}{}
	}
	r.botSelf[key] = struct{}{}
	r.mu.Unlock()
}

// SetBotSelfResolver 注入实时机器人账号判定，非 nil 时优先于静态登记表
// 实时来源能反映禁用与删除，避免启动快照把已禁用账号继续当成机器人自身
func (r *Registry) SetBotSelfResolver(resolver func(platform, userID string) bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.botSelfResolver = resolver
	r.mu.Unlock()
}

// IsBotSelf 判断平台账号是否为机器人自身；实时判定存在时以它为准
func (r *Registry) IsBotSelf(platform, userID string) bool {
	key := platformIdentityKey(platform, userID)
	if key == "" {
		return false
	}
	r.mu.RLock()
	resolver := r.botSelfResolver
	_, cached := r.botSelf[key]
	r.mu.RUnlock()
	if resolver != nil {
		return resolver(platform, userID)
	}
	return cached
}

// ObserveIdentity 观察平台身份，对齐 memory_profile_admin.observe_identity
func (r *Registry) ObserveIdentity(ctx context.Context, obs IdentityObservation) (*PlatformIdentity, error) {
	platform := normalizePlatform(obs.Platform)
	userID := strings.TrimSpace(obs.UserID)
	if platform == "" || userID == "" {
		return nil, fmt.Errorf("personinfo: 身份观察需要 platform 与 user_id")
	}
	args := map[string]any{
		"platform": platform,
		"user_id":  userID,
		"aliases":  cleanAliasList(obs.Aliases),
	}
	if groupID := strings.TrimSpace(obs.GroupID); groupID != "" {
		args["group_id"] = groupID
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
			}
		}
		payload, err := r.RawProfileAdmin(ctx, "observe_identity", args)
		if err != nil {
			lastErr = err
			continue
		}
		identity := platformIdentityFromPayload(payload)
		if identity.Success {
			return identity, nil
		}
		lastErr = fmt.Errorf("personinfo: 身份观察失败: %s", identity.Error)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("personinfo: 身份观察失败")
	}
	return nil, lastErr
}

// ObserveIdentityAsync 异步观察身份，失败不影响消息主流程
func (r *Registry) ObserveIdentityAsync(ctx context.Context, obs IdentityObservation) {
	if r == nil {
		return
	}
	go func() {
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = r.ObserveIdentity(observeCtx, obs)
	}()
}

// ResolveIdentity 按平台账号或别名解析 person_id，对齐 memory_profile_admin.resolve_identity
func (r *Registry) ResolveIdentity(ctx context.Context, personID, platform, userID, alias string) (*PlatformIdentity, error) {
	identifier := strings.TrimSpace(personID)
	if identifier == "" {
		identifier = strings.TrimSpace(userID)
	}
	if identifier == "" {
		identifier = strings.TrimSpace(alias)
	}
	if identifier == "" {
		return nil, fmt.Errorf("personinfo: 身份解析缺少目标")
	}
	payload, err := r.RawProfileAdmin(ctx, "resolve_identity", map[string]any{"identifier": identifier})
	if err != nil {
		return nil, err
	}
	return platformIdentityFromPayload(payload), nil
}

// ResolveAliasPersonID 在 person_info 精确匹配失败后回退 Memorix 别名映射
func (r *Registry) ResolveAliasPersonID(ctx context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	identity, err := r.ResolveIdentity(ctx, "", "", "", name)
	if err != nil || identity == nil || !identity.Success {
		return ""
	}
	return strings.TrimSpace(identity.PersonID)
}

// GetPersonAliases 查询画像别名详情
func (r *Registry) GetPersonAliases(ctx context.Context, personID string) (map[string]any, error) {
	return r.GetAliases(ctx, personID)
}

func platformIdentityFromPayload(payload map[string]any) *PlatformIdentity {
	out := &PlatformIdentity{Raw: payload}
	out.Success = boolFromAny(payload["success"])
	out.PersonID = stringFromAny(payload["person_id"])
	out.PersonName = firstString(payload["person_name"], payload["name"])
	out.Platform = stringFromAny(payload["platform"])
	out.UserID = stringFromAny(payload["user_id"])
	out.GroupID = stringFromAny(payload["group_id"])
	out.Aliases = stringListFromAny(payload["aliases"])
	out.Error = stringFromAny(payload["error"])
	if !out.Success && out.Error == "" {
		out.Error = "身份解析失败"
	}
	return out
}

func platformIdentityKey(platform, userID string) string {
	platform = normalizePlatform(platform)
	userID = strings.TrimSpace(userID)
	if platform == "" || userID == "" {
		return ""
	}
	return platform + ":" + userID
}

func cleanAliasList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		key := strings.ToLower(text)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, text)
	}
	return out
}
