package bootstrap

// A_Memorix 原生内核启动计划：
// 集中处理后端解析、实例目录/配置预检与宿主启动
// main 只按计划加载实例持有者，并在显式 native 失败时保留真实错误

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/A_memorix/core/runtime/native"
	"MaiGo/internal/A_memorix/hostadapter"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/person_info"
	botservices "MaiGo/internal/services"
)

// nativeAMemorixStartupPlan 是一次原生内核启动的预检结果
// Attempt=false 时不加载实例持有者，由调用方按原有后端选择降级
// PrepareErr 只在 Attempt=true 时表示实例目录/配置文件等真实错误
type nativeAMemorixStartupPlan struct {
	Backend  string
	Explicit bool
	Decision memorix.NativeStartupDecision
	// LoadInstanceDir 是本次启动需要加载的实例目录；空表示沿用原有逻辑不加载
	LoadInstanceDir string
	PrepareErr      error
}

// resolveNativeAMemorixStartupPlan 解析后端与实例目录，并做 native 启动前的真实错误预检
// 显式 native 即使没有 MAIBOT_INSTANCE_DIR 也按 RepoRoot（当前工作目录）尝试
// auto 只读取 bot_config.toml 中 [a_memorix.plugin].enabled=true 的结果
func resolveNativeAMemorixStartupPlan() nativeAMemorixStartupPlan {
	backend, explicit := resolveMemorixBackend()
	envDir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	plan := nativeAMemorixStartupPlan{
		Backend:  backend,
		Explicit: explicit,
		Decision: memorix.ResolveNativeStartup(memorix.NativeStartupOptions{
			Backend:     backend,
			Explicit:    explicit,
			InstanceDir: envDir,
		}),
	}
	if !plan.Decision.Attempt {
		// 非 native 尝试仍保留原有实例配置加载，避免影响 auto 降级
		if envDir != "" {
			plan.LoadInstanceDir = envDir
		}
		return plan
	}
	if err := validateNativeAMemorixInstance(plan.Decision); err != nil {
		plan.PrepareErr = err
		return plan
	}
	plan.LoadInstanceDir = plan.Decision.InstanceDir
	return plan
}

// validateNativeAMemorixInstance 在创建 InstanceHolder 前检查目录与配置确实存在
// InstanceHolder 会对缺失配置自动生成默认文件，native 启动门不能把这种情况当成可用实例
func validateNativeAMemorixInstance(decision memorix.NativeStartupDecision) error {
	instanceDir := strings.TrimSpace(decision.InstanceDir)
	if instanceDir == "" {
		return fmt.Errorf("A_Memorix 原生内核实例目录为空")
	}
	info, err := os.Stat(instanceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("A_Memorix 原生内核实例目录不存在: %s", instanceDir)
		}
		return fmt.Errorf("检查 A_Memorix 原生内核实例目录 %s: %w", instanceDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("A_Memorix 原生内核实例路径不是目录: %s", instanceDir)
	}
	configPath := strings.TrimSpace(decision.ConfigPath)
	if configPath == "" {
		configPath = filepath.Join(instanceDir, "config", "bot_config.toml")
	}
	info, err = os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("A_Memorix 原生内核实例缺少配置文件: %s", configPath)
		}
		return fmt.Errorf("检查 A_Memorix 原生内核配置文件 %s: %w", configPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("A_Memorix 原生内核配置路径不是文件: %s", configPath)
	}
	return nil
}

// nativeAMemorixStart 是一次 native 启动尝试的结果
// Host 非空表示已进入 ready 并注册到全局 Registry
type nativeAMemorixStart struct {
	Backend  string
	Explicit bool
	Holder   *maibotcfg.InstanceHolder
	Host     *memorix.AMemorixHostService
	Err      error
	// Factory 在未尝试启动时仍可装配休眠宿主，供配置热重载拉起内核
	Factory func() memorix.KernelFactory
}

// startNativeAMemorixBackend 按启动计划尝试原生内核
// 未尝试时返回空 Host 与空 Err，调用方继续原有 fallback
// 尝试失败时 Host 为空，Err 保留真实原因；显式 native 由调用方保持未启用，不静默降级
func startNativeAMemorixBackend(plan nativeAMemorixStartupPlan, holder *maibotcfg.InstanceHolder, db *gorm.DB, modelStore modelprofiles.Source, botAccounts ...*botservices.BotAccountService) nativeAMemorixStart {
	start := nativeAMemorixStart{Backend: plan.Backend, Explicit: plan.Explicit, Holder: holder}
	var botAccountService *botservices.BotAccountService
	if len(botAccounts) > 0 {
		botAccountService = botAccounts[0]
	}
	nativeOptions := func() native.Options {
		deps := hostadapter.NativeRuntimeDeps{
			DB: db, ModelStore: modelStore,
			BotSelfResolver: hostadapter.NewBotSelfResolver(botAccountService),
		}
		if holder != nil {
			if cfg := holder.Config(); cfg != nil {
				modelConfig := cfg.Model
				deps.ModelConfig = &modelConfig
				deps.BotName = cfg.Bot.Bot.Nickname
				deps.Personality = cfg.Bot.Personality.Personality
			}
		}
		return hostadapter.BuildNativeOptions(deps)
	}
	start.Factory = func() memorix.KernelFactory {
		return newNativeAMemorixKernelFactory(nativeOptions)
	}
	if !plan.Decision.Attempt {
		return start
	}
	if plan.PrepareErr != nil {
		start.Err = plan.PrepareErr
		markNativeAMemorixStartupDegraded(plan, start.Err)
		return start
	}
	if holder == nil {
		start.Err = fmt.Errorf("A_Memorix 原生内核实例配置未加载: %s", plan.Decision.InstanceDir)
		markNativeAMemorixStartupDegraded(plan, start.Err)
		return start
	}
	service, err := startNativeAMemorixHost(holder, db, start.Factory)
	if err != nil {
		start.Err = fmt.Errorf("启动 A_Memorix 原生内核失败: %w", err)
		markNativeAMemorixStartupDegraded(plan, start.Err)
		return start
	}
	start.Host = service
	return start
}

// configurePersoninfoBotSelfAccounts 把 Bot 账号服务接到人物画像与原生内核的共同来源
// 启动时先写入一份静态快照；实时解析器存在时禁用/删除账号立即生效
func configurePersoninfoBotSelfAccounts(ctx context.Context, service *botservices.BotAccountService) {
	if service == nil {
		return
	}
	registry := personinfo.DefaultRegistry()
	registry.SetBotSelfResolver(hostadapter.NewBotSelfResolver(service))
	if ctx == nil {
		ctx = context.Background()
	}
	loadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := syncPersoninfoBotSelves(loadCtx, registry, service); err != nil {
		zap.L().Warn("加载 Bot 自身账号失败，人物过滤暂按实时解析结果", zap.Error(err))
	}
}

// syncPersoninfoBotSelves 把 Bot 账号服务的完整账号快照写入人物画像注册表
func syncPersoninfoBotSelves(ctx context.Context, registry *personinfo.Registry, service *botservices.BotAccountService) error {
	if registry == nil || service == nil {
		return nil
	}
	pairs, err := service.GetAllBotAccountPairs(ctx)
	if err != nil {
		return fmt.Errorf("读取 Bot 平台账号失败: %w", err)
	}
	for _, pair := range pairs {
		registry.SetBotSelf(pair[0], pair[1])
	}
	return nil
}

// newNativeAMemorixKernelFactory 是内核工厂注入点；测试可替换为失败工厂验证启动失败路径
var newNativeAMemorixKernelFactory = func(options func() native.Options) memorix.KernelFactory {
	return hostadapter.NewNativeKernelFactoryWithProvider(options)
}

// markNativeAMemorixStartupDegraded 记录 degraded 状态，保留后端、实例与真实错误供启动日志/排障
func markNativeAMemorixStartupDegraded(plan nativeAMemorixStartupPlan, err error) {
	zap.L().Error("A_Memorix 原生内核不可用，状态 degraded",
		zap.String("backend", plan.Backend),
		zap.Bool("explicit", plan.Explicit),
		zap.String("status", "degraded"),
		zap.String("instance_dir", plan.Decision.InstanceDir),
		zap.String("config_path", plan.Decision.ConfigPath),
		zap.Error(err))
}
