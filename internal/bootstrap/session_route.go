package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"MaiGo/internal/platform_io"
	"MaiGo/internal/services"
)

// pluginSessionRouteResolver 为新会话解析 Bot 身份与适配器作用域
type pluginSessionRouteResolver struct {
	platform *platformio.Manager
	accounts *services.BotAccountService
}

type sessionRoute struct {
	accountID string
	scope     string
}

// ResolveNewSessionRoute 使用已确认的 Bot 身份，并继承该账号唯一的适配器作用域
func (r pluginSessionRouteResolver) ResolveNewSessionRoute(ctx context.Context, platform, accountID, scope string) (string, string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	accountID = strings.TrimSpace(accountID)
	scope = strings.TrimSpace(scope)
	var adapters []platformio.Adapter
	if r.platform != nil {
		adapters = r.platform.Adapters()
	}
	routes := make([]sessionRoute, 0, len(adapters))
	pluginAccounts := make(map[string]struct{})
	for _, adapter := range adapters {
		desc := adapter.Descriptor()
		if !strings.EqualFold(strings.TrimSpace(desc.Platform), platform) || desc.Kind != platformio.DriverKindPlugin {
			continue
		}
		account := strings.TrimSpace(desc.AccountID)
		pluginAccounts[account] = struct{}{}
		routes = append(routes, sessionRoute{accountID: account, scope: strings.TrimSpace(desc.Scope)})
	}
	for _, adapter := range adapters {
		desc := adapter.Descriptor()
		if !strings.EqualFold(strings.TrimSpace(desc.Platform), platform) || desc.Kind == platformio.DriverKindPlugin {
			continue
		}
		account := strings.TrimSpace(desc.AccountID)
		if _, shadowed := pluginAccounts[account]; shadowed {
			continue
		}
		routes = append(routes, sessionRoute{accountID: account, scope: strings.TrimSpace(desc.Scope)})
	}

	if accountID == "" {
		accounts, err := r.botAccounts(ctx, platform)
		if err != nil {
			return "", "", err
		}
		if scope != "" {
			scoped := make(map[string]struct{})
			for _, route := range routes {
				if route.scope == scope && route.accountID != "" {
					scoped[route.accountID] = struct{}{}
				}
			}
			if len(scoped) > 0 {
				filtered := accounts[:0]
				for _, account := range accounts {
					if _, ok := scoped[account]; ok {
						filtered = append(filtered, account)
					}
				}
				accounts = filtered
			}
		}
		if len(accounts) == 0 {
			return "", "", fmt.Errorf("平台 %s 缺少可确认的 Bot 账号，请指定 account_id", platform)
		}
		if len(accounts) > 1 {
			return "", "", fmt.Errorf("平台 %s 存在多个 Bot 账号，请指定 account_id", platform)
		}
		accountID = accounts[0]
	}
	if scope == "" {
		scopes := make(map[string]struct{})
		for _, route := range routes {
			if route.accountID == accountID {
				scopes[route.scope] = struct{}{}
			}
		}
		if len(scopes) > 1 {
			return "", "", fmt.Errorf("平台 %s 的账号 %s 存在多个作用域，请指定 scope", platform, accountID)
		}
		for value := range scopes {
			scope = value
		}
	}
	return accountID, scope, nil
}

func (r pluginSessionRouteResolver) botAccounts(ctx context.Context, platform string) ([]string, error) {
	if r.accounts == nil {
		return nil, fmt.Errorf("平台 %s 缺少可确认的 Bot 账号，请指定 account_id", platform)
	}
	accounts, err := r.accounts.GetBotAccounts(ctx, platform)
	if err != nil {
		return nil, err
	}
	unique := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if trimmed := strings.TrimSpace(account); trimmed != "" {
			unique[trimmed] = struct{}{}
		}
	}
	out := make([]string, 0, len(unique))
	for account := range unique {
		out = append(out, account)
	}
	sort.Strings(out)
	return out, nil
}
