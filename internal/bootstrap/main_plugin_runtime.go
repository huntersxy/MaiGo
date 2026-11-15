package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/plugin_runtime"
	botservices "MaiGo/internal/services"
)

// pluginBotAccountReporter 把 Host 的 Bot 账号上报端口接到 services.BotAccountService
type pluginBotAccountReporter struct {
	service *botservices.BotAccountService
}

func (r pluginBotAccountReporter) RecordAdapterAccount(ctx context.Context, identity pluginruntime.PluginBotAccountIdentity, source string) error {
	if r.service == nil {
		return fmt.Errorf("Bot 平台账号服务未初始化")
	}
	_, err := r.service.RecordAdapterAccount(ctx, botservices.AdapterAccountIdentity{
		Platform:    identity.Platform,
		AccountID:   identity.AccountID,
		AdapterID:   identity.AdapterID,
		PluginID:    identity.PluginID,
		GatewayName: identity.GatewayName,
	}, source)
	return err
}

// newPluginBotAccountService 构造带 Gorm 仓储的 Bot 账号服务，并配置为进程默认实例
//
// WebUI bot-accounts 与 memorix bot-self 都读写 bot_platform_accounts，服务写入后二者共享同一份数据
func newPluginBotAccountService(db *gorm.DB, holder *maibotcfg.InstanceHolder, platformMgr *platformio.Manager) *botservices.BotAccountService {
	repo := &botservices.GormBotAccountRepository{DB: db}
	config := func() botservices.BotAccountConfig {
		if holder == nil {
			return botservices.BotAccountConfig{}
		}
		cfg := holder.Config()
		if cfg == nil {
			return botservices.BotAccountConfig{}
		}
		return botservices.BotAccountConfig{
			Platform:  cfg.Bot.Bot.Platform,
			QQAccount: cfg.Bot.Bot.QqAccount,
			Platforms: append([]string(nil), cfg.Bot.Bot.Platforms...),
		}
	}
	botservices.ConfigureDefaultBotAccountService(repo, config)
	service := botservices.DefaultBotAccountService()
	service.SetOnlineProvider(func(platform string) []string {
		return onlinePluginBotAccounts(platformMgr, platform)
	})
	return service
}

// onlinePluginBotAccounts 对齐 Python get_online_bot_accounts：
// 仅统计已注册的 PLUGIN 驱动且 account_id 非空；注册即在线，不读 IsConnected。
func onlinePluginBotAccounts(mgr *platformio.Manager, platform string) []string {
	if mgr == nil {
		return nil
	}
	platform = strings.TrimSpace(platform)
	seen := make(map[string]struct{})
	accounts := make([]string, 0)
	for _, descriptor := range mgr.AdapterDescriptors() {
		if descriptor.Kind != platformio.DriverKindPlugin {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(descriptor.Platform), platform) {
			continue
		}
		accountID := strings.TrimSpace(descriptor.AccountID)
		if accountID == "" {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		accounts = append(accounts, accountID)
	}
	sort.Strings(accounts)
	return accounts
}
