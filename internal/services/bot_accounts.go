package services

import (
	"context"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	BotAccountSourceReady   = "ready"
	BotAccountSourceInbound = "inbound"
	WebUIBotUserID          = "self"
)

var localPlatformBotIDs = map[string]string{
	"bot_console": "__maibot_console_bot__",
	"maisaka_cli": "__maisaka_cli_bot__",
}

// BotPlatformAccount 对齐 MaiBot bot_platform_accounts 表
type BotPlatformAccount struct {
	ID              uint64     `gorm:"primaryKey" json:"id"`
	Platform        string     `gorm:"size:100;index;not null;uniqueIndex:uq_bot_platform_accounts_platform_account" json:"platform"`
	AccountID       string     `gorm:"size:255;index;not null;uniqueIndex:uq_bot_platform_accounts_platform_account" json:"account_id"`
	Disabled        bool       `gorm:"index;not null" json:"disabled"`
	FirstSeenAt     time.Time  `gorm:"not null" json:"first_seen_at"`
	LastSeenAt      time.Time  `gorm:"index;not null" json:"last_seen_at"`
	DisabledAt      *time.Time `json:"disabled_at,omitempty"`
	LastSource      string     `gorm:"size:32;not null" json:"last_source,omitempty"`
	LastAdapterID   *string    `gorm:"size:255" json:"last_adapter_id,omitempty"`
	LastPluginID    *string    `gorm:"size:255" json:"last_plugin_id,omitempty"`
	LastGatewayName *string    `gorm:"size:255" json:"last_gateway_name,omitempty"`
}

func (BotPlatformAccount) TableName() string { return "bot_platform_accounts" }

// AdapterAccountIdentity 适配器上报账号时携带的来源信息
type AdapterAccountIdentity struct {
	Platform    string
	AccountID   string
	AdapterID   string
	PluginID    string
	GatewayName string
}

// BotAccountRecordResult 记录结果，与 Python RecordResult 语义一致
type BotAccountRecordResult struct {
	Persisted bool                `json:"persisted"`
	Enabled   bool                `json:"enabled"`
	Account   *BotPlatformAccount `json:"account,omitempty"`
}

// BotAccountConfig 是配置兜底所需的最小配置面
type BotAccountConfig struct {
	Platform  string
	QQAccount string
	Platforms []string
}

// BotAccountConfigSource 返回当前配置快照
type BotAccountConfigSource func() BotAccountConfig

// BotAccountRepository 抽象账号持久层，便于测试与后续替换
type BotAccountRepository interface {
	Ensure(ctx context.Context) error
	List(ctx context.Context) ([]BotPlatformAccount, error)
	Find(ctx context.Context, platform, accountID string) (*BotPlatformAccount, error)
	FindByID(ctx context.Context, id uint64) (*BotPlatformAccount, error)
	Save(ctx context.Context, account *BotPlatformAccount) error
	Delete(ctx context.Context, id uint64) error
	RemainingIDs(ctx context.Context, platform string) ([]uint64, error)
}

// BotAccountService 对齐 MaiBot BotAccountService 的身份缓存、配置兜底与禁用状态
type BotAccountService struct {
	mu sync.RWMutex

	repo   BotAccountRepository
	config BotAccountConfigSource
	online func(platform string) []string

	initialized bool
	enabled     map[string]map[string]struct{}
	disabled    map[string]map[string]struct{}
	observed    map[string]struct{}
	volatile    map[string]map[string]struct{}
	// warnedUnconfigured 记录已提示过的平台，对齐 Python _warned_unconfigured_platforms
	warnedUnconfigured map[string]struct{}
}

func NewBotAccountService(repo BotAccountRepository, config BotAccountConfigSource) *BotAccountService {
	return &BotAccountService{
		repo: repo, config: config,
		enabled: map[string]map[string]struct{}{}, disabled: map[string]map[string]struct{}{},
		observed: map[string]struct{}{}, volatile: map[string]map[string]struct{}{},
		warnedUnconfigured: map[string]struct{}{},
	}
}

func (s *BotAccountService) SetOnlineProvider(provider func(platform string) []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.online = provider
	s.mu.Unlock()
}

func normalizePlatform(platform string) string   { return strings.ToLower(strings.TrimSpace(platform)) }
func normalizeAccountID(accountID string) string { return strings.TrimSpace(accountID) }

func (s *BotAccountService) ensureInitialized(ctx context.Context) error {
	if s == nil {
		return errors.New("Bot 平台账号服务未初始化")
	}
	s.mu.RLock()
	ready := s.initialized
	s.mu.RUnlock()
	if ready {
		return nil
	}
	if s.repo == nil {
		return errors.New("Bot 平台账号仓储未接线")
	}
	if err := s.repo.Ensure(ctx); err != nil {
		return fmt.Errorf("初始化 Bot 平台账号表失败: %w", err)
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initialized {
		return nil
	}
	s.enabled = map[string]map[string]struct{}{}
	s.disabled = map[string]map[string]struct{}{}
	s.observed = map[string]struct{}{}
	for _, row := range rows {
		platform := normalizePlatform(row.Platform)
		accountID := normalizeAccountID(row.AccountID)
		if platform == "" || accountID == "" {
			continue
		}
		s.observed[platform] = struct{}{}
		target := s.enabled
		if row.Disabled {
			target = s.disabled
		}
		if target[platform] == nil {
			target[platform] = map[string]struct{}{}
		}
		target[platform][accountID] = struct{}{}
	}
	s.initialized = true
	return nil
}

func configuredAccounts(config BotAccountConfig) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	add := func(platform, accountID string) {
		platform = normalizePlatform(platform)
		accountID = normalizeAccountID(accountID)
		if platform == "" || accountID == "" {
			return
		}
		if out[platform] == nil {
			out[platform] = map[string]struct{}{}
		}
		out[platform][accountID] = struct{}{}
	}
	qqAccount := normalizeAccountID(config.QQAccount)
	if qqAccount != "" && qqAccount != "0" {
		primary := normalizePlatform(config.Platform)
		if primary == "" {
			primary = "qq"
		}
		add(primary, qqAccount)
	}
	for _, entry := range config.Platforms {
		platform, accountID, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		add(platform, accountID)
	}
	return out
}

func (s *BotAccountService) configSnapshot() BotAccountConfig {
	if s == nil || s.config == nil {
		return BotAccountConfig{}
	}
	return s.config()
}

func (s *BotAccountService) configSnapshotAccounts() map[string]map[string]struct{} {
	return configuredAccounts(s.configSnapshot())
}

// OnlineBotAccounts 对齐 get_online_bot_accounts，只反映当前在线网关账号，不读取历史身份
func (s *BotAccountService) OnlineBotAccounts(platform string) []string {
	normalized := normalizePlatform(platform)
	if normalized == "" {
		return nil
	}
	if normalized == "internal" || normalized == "webui" {
		return []string{WebUIBotUserID}
	}
	if botID, ok := localPlatformBotIDs[normalized]; ok {
		return []string{botID}
	}
	if s == nil {
		return nil
	}
	s.mu.RLock()
	provider := s.online
	s.mu.RUnlock()
	if provider == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, accountID := range provider(normalized) {
		normalizedAccount := normalizeAccountID(accountID)
		if normalizedAccount != "" {
			seen[normalizedAccount] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func (s *BotAccountService) GetBotAccounts(ctx context.Context, platform string) ([]string, error) {
	normalized := normalizePlatform(platform)
	if normalized == "" {
		return nil, nil
	}
	if normalized == "internal" || normalized == "webui" {
		return []string{WebUIBotUserID}, nil
	}
	if botID, ok := localPlatformBotIDs[normalized]; ok {
		return []string{botID}, nil
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	accounts := map[string]struct{}{}
	for accountID := range s.enabled[normalized] {
		accounts[accountID] = struct{}{}
	}
	for accountID := range s.volatile[normalized] {
		accounts[accountID] = struct{}{}
	}
	_, observed := s.observed[normalized]
	s.mu.RUnlock()
	if !observed {
		for accountID := range s.configSnapshotAccounts()[normalized] {
			accounts[accountID] = struct{}{}
		}
	}
	return sortedKeys(accounts), nil
}

func (s *BotAccountService) GetAllBotAccountPairs(ctx context.Context) ([][2]string, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	platforms := map[string]struct{}{}
	for platform := range s.observed {
		platforms[platform] = struct{}{}
	}
	for platform := range s.enabled {
		platforms[platform] = struct{}{}
	}
	for platform := range s.volatile {
		platforms[platform] = struct{}{}
	}
	s.mu.RUnlock()
	for platform := range s.configSnapshotAccounts() {
		platforms[platform] = struct{}{}
	}
	for platform := range localPlatformBotIDs {
		platforms[platform] = struct{}{}
	}
	platforms["internal"] = struct{}{}
	platforms["webui"] = struct{}{}
	pairs := make([][2]string, 0, len(platforms))
	for platform := range platforms {
		accounts, err := s.GetBotAccounts(ctx, platform)
		if err != nil {
			return nil, err
		}
		for _, accountID := range accounts {
			pairs = append(pairs, [2]string{platform, accountID})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs, nil
}

func (s *BotAccountService) IsBotSelf(ctx context.Context, platform, userID string) (bool, error) {
	accounts, err := s.GetBotAccounts(ctx, platform)
	if err != nil {
		return false, err
	}
	target := normalizeAccountID(userID)
	for _, accountID := range accounts {
		if accountID == target {
			return true, nil
		}
	}
	if len(accounts) == 0 {
		s.warnUnconfiguredPlatform(platform, userID)
	}
	return false, nil
}

// warnUnconfiguredPlatform 每平台只提示一次，对齐 Python _warned_unconfigured_platforms
func (s *BotAccountService) warnUnconfiguredPlatform(platform, userID string) {
	if s == nil {
		return
	}
	normalized := normalizePlatform(platform)
	if normalized == "" {
		return
	}
	s.mu.Lock()
	if s.warnedUnconfigured == nil {
		s.warnedUnconfigured = map[string]struct{}{}
	}
	if _, warned := s.warnedUnconfigured[normalized]; warned {
		s.mu.Unlock()
		return
	}
	s.warnedUnconfigured[normalized] = struct{}{}
	s.mu.Unlock()
	zap.L().Warn("平台 " + normalized + " 未配置机器人账号，无法判断用户 " + strings.TrimSpace(userID) + " 是否为机器人自己")
}

func stringPtrOrNil(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func (s *BotAccountService) RecordAdapterAccount(ctx context.Context, identity AdapterAccountIdentity, source string) (BotAccountRecordResult, error) {
	platform := normalizePlatform(identity.Platform)
	accountID := normalizeAccountID(identity.AccountID)
	if platform == "" || accountID == "" {
		return BotAccountRecordResult{}, errors.New("适配器上报的 platform 和 account_id 不能为空")
	}
	source = strings.TrimSpace(source)
	if source != BotAccountSourceReady && source != BotAccountSourceInbound {
		return BotAccountRecordResult{}, fmt.Errorf("不支持的 Bot 账号来源: %s", source)
	}
	if err := s.ensureInitialized(ctx); err != nil {
		return BotAccountRecordResult{}, err
	}
	now := time.Now()
	repository := s.repo
	var account *BotPlatformAccount
	var dbErr error
	if repository == nil {
		dbErr = errors.New("Bot 平台账号仓储未接线")
	} else {
		account, dbErr = repository.Find(ctx, platform, accountID)
		if dbErr == nil {
			if account == nil {
				account = &BotPlatformAccount{Platform: platform, AccountID: accountID, FirstSeenAt: now, LastSeenAt: now}
			}
			account.LastSeenAt = now
			account.LastSource = source
			account.LastAdapterID = stringPtrOrNil(identity.AdapterID)
			account.LastPluginID = stringPtrOrNil(identity.PluginID)
			account.LastGatewayName = stringPtrOrNil(identity.GatewayName)
			dbErr = repository.Save(ctx, account)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed[platform] = struct{}{}
	if dbErr != nil {
		disabled := false
		if s.disabled[platform] != nil {
			_, disabled = s.disabled[platform][accountID]
		}
		if !disabled {
			if s.volatile[platform] == nil {
				s.volatile[platform] = map[string]struct{}{}
			}
			s.volatile[platform][accountID] = struct{}{}
		}
		zap.L().Error("Bot 平台账号写入数据库失败，当前进程继续使用临时身份",
			zap.String("platform", platform), zap.String("account_id", accountID), zap.Error(dbErr))
		return BotAccountRecordResult{Persisted: false, Enabled: !disabled}, nil
	}
	deleteSet(s.volatile[platform], accountID)
	if account != nil && account.Disabled {
		if s.disabled[platform] == nil {
			s.disabled[platform] = map[string]struct{}{}
		}
		s.disabled[platform][accountID] = struct{}{}
		deleteSet(s.enabled[platform], accountID)
		zap.L().Warn("已禁用的 Bot 平台账号仍被适配器上报",
			zap.String("platform", platform), zap.String("account_id", accountID))
		return BotAccountRecordResult{Persisted: true, Enabled: false, Account: account}, nil
	}
	if s.enabled[platform] == nil {
		s.enabled[platform] = map[string]struct{}{}
	}
	s.enabled[platform][accountID] = struct{}{}
	deleteSet(s.disabled[platform], accountID)
	return BotAccountRecordResult{Persisted: true, Enabled: true, Account: account}, nil
}

func deleteSet(set map[string]struct{}, key string) {
	if set != nil {
		delete(set, key)
	}
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (s *BotAccountService) ListAccounts(ctx context.Context) ([]BotPlatformAccount, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("Bot 平台账号仓储未接线")
	}
	if err := s.repo.Ensure(ctx); err != nil {
		return nil, fmt.Errorf("初始化 Bot 平台账号表失败: %w", err)
	}
	return s.repo.List(ctx)
}

func (s *BotAccountService) refreshDisabledState(account *BotPlatformAccount) {
	if account == nil {
		return
	}
	platform := normalizePlatform(account.Platform)
	accountID := normalizeAccountID(account.AccountID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observed[platform] = struct{}{}
	deleteSet(s.enabled[platform], accountID)
	deleteSet(s.disabled[platform], accountID)
	if account.Disabled {
		// 对齐 Python _save_disabled_state：禁用时丢掉 volatile，避免门控仍把账号当在线自身
		deleteSet(s.volatile[platform], accountID)
		if s.disabled[platform] == nil {
			s.disabled[platform] = map[string]struct{}{}
		}
		s.disabled[platform][accountID] = struct{}{}
		return
	}
	if s.enabled[platform] == nil {
		s.enabled[platform] = map[string]struct{}{}
	}
	s.enabled[platform][accountID] = struct{}{}
}

func (s *BotAccountService) SetDisabledByID(ctx context.Context, id uint64, disabled bool) (*BotPlatformAccount, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if s.repo == nil {
		return nil, errors.New("Bot 平台账号仓储未接线")
	}
	account, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("Bot 平台账号不存在: id=%d", id)
	}
	now := time.Now()
	account.Disabled = disabled
	if disabled {
		account.DisabledAt = &now
	} else {
		account.DisabledAt = nil
	}
	if err := s.repo.Save(ctx, account); err != nil {
		return nil, fmt.Errorf("保存 Bot 平台账号失败: %w", err)
	}
	s.refreshDisabledState(account)
	return account, nil
}

func (s *BotAccountService) SetDisabled(ctx context.Context, platform, accountID string, disabled bool) (*BotPlatformAccount, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	if s.repo == nil {
		return nil, errors.New("Bot 平台账号仓储未接线")
	}
	normalizedPlatform := normalizePlatform(platform)
	normalizedAccount := normalizeAccountID(accountID)
	account, err := s.repo.Find(ctx, normalizedPlatform, normalizedAccount)
	if err != nil {
		return nil, fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("Bot 平台账号不存在: platform=%s account_id=%s", normalizedPlatform, normalizedAccount)
	}
	return s.SetDisabledByID(ctx, account.ID, disabled)
}

func (s *BotAccountService) DeleteByID(ctx context.Context, id uint64) error {
	if err := s.ensureInitialized(ctx); err != nil {
		return err
	}
	if s.repo == nil {
		return errors.New("Bot 平台账号仓储未接线")
	}
	account, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	if account == nil {
		return fmt.Errorf("Bot 平台账号不存在: id=%d", id)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("删除 Bot 平台账号失败: %w", err)
	}
	platform := normalizePlatform(account.Platform)
	accountID := normalizeAccountID(account.AccountID)
	remaining, err := s.repo.RemainingIDs(ctx, platform)
	if err != nil {
		return fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleteSet(s.enabled[platform], accountID)
	deleteSet(s.disabled[platform], accountID)
	deleteSet(s.volatile[platform], accountID)
	if len(remaining) == 0 && len(s.volatile[platform]) == 0 {
		delete(s.observed, platform)
		delete(s.enabled, platform)
		delete(s.disabled, platform)
		delete(s.volatile, platform)
	}
	return nil
}

func (s *BotAccountService) ResetCacheForTest() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initialized = false
	s.enabled = map[string]map[string]struct{}{}
	s.disabled = map[string]map[string]struct{}{}
	s.observed = map[string]struct{}{}
	s.volatile = map[string]map[string]struct{}{}
}

var defaultBotAccountService = NewBotAccountService(nil, nil)

func DefaultBotAccountService() *BotAccountService { return defaultBotAccountService }

func ConfigureDefaultBotAccountService(repo BotAccountRepository, config BotAccountConfigSource) {
	defaultBotAccountService = NewBotAccountService(repo, config)
}

// NormalizePlatform 对齐 services/bot_account_service.normalize_platform：
// 去空白转小写，不做任何平台别名合并。
func NormalizePlatform(platform string) string {
	return strings.ToLower(strings.TrimSpace(platform))
}

// NormalizeAccountID 对齐 services/bot_account_service.normalize_account_id
func NormalizeAccountID(accountID string) string {
	return strings.TrimSpace(accountID)
}
