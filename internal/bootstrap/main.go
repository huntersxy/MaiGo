package bootstrap

import (
	webuichat "MaiGo/internal/webui/routers/chat"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"MaiGo/internal/A_memorix"
	"MaiGo/internal/agent"
	"MaiGo/internal/chat/image_system"
	"MaiGo/internal/chat/message_receive"
	"MaiGo/internal/chat/session"
	"MaiGo/internal/chat/wiring"
	"MaiGo/internal/cli"
	"MaiGo/internal/common/database"
	"MaiGo/internal/common/i18n"
	"MaiGo/internal/common/logger"
	"MaiGo/internal/common/media"
	"MaiGo/internal/common/message_server"
	maimserver "MaiGo/internal/common/message_server/server"
	"MaiGo/internal/config"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/config/model_profiles"
	"MaiGo/internal/core/internal_chat"
	"MaiGo/internal/core/localoperator"
	"MaiGo/internal/desktop_pet"
	"MaiGo/internal/emoji_system"
	"MaiGo/internal/files"
	"MaiGo/internal/host"
	"MaiGo/internal/learners"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/maisaka"
	"MaiGo/internal/maisaka/display"
	"MaiGo/internal/maisaka/monitor"
	maisaruntime "MaiGo/internal/maisaka/runtime"
	"MaiGo/internal/mcp_module"
	"MaiGo/internal/person_info"
	"MaiGo/internal/platform_io"
	"MaiGo/internal/platform_io/drivers/maim_bridge"
	"MaiGo/internal/platform_io/drivers/onebot"
	"MaiGo/internal/plugin_market"
	"MaiGo/internal/plugin_runtime"
	"MaiGo/internal/plugin_runtime/hostports"
	"MaiGo/internal/prompt"
	"MaiGo/internal/services/html_render"
	memorywriteback "MaiGo/internal/services/memory_writeback"
	"MaiGo/internal/services/model_stats"
	"MaiGo/internal/services/news"
	"MaiGo/internal/services/schedules"
	goruntime "runtime"

	"MaiGo/internal/common/secrets"
	"MaiGo/internal/common/self_update"
	"MaiGo/internal/common/version"
	"MaiGo/internal/maisaka/builtin_tool"
	"MaiGo/internal/manager/asynctask"
	"MaiGo/internal/runtime"
	"MaiGo/internal/runtime/execution"
	"MaiGo/internal/runtime/execution/committer"
	"MaiGo/internal/runtime/execution/policy"
	chatscheduler "MaiGo/internal/runtime/execution/scheduler"
	botservices "MaiGo/internal/services"
	"MaiGo/internal/services/statistics"
	"MaiGo/internal/web_search"
	webapp "MaiGo/internal/webui/core"
	"MaiGo/internal/webui/routers"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newMessageService 构造消息服务；自查身份由 Bot 平台账号按 (platform, account) 判定
func newMessageService(db *gorm.DB, selfID int64, botName string) *botservices.MessageService {
	return botservices.NewMessageService(db, selfID, botName)
}

func Run() {
	os.Exit(runMain())
}

// runMain 在 Worker 初始化前安装信号，首次中断取消启动上下文，第二次中断直接退出
func runMain() (exitCode int) {
	var dispatched cli.Outcome
	switch out := cli.Dispatch(os.Args); {
	case !out.Handled:
		break
	case out.StartBot:
		dispatched = out
	default:
		return out.ExitCode
	}
	if dispatched.HostMode && !host.HasRuntimeContext() && os.Getenv(runtime.EnvRuntimeMode) == "" {
		workspace, err := host.OpenWorkspace(config.InstanceRoot())
		if err != nil {
			fmt.Fprintf(os.Stderr, "宿主启动失败：%v"+string(rune(10)), err)
			return 2
		}
		if err := host.RunHost(context.Background(), workspace, map[string]struct{}{}); err != nil {
			fmt.Fprintf(os.Stderr, "宿主关闭未完成。%v"+string(rune(10)), err)
			return 1
		}
		return 0
	}

	if runtime.IsRunner() {
		boot, err := config.LoadBootstrap()
		if err != nil {
			fmt.Printf("加载启动配置失败: %v\n", err)
			return 1
		}
		logger.Init(boot.LogLevel, boot.Debug)
		zap.L().Info(i18n.T("startup.launching_script", nil))
		return runtime.RunRunner()
	}

	// 对齐 Python src/runtime.py:132：Worker 运行期间持有实例锁，宿主据此拒绝归档、克隆与移动
	instanceLease, leaseErr := host.LockInstance(config.InstanceRoot())
	if leaseErr != nil {
		fmt.Fprintf(os.Stderr, "无法取得 Bot 实例锁，可能已有实例在运行：%v"+string(rune(10)), leaseErr)
		return 1
	}
	defer instanceLease.Release()
	// 对齐 Python src/runtime.py:107：启动即按 MAIBOT_LOCALE 设置进程级 locale
	i18n.SetLocale(i18n.EnvLocale())
	shutdownSignal := runtime.NewShutdownSignal()
	stopSignals := runtime.InstallShutdownSignalHandler(shutdownSignal)
	defer stopSignals()
	startup := newStartupLifecycle(shutdownSignal)
	defer startup.AbortIfNeeded(&exitCode)
	startup.AddCleanup("停止日志广播", func(context.Context) error {
		logger.StopWebUIBroadcasts()
		return nil
	})
	startup.AddCleanup("停止内存诊断", func(context.Context) error {
		if activeMemoryDiagnostics != nil {
			activeMemoryDiagnostics.Stop()
			activeMemoryDiagnostics = nil
		}
		return nil
	})
	startup.AddCleanup("停止调度延迟看门狗", func(context.Context) error {
		if activeEventLoopWatchdog != nil {
			activeEventLoopWatchdog.Stop()
			activeEventLoopWatchdog = nil
		}
		if activeEventLoopWatchdogCancel != nil {
			activeEventLoopWatchdogCancel()
			activeEventLoopWatchdogCancel = nil
		}
		if activeLogCleanupCancel != nil {
			activeLogCleanupCancel()
			activeLogCleanupCancel = nil
		}
		return nil
	})

	boot, err := config.LoadBootstrap()
	if err != nil {
		fmt.Printf("加载启动配置失败: %v\n", err)
		return 1
	}
	logger.Init(boot.LogLevel, boot.Debug)
	zap.L().Info(i18n.T("startup.launching_script", nil))
	if !startup.Check("启动配置") {
		return 1
	}
	return runWorker(boot, startup, shutdownSignal)
}

// activeMemoryDiagnostics 当前内存诊断实例，供关停时停止
var activeMemoryDiagnostics *logger.MemoryDiagnostics

// activeEventLoopWatchdog 当前调度延迟看门狗，供关停时停止
var (
	activeEventLoopWatchdog       *runtime.EventLoopWatchdog
	activeEventLoopWatchdogCancel context.CancelFunc
)

// activeLogCleanupCancel 日志周期清理任务的取消函数，供关停时停止
var activeLogCleanupCancel context.CancelFunc

// adapterLifecycleResult 把 pluginruntime 的结果转为 messagereceive 形状
func adapterLifecycleResult(result pluginruntime.AdapterTransitionResult) messagereceive.AdapterLifecycleResult {
	return messagereceive.AdapterLifecycleResult{
		Changed: result.ChangedPluginIDs,
		Failed:  result.FailedPlugins,
		Pending: result.PendingPluginIDs,
	}
}

// commandPermissionRules 把实例 TOML 的命令权限配置转为规则表
// 对齐 Python command_permissions 的 allow_users/allow_chats 结构
func commandPermissionRules(raw any) map[string]localoperator.CommandPermissionRule {
	rules := map[string]localoperator.CommandPermissionRule{}
	table, ok := raw.(map[string]any)
	if !ok {
		return rules
	}
	for commandID, entry := range table {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rules[commandID] = localoperator.CommandPermissionRule{
			AllowUsers: stringListFromAny(fields["allow_users"]),
			AllowChats: stringListFromAny(fields["allow_chats"]),
		}
	}
	return rules
}

// stringListFromAny 把任意列表转为字符串切片
func stringListFromAny(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

// forcePluginCompatibilityFromInstance 读实例配置的强制插件兼容开关
// 对齐 Python src/plugin_runtime/compat_policy.py:12-21 is_force_plugin_compatibility_enabled
func forcePluginCompatibilityFromInstance(holder *maibotcfg.InstanceHolder) bool {
	if holder == nil {
		return false
	}
	cfg := holder.Config()
	if cfg == nil {
		return false
	}
	return cfg.Bot.Debug.ForcePluginCompatibility
}

// applyInstanceLogConfig 按实例 LogConfig 重建日志系统
// 对齐 Python src/common/logger.py:57-73 与 :387-391：file/console 分级、5 MiB 轮转、30 份保留、30 天清理
func applyInstanceLogConfig(cfg *maibotcfg.InstanceConfig, db *gorm.DB) {
	if cfg == nil {
		return
	}
	logCfg := cfg.Bot.Log
	opts := logger.DefaultFileOptions()
	if logCfg.LogFileMaxBytes > 0 {
		opts.MaxBytes = int64(logCfg.LogFileMaxBytes)
	}
	if logCfg.MaxLogFiles > 0 {
		opts.BackupCount = logCfg.MaxLogFiles
	}
	if logCfg.LogCleanupDays > 0 {
		opts.CleanupDays = logCfg.LogCleanupDays
	}
	if dir := strings.TrimSpace(cfg.Dir); dir != "" {
		opts.Dir = filepath.Join(dir, "logs")
	}
	logger.InitWithOptions(logger.InitOptions{
		Level:        logCfg.LogLevel,
		ConsoleLevel: logCfg.ConsoleLogLevel,
		FileLevel:    logCfg.FileLogLevel,
		File:         opts,
		FileEnabled:  true,
		Style: logger.StyleOptions{
			DateStyle:     logCfg.DateStyle,
			LogLevelStyle: logCfg.LogLevelStyle,
			ColorText:     logCfg.ColorText,
		},
	})
	// 对齐 Python logger.py:467-480：第三方库日志按 suppress_libraries/library_log_levels 控制
	logger.ConfigureLibraryLogs(logger.LibraryLogOptions{
		SuppressLibraries: logCfg.SuppressLibraries,
		LibraryLogLevels:  logCfg.LibraryLogLevels,
	})
	// 对齐 Python event_loop_watchdog.py:51-72：按 log.event_loop_watchdog_* 启动调度延迟看门狗
	if logCfg.EventLoopWatchdogEnabled {
		watchdogCtx, cancelWatchdog := context.WithCancel(context.Background())
		activeEventLoopWatchdog = runtime.StartEventLoopWatchdog(watchdogCtx, runtime.EventLoopWatchdogOptions{
			Name:          "main",
			WarnThreshold: time.Duration(logCfg.EventLoopWatchdogWarnSeconds * float64(time.Second)),
		})
		activeEventLoopWatchdogCancel = cancelWatchdog
	}
	// 对齐 Python memory_diagnostics.py:220-236：按 log.memory_diagnostics_* 启动周期诊断
	if logCfg.MemoryDiagnosticsEnabled {
		diagnostics, diagErr := logger.StartMemoryDiagnostics(logger.MemoryDiagnosticsOptions{
			Dir:      filepath.Join(opts.Dir, "memory_diagnostics"),
			Interval: time.Duration(logCfg.MemoryDiagnosticsIntervalSeconds * float64(time.Second)),
			Deep:     logCfg.MemoryDiagnosticsDeepEnabled,
			// 对齐 Python memory_diagnostics.py:81-116：业务计数由调用方注入
			RuntimeCounts: func() map[string]any {
				return map[string]any{
					"goroutines": goruntime.NumGoroutine(),
					"gomaxprocs": goruntime.GOMAXPROCS(0),
				}
			},
			// 对齐 Python memory_diagnostics.py:51-79：连接池状态由调用方注入
			DatabaseCounts: func() map[string]any {
				if db == nil {
					return nil
				}
				sqlDB, err := db.DB()
				if err != nil {
					return nil
				}
				stats := sqlDB.Stats()
				return map[string]any{
					"open_connections": stats.OpenConnections,
					"in_use":           stats.InUse,
					"idle":             stats.Idle,
					"max_open":         stats.MaxOpenConnections,
					"wait_count":       stats.WaitCount,
					"wait_duration_ms": stats.WaitDuration.Milliseconds(),
				}
			},
		})
		if diagErr != nil {
			zap.L().Warn("内存诊断启动失败，业务继续运行", zap.Error(diagErr))
		} else if diagnostics != nil {
			activeMemoryDiagnostics = diagnostics
		}
	}
	// 对齐 Python logger.py:945-948：启动时清理过期日志
	if deleted, freed, err := logger.CleanupOldLogs(opts); err != nil {
		zap.L().Warn("清理旧日志文件失败", zap.Error(err))
	} else if deleted > 0 {
		zap.L().Info("清理了过期日志文件",
			zap.Int("deleted", deleted),
			zap.Int64("freed_bytes", freed))
	}
	// 对齐 Python start_log_cleanup_task：之后每 24 小时再清理一次
	startLogCleanupLoop(opts)
}

// logCleanupInterval 与 Python start_log_cleanup_task 的 sleep 周期一致
const logCleanupInterval = 24 * time.Hour

// startLogCleanupLoop 按固定周期清理过期日志；进程退出时随 ctx 结束
func startLogCleanupLoop(opts logger.FileOptions) {
	ctx, cancel := context.WithCancel(context.Background())
	activeLogCleanupCancel = cancel
	go func() {
		ticker := time.NewTicker(logCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if deleted, freed, err := logger.CleanupOldLogs(opts); err != nil {
					zap.L().Warn("周期清理旧日志文件失败", zap.Error(err))
				} else if deleted > 0 {
					zap.L().Info("周期清理了过期日志文件",
						zap.Int("deleted", deleted),
						zap.Int64("freed_bytes", freed))
				}
			}
		}
	}()
}

func runWorker(boot config.Bootstrap, startup *startupLifecycle, shutdownSignal *runtime.ShutdownSignal) (exitCode int) {
	stats.MarkProcessStarted()

	startup.AddCleanup("停止 pprof", startPprofServer())

	if err := secrets.Configure(boot.SecretKey); err != nil {
		zap.L().Warn("加密密钥不可用，模型和 MCP 配置暂时无法应用", zap.Error(err))
	}

	var backgroundWG sync.WaitGroup
	var startupTasksWG sync.WaitGroup
	var memoryMgr *memory.Manager
	instanceDir := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if host.HasRuntimeContext() {
		host.ReportPhase(host.PhaseStarting, "")
	}
	db, err := memory.OpenDB(boot.DatabaseDSN)
	if err != nil {
		zap.L().Error("打开数据库失败", zap.Error(err))
		return 1
	}
	startup.AddCleanup("关闭数据库", func(context.Context) error {
		if memoryMgr != nil {
			return memoryMgr.Close()
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	})
	startup.AddCleanup("等待启动期后台任务退出", func(ctx context.Context) error {
		return waitGroupContext(ctx, &startupTasksWG)
	})
	if !startup.Check("数据库连接") {
		return 1
	}
	// 数据库不再有 pg_restore 恢复阶段：SQLite 库文件随 data/ 目录导入，
	// 导入后直接由下面的迁移阶段校验并升级版本。
	if err := startup.RunStage("startup.database_migration", 30*time.Minute, func(context.Context) error {
		return migrateSchemaAtStartup(db)
	}); err != nil {
		zap.L().Error("数据库迁移失败", zap.Error(err))
		return 1
	}

	startup.AddCleanup("停止异步任务", func(ctx context.Context) error {
		return asynctask.Default().StopAll(ctx)
	})
	if !startup.Check("数据库迁移") {
		return 1
	}
	if err := startup.RunStage("startup.database_maintenance", 5*time.Minute, func(ctx context.Context) error {
		return runStartupDatabaseMaintenance(ctx, db)
	}); err != nil {
		zap.L().Error("启动数据库维护失败", zap.Error(err))
		return 1
	}
	if !startup.Check("数据库维护") {
		return 1
	}
	learners.DefaultRegistry().SetStore(learners.NewGORMStore(db))
	if err := startup.RunStage("startup.dynamic_config", 2*time.Minute, func(context.Context) error {
		return config.InitStore(db)
	}); err != nil {
		zap.L().Error("加载动态配置失败", zap.Error(err))
		return 1
	}
	if !startup.Check("动态配置初始化") {
		return 1
	}
	var instConfigWatchCancel context.CancelFunc
	var instWatchCtx context.Context
	var instHolder *maibotcfg.InstanceHolder
	nativePlan := resolveNativeAMemorixStartupPlan()
	if instDir := nativePlan.LoadInstanceDir; instDir != "" {
		instHolder, err = maibotcfg.NewInstanceHolder(instDir)
		if err != nil {
			nativePlan.PrepareErr = fmt.Errorf("加载 A_Memorix 原生内核实例配置失败: %w", err)
			zap.L().Warn("MaiBot 实例 TOML 配置加载失败，继续沿用环境变量与数据库配置",
				zap.String("instance_dir", instDir), zap.Error(err))
		} else {
			instCfg := instHolder.Config()
			// 对齐 Python src/common/logger.py:57-73：实例配置就绪后按 LogConfig 重建日志（文件轮转与分级）
			applyInstanceLogConfig(instCfg, db)
			// 对齐 Python src/llm_models/request_snapshot.py:797-801：快照保留数读 log.llm_request_snapshot_limit
			llm.ConfigureRequestSnapshots(
				filepath.Join(instCfg.Dir, "logs", "llm_request_snapshots"),
				instCfg.Bot.Log.LlmRequestSnapshotLimit,
			)
			// 对齐 Python prompt_preview_logger.py:595-604：预览保留数读 log.maisaka_prompt_preview_limit
			display.ConfigureDefaultPreviewLimit(instCfg.Bot.Log.MaisakaPromptPreviewLimit)
			zap.L().Info("MaiBot 实例 TOML 配置已加载",
				zap.String("instance_dir", instCfg.Dir),
				zap.String("bot_config_version", instCfg.BotInner.Version),
				zap.String("model_config_version", instCfg.ModelInner.Version))
			botservices.ConfigureLLMCacheStatsFromInstance(instCfg)
			instHolder.RegisterReloadCallback(func(cfg *maibotcfg.InstanceConfig, _ []maibotcfg.ReloadScope) {
				botservices.ConfigureLLMCacheStatsFromInstance(cfg)
			})
			watchCtx, cancel := context.WithCancel(context.Background())
			instConfigWatchCancel = cancel
			instWatchCtx = watchCtx
			startup.AddCleanup("停止配置监听", func(context.Context) error {
				cancel()
				return nil
			})
			instHolder.StartConfigWatcher(watchCtx, maibotcfg.ConfigWatcherOptions{
				OnPolicyChangedContext: func(_ context.Context, policyPath string) error {
					manager, err := platformio.NewAdapterPolicyManager(policyPath)
					if err != nil {
						return err
					}
					if _, err := manager.GetDefaultActions(); err != nil {
						return fmt.Errorf("适配器策略文件校验失败: %w", err)
					}
					zap.L().Info("适配器策略已重新加载", zap.String("path", policyPath))
					return nil
				},
			})
		}
	}

	var sharingBotConfig *maibotcfg.BotConfigFile
	if instHolder != nil {
		sharingBotConfig = &instHolder.Config().Bot
	}
	if err := startup.RunStage("startup.sharing_groups", 1*time.Minute, func(ctx context.Context) error {
		return botservices.InitializeSharingGroups(ctx, db, sharingBotConfig)
	}); err != nil {
		zap.L().Error("初始化相通组失败", zap.Error(err))
		return 1
	}
	if !startup.Check("相通组初始化") {
		return 1
	}
	migrationConfigPath := filepath.Join(memory.InstanceRoot(), "config", "bot_config.toml")
	if instHolder != nil {
		migrationConfigPath = filepath.Join(instHolder.Dir(), "config", "bot_config.toml")
		botservices.ReloadChatMigrationConfig = func(context.Context) error {
			return instHolder.ReloadFromDisk([]maibotcfg.ReloadScope{maibotcfg.ReloadScopeBot})
		}
	}
	botservices.ChatMigrationConfigPath = migrationConfigPath
	botservices.ChatMigrationNotifyExpressionIndex = learners.DefaultRegistry().NotifyExpressionVectorBackfill
	memorix.ChatActivityGate = func(ctx context.Context, sessionID string, operation func() error) error {
		return botservices.ChatMigrationGateInstance.Activity(ctx, sessionID, operation)
	}
	if err := startup.RunStage("startup.chat_migrations", 1*time.Minute, func(ctx context.Context) error {
		return botservices.InitializeChatMigrations(ctx, db)
	}); err != nil {
		zap.L().Error("初始化聊天迁移失败", zap.Error(err))
		return 1
	}
	if !startup.Check("聊天迁移初始化") {
		return 1
	}
	promptMgr, err := prompt.NewManager()
	if err != nil {
		zap.L().Warn("Prompt 模板管理器创建失败", zap.Error(err))
	} else {
		zap.L().Info("Prompt 模板管理器已就绪",
			zap.String("locale", promptMgr.Locale()),
			zap.Int("template_count", len(promptMgr.Names())))
	}
	if instHolder != nil && promptMgr != nil {
		customRoot := prompt.CustomPromptsRoot(instHolder.Dir())
		if err := promptMgr.LoadFromDir(customRoot); err != nil {
			zap.L().Warn("custom_prompts 覆盖加载失败，沿用嵌入模板",
				zap.String("custom_prompts_dir", customRoot), zap.Error(err))
		} else {
			zap.L().Info("已扫描 custom_prompts 覆盖",
				zap.String("custom_prompts_dir", customRoot),
				zap.Int("template_count", len(promptMgr.Names())))
		}
		instHolder.RegisterReloadCallback(func(_ *maibotcfg.InstanceConfig, _ []maibotcfg.ReloadScope) {
			if err := promptMgr.LoadFromDir(customRoot); err != nil {
				zap.L().Warn("实例 TOML 重载后 custom_prompts 刷新失败",
					zap.String("custom_prompts_dir", customRoot), zap.Error(err))
			}
		})
		if instWatchCtx != nil {
			prompt.StartCustomOverlayWatcher(instWatchCtx, customRoot, promptMgr, prompt.OverlayWatcherOptions{})
		}
	}
	newsSvc := news.NewService(news.LoadConfigFromEnv())
	zap.L().Info("首页资讯服务已就绪", zap.Any("status", newsSvc.Status()))

	renderCfg := htmlrender.LoadConfigFromEnv()
	if instHolder != nil {
		if instCfg := instHolder.Config(); instCfg != nil {
			renderCfg = htmlrender.ApplyPluginRuntimeConfig(renderCfg, instCfg.Bot.PluginRuntime.Render, instHolder.Dir())
		}
	}
	htmlRenderSvc := htmlrender.NewService(renderCfg)
	startup.AddCleanup("关闭 HTML 渲染", func(context.Context) error {
		return htmlRenderSvc.Close()
	})
	zap.L().Info("HTML 渲染服务已就绪", zap.Any("status", htmlRenderSvc.Status()))

	pluginSpawnCfg := pluginruntime.LoadSpawnConfigFromEnv()
	pluginHost := pluginruntime.NewHost()
	pluginHost.SetInstanceRoot(pluginruntime.ResolveInstanceRoot())
	pluginRuntimeSettings := func() pluginruntime.RuntimeSettings {
		if instHolder == nil || instHolder.Config() == nil {
			return pluginruntime.DefaultRuntimeSettings()
		}
		pr := instHolder.Config().Bot.PluginRuntime
		return pluginruntime.RuntimeSettingsFromConfig(pr.HealthCheckIntervalSec, pr.MaxRestartAttempts, pr.RunnerSpawnTimeoutSec, pr.HookBlockingTimeoutSec, pr.IpcSocketPath)
	}
	pluginHost.ApplyRuntimeSettings(pluginRuntimeSettings())
	pluginHost.SetHookBlockingTimeoutGetter(func() float64 { return pluginRuntimeSettings().HookBlockingTimeout.Seconds() })
	// 对齐 Python compat_policy.py:12-21 与 integration.py:129：强制兼容开关读 debug.force_plugin_compatibility
	pluginHost.SetForcePluginCompatibility(forcePluginCompatibilityFromInstance(instHolder))
	startup.AddCleanup("停止插件运行时", func(ctx context.Context) error {
		return pluginHost.Stop(ctx)
	})
	pluginRunnerOpts := pluginruntime.StartRunnerOptionsFromSpawnConfig(pluginSpawnCfg, pluginruntime.StartRunnerOptions{
		Group:       pluginruntime.RunnerGroupThirdParty,
		HostVersion: version.String(),
	})
	pluginUploads, uploadErr := pluginruntime.NewUploadStore(filepath.Join(boot.MediaDir, "plugin_uploads"))
	if uploadErr != nil {
		zap.L().Warn("插件上传暂存目录初始化失败，webui.claim_upload 将保持未接线", zap.Error(uploadErr))
		pluginUploads = nil
	}

	modelStore := modelprofiles.NewStore(db)
	mcpStore := mcp.NewStore(db)
	startup.AddCleanup("关闭 MCP 连接", func(context.Context) error {
		return mcp.DefaultService().Close()
	})

	maimMsgCfg := maimserver.LoadConfigFromEnv()
	if instHolder != nil {
		if instCfg := instHolder.Config(); instCfg != nil {
			maimMsgCfg = maimserver.LoadConfigFromMaiBot(instCfg.Bot.MaimMessage)
		}
	}

	desktopPetEnv := desktoppet.LoadConfigFromEnv()
	desktopPetSvc := desktoppet.NewService(
		desktoppet.Config{
			Enabled:          desktopPetEnv.Enabled,
			ChatToolsEnabled: desktopPetEnv.ChatToolsEnabled,
			SkinID:           desktopPetEnv.SkinID,
		},
		desktopPetEnv.SpawnEnv,
	)
	desktoppet.SetDefaultService(desktopPetSvc)
	desktopPetSvc.SetHookEmitter(func(ctx context.Context, hookName string, kwargs map[string]any) {
		pluginHost.EmitHook(ctx, hookName, kwargs)
	})
	desktopPetSvc.SetButtonProvider(pluginHost)
	pluginHost.SubscribeStatusChanges(desktopPetSvc.NotifyButtonsChanged)
	tools.RegisterDesktopPetService(desktopPetSvc)
	startup.AddCleanup("停止桌宠", func(ctx context.Context) error {
		return desktopPetSvc.Stop(ctx)
	})
	if err := desktopPetSvc.Start(startup.Context()); err != nil {
		zap.L().Warn("桌宠子进程启动失败",
			zap.Error(err),
			zap.Any("status", desktopPetSvc.Status()))
	} else if desktopPetEnv.Enabled {
		st := desktopPetSvc.Status()
		zap.L().Info("桌宠子进程已启动（MAIBOT_DESKTOP_PET=1）",
			zap.Int("pid", st.PID),
			zap.Bool("running", st.Running))
	}
	if instHolder != nil {
		instHolder.RegisterReloadCallback(func(_ *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
			for _, scope := range scopes {
				if scope != maibotcfg.ReloadScopeBot {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := desktopPetSvc.Reload(ctx); err != nil {
					zap.L().Warn("桌宠配置热重载失败，保留上一版本", zap.Error(err))
				}
				return
			}
		})
	}

	// 平台接入统一走适配器：OneBot 等协议端由插件适配器通过 maim_message 接入
	platformIOMgr, err := platformio.NewManager()
	if err != nil {
		zap.L().Error("Platform IO 管理器创建失败", zap.Error(err))
		return 1
	}

	var maimMsgServer *maimserver.Server
	var maimMsgAdapter *maimbridge.Adapter
	var maimMsgCancel context.CancelFunc
	var maimMsgClosed atomic.Bool
	if maimMsgCfg.Enabled {
		maimMsgServer = maimserver.New(maimMsgCfg, platformIOMgr, nil)
		if instHolder != nil {
			instHolder.RegisterReloadCallback(func(cfg *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
				if maimMsgClosed.Load() || cfg == nil {
					return
				}
				for _, scope := range scopes {
					if scope != maibotcfg.ReloadScopeBot {
						continue
					}
					maimMsgServer.ApplyConfig(maimserver.LoadConfigFromMaiBot(cfg.Bot.MaimMessage))
					return
				}
			})
		}
		maimMsgAdapter = maimbridge.NewAdapter(maimMsgServer.Hub())
		maimMsgServer.SetInboundHandler(func(ctx context.Context, connPlatform string, msg maimmsg.MessageBase) error {
			envelope, err := maimbridge.InboundFromMessageBase(msg, connPlatform)
			if err != nil {
				return err
			}
			return maimMsgAdapter.EmitInbound(ctx, envelope)
		})
		if err := platformIOMgr.Register(maimMsgAdapter); err != nil {
			zap.L().Error("Platform IO 注册 maim_message 适配器失败", zap.Error(err))
			return 1
		}
		zap.L().Info("maim_message WS 已启用",
			zap.String("listen", maimMsgCfg.Addr()),
			zap.Bool("token_required", maimMsgCfg.TokenRequired()))
	}
	if !startup.Check("Platform IO 路由初始化") {
		return 1
	}
	if instHolder != nil {
		if dir := strings.TrimSpace(instHolder.Dir()); dir != "" {
			instanceDir = dir
		}
	}
	statsRecorder := modelstats.NewRecorder()
	modelstats.SetDefault(statsRecorder)
	statsRecorder.Start(db)

	msgCounters := stats.NewMessageCounters()
	stats.SetDefaultMessages(msgCounters)
	msgCounters.Start(db)

	onlineTracker := stats.NewOnlineTracker()
	stats.SetDefaultOnline(onlineTracker)
	onlineTracker.Start(db)
	aggregationCtx, aggregationCancel := context.WithCancel(context.Background())
	stopAggregation := stats.StartAggregationRefresher(aggregationCtx, db, stats.AggregationRefresherOptions{InstanceDir: instanceDir})
	detailedReporter := stats.NewDetailedReporter(stats.DetailedReportOptions{DB: db, InstanceDir: instanceDir})
	if err := detailedReporter.Start(aggregationCtx); err != nil {
		zap.L().Error("启动详细统计报告任务失败", zap.Error(err))
	} else {
		startup.AddCleanup("停止详细统计报告", func(context.Context) error {
			detailedReporter.Stop()
			return nil
		})
	}
	startup.AddCleanup("停止统计采集", func(context.Context) error {
		aggregationCancel()
		stopAggregation()
		statsRecorder.Close()
		msgCounters.Close()
		onlineTracker.Close()
		return nil
	})

	if err := startup.RunStage("startup.memory_manager", 2*time.Minute, func(context.Context) error {
		var initErr error
		memoryMgr, initErr = memory.NewManager(db, boot.MediaDir)
		return initErr
	}); err != nil {
		zap.L().Error("记忆管理器创建失败", zap.Error(err))
		return 1
	}
	monitor.SetEventStore(monitor.NewEventStore(db))
	llm.SetLLMMonitorEmitters(
		func(ev llm.LLMRetryEvent) {
			monitor.EmitLLMRetry(ev.SessionID, ev.TaskName, ev.RequestType, ev.ModelName, ev.Attempt, ev.MaxAttempts, ev.Reason, ev.RetryInterval)
		},
		func(ev llm.LLMErrorEvent) {
			monitor.EmitLLMError(ev.SessionID, ev.TaskName, ev.RequestType, ev.ModelName, ev.Message)
		},
	)
	// Bot 账号服务先于原生内核创建：插件 Host、人物画像与 memorix 共用同一实时来源
	botAccountService := newPluginBotAccountService(db, instHolder, platformIOMgr)
	configurePersoninfoBotSelfAccounts(startup.Context(), botAccountService)
	// 后端选择：native 显式或 auto 配置启用时尝试原生内核，失败按真实错误降级到 Remote/Disabled
	nativeStart := startNativeAMemorixBackend(nativePlan, instHolder, db, modelStore, botAccountService)
	backend := nativeStart.Backend
	nativeHost := nativeStart.Host
	if instHolder == nil && nativeStart.Holder != nil {
		instHolder = nativeStart.Holder
	}
	if nativeHost != nil {
		startup.AddCleanup("停止 A_Memorix 原生内核", func(ctx context.Context) error {
			return nativeHost.Stop(ctx)
		})
		zap.L().Info("Memorix Host 已接入 A_Memorix 原生内核",
			zap.String("backend", backend), zap.String("state", nativeHost.State()))
	} else {
		wireMemorixHostAfterNativeStart(nativePlan, nativeStart)
		// 对齐 Python main.py：未启用时也注册配置热重载回调，
		// 否则在 WebUI 打开 A_Memorix 开关后内核永远不会启动，只能重启进程
		registerDormantAMemorixHost(nativePlan, instHolder, db, nativeStart.Factory)
	}
	personinfo.DefaultRegistry().SetMemory(memoryMgr)
	emojisystem.DefaultRegistry().SetMemory(memoryMgr)
	wireReasoningPreviewSessionLookup(memoryMgr.GetDB())
	bootstrapCtx, bootstrapCancel := context.WithTimeout(startup.Context(), 30*time.Second)
	if err := emojisystem.DefaultRegistry().Reload(bootstrapCtx); err != nil {
		zap.L().Warn("加载表情包内存池失败", zap.Error(err))
	}
	bootstrapCancel()
	config.SetPersonaSource(func() config.PersonaConfig {
		return personaConfigFromInstance(instHolder)
	})
	if err := config.Reload(); err != nil {
		zap.L().Error("加载人格配置失败", zap.Error(err))
		return 1
	}
	if !startup.Check("人格与动态配置初始化") {
		return 1
	}
	if instHolder != nil {
		if err := maibotcfg.WireInstanceRuntimeBridge(instHolder); err != nil {
			zap.L().Warn("实例 TOML 桥接到 RuntimeConfig 失败，沿用数据库配置", zap.Error(err))
		} else {
			zap.L().Info("实例 TOML 已桥接 chat/typing/媒体字段到 RuntimeConfig 内存快照")
		}
	}
	wirePersonalityReloadBridge(instHolder)
	chatHub := internalchat.NewHub()
	// 实时推送的 reply 段只有 target_message_id，必须回查被引用消息补全引用卡片
	chatHub.SetReplyTargetLookup(func(ctx context.Context, messageID string) (internalchat.ReplyTarget, bool) {
		numeric, parseErr := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
		if parseErr != nil || numeric == 0 || memoryMgr == nil {
			return internalchat.ReplyTarget{}, false
		}
		log, err := memoryMgr.GetMessageLogBySessionAndMessageID(ctx, "", numeric)
		if err != nil || log == nil {
			return internalchat.ReplyTarget{}, false
		}
		return internalchat.ReplyTarget{
			Content:  strings.TrimSpace(log.ProcessedPlainText),
			SenderID: strconv.FormatInt(log.UserID, 10),
			Nickname: log.UserNickname,
			Cardname: log.UserCardname,
		}, true
	})
	// 内聊平台没有 Platform IO 驱动，插件/服务经 send.text 发到 internal 流时直接广播到 WebUI
	botservices.SetWebChatBroadcast(func(plainText string) {
		chatHub.SendBotSegments(plainText, nil)
	})
	chatRelay := internalchat.NewRelay(chatHub, os.Getenv("MAIBOT_INSTANCE_DIR"))
	// 文件服务在 Host 能力注册前创建，files.* 才能拿到同一实例
	fileCfg := filesvc.DefaultConfig(boot.MediaDir)
	fileCfg.DefaultTransferBaseURL = fmt.Sprintf("http://127.0.0.1:%d", boot.ServerPort)
	if instHolder != nil {
		botCfg := instHolder.Config().Bot
		fileCfg.MaxFileBytes = botCfg.FileMaxBytes()
		fileCfg.MaxStorageBytes = botCfg.FileMaxStorageBytes()
		fileCfg.MaxTextBytes = botCfg.FileMaxTextBytes()
		fileCfg.CacheDays = botCfg.FileCacheDays()
		fileCfg.MaxConcurrentTransfers = botCfg.FileMaxConcurrentTransfers()
		fileCfg.TransferTimeout = botCfg.FileTransferTimeout()
		fileCfg.TrustedDownloadOrigins = botCfg.FileTrustedDownloadOrigins()
		if baseURL := botCfg.FileTransferBaseURL(); baseURL != "" {
			fileCfg.DefaultTransferBaseURL = baseURL
		}
	}
	var fileStore *filesvc.Store
	if err := startup.RunStage("startup.file_store", 2*time.Minute, func(context.Context) error {
		var initErr error
		fileStore, initErr = filesvc.NewStore(db, fileCfg)
		return initErr
	}); err != nil {
		zap.L().Error("文件存储初始化失败", zap.Error(err))
		return 1
	}
	fileService := filesvc.NewService(fileStore, fileCfg)
	fileService.SetRemoteURLResolver(filesvc.RemoteURLResolverFunc(directRemoteURLResolver))
	pluginHost.SetFileScopeIssuer(fileService)
	// botAccountService 已在 A_Memorix 原生内核启动前创建，插件 Host 复用同一实例
	pluginHost.SetBotAccountPort(pluginBotAccountReporter{service: botAccountService})
	sessionsPort := hostports.NewSessions(memoryMgr.GetDB(), hostports.WithSessionRouteResolver(pluginSessionRouteResolver{
		platform: platformIOMgr,
		accounts: botAccountService,
	}))
	// WebUI / 插件发送共用同一 AvatarService（对齐 Python avatar_service 单例）
	avatarService := platformio.NewAvatarService(pluginruntime.ResolveInstanceRoot())
	avatarService.Provider = hostports.NewAdapterAvatarProvider(platformIOMgr)
	sessionsPort.SetAvatarService(avatarService)
	senderPort := hostports.NewSender(platformIOMgr, sessionsPort, filepath.Join(boot.MediaDir, "plugin_send"))
	gatewayPort := hostports.NewGateway(platformIOMgr, senderPort)
	// 对齐 Python plugin_driver.py:113-126：出站 file 段的 file_ref 解析为可下载 URL
	gatewayPort.SetFileResolver(func(ctx context.Context, fileRef, sessionID string) (string, error) {
		return fileService.DownloadURL(fileRef, sessionID, false, "")
	})
	pluginHost.SetMessageGatewayPort(gatewayPort)
	pluginMaxEmoji := func() int {
		if instHolder != nil {
			if cfg := instHolder.Config(); cfg != nil {
				return cfg.Bot.EmojiMaxRegNum()
			}
		}
		return 64
	}
	var maiAgent *maisaruntime.Agent
	var acpService *agent.Service
	marketPort := newPluginMarketPort()
	capDeps := pluginruntime.HostCapabilityDeps{
		Memory:        memoryMgr,
		Instance:      pluginruntime.InstanceBotConfig{Holder: instHolder},
		InternalChat:  chatRelay,
		Files:         fileService,
		Emojis:        emojisystem.DefaultRegistry().Manager(),
		Sender:        senderPort,
		LLM:           hostports.NewLLM(func() *llm.Router { return maiAgent.LLMRouter() }),
		Sessions:      sessionsPort,
		EmojiAdmin:    hostports.NewEmojiAdmin(emojisystem.DefaultRegistry().WebUI, memoryMgr, pluginMaxEmoji),
		Render:        hostports.NewRender(htmlRenderSvc),
		Web:           hostports.NewWeb(websearch.DefaultService()),
		Gateway:       gatewayPort,
		Uploads:       pluginUploads,
		PluginDataDir: hostports.PluginDataDirResolver(""),
		Database:      newPluginDatabasePort(memoryMgr.GetDB),
		Persons:       newPluginPersonPort(personinfo.DefaultRegistry()),
		Frequency:     newPluginFrequencyPort(func() *maisaruntime.Agent { return maiAgent }),
		Knowledge:     newPluginKnowledgePort(),
		Market:        marketPort,
		Statistics:    newPluginStatisticsPort(memoryMgr.GetDB),
		Components:    pluginHost.ComponentAdmin(),
	}
	var registeredCaps []string
	if err := startup.RunStage("startup.plugin_capabilities", 2*time.Minute, func(context.Context) error {
		var initErr error
		registeredCaps, initErr = pluginruntime.RegisterHostCapabilities(pluginHost, capDeps)
		return initErr
	}); err != nil {
		zap.L().Error("注册插件 Host 能力失败", zap.Error(err))
		return 1
	}
	if desktopPetSvc != nil {
		petCaps, petErr := desktoppet.RegisterCapabilities(pluginHost, desktopPetSvc)
		if petErr != nil {
			zap.L().Error("注册桌宠能力失败", zap.Error(petErr))
			return 1
		}
		registeredCaps = append(registeredCaps, petCaps...)
	}
	zap.L().Info("插件 Host 能力已注册", zap.Strings("capabilities", registeredCaps))
	wirePluginConfigReloadBridge(pluginHost, instHolder)
	cleanupCtx, cleanupCancel := context.WithTimeout(startup.Context(), 30*time.Second)
	if err := memoryMgr.CleanupToolResults(cleanupCtx, 24*time.Hour); err != nil {
		zap.L().Warn("清理无引用工具结果失败", zap.Error(err))
	}
	cleanupCancel()
	zap.L().Info("记忆系统已初始化")
	if err := startup.RunStage("startup.agent", 5*time.Minute, func(context.Context) error {
		var initErr error
		maiAgent, initErr = maisaruntime.New(memoryMgr, modelStore, mcpStore)
		return initErr
	}); err != nil {
		zap.L().Error("Agent 创建失败", zap.Error(err))
		return 1
	}
	startup.AddCleanup("停止 Agent", func(context.Context) error {
		maiAgent.Stop()
		return nil
	})
	maiAgent.SetPluginHost(pluginHost)
	maiAgent.SetFileService(fileService)
	// 对齐 Python message_repository.py:188-207 的 filter_bot：按 (platform, account) 对排除机器人账号
	if botAccountService != nil {
		maiAgent.SetBotAccountPairs(botAccountService.GetAllBotAccountPairs)
	}
	// 插件 Host 先于 Agent 构造，Agent 创建后补齐发送历史与昵称；失败只告警，不阻断插件发送
	if err := senderPort.SetBotName(maiAgent.BotName()); err != nil {
		zap.L().Warn("插件发送 Bot 昵称注入失败", zap.Error(err))
	}
	if err := senderPort.SetMaisakaHistoryAppender(hostports.NewMaisakaHistoryAppender(maiAgent)); err != nil {
		zap.L().Warn("插件发送 Maisaka 历史 appender 注入失败", zap.Error(err))
	}
	maiAgent.SetFileIngestor(func(ctx context.Context, msg *onebot.ConversationMessage, sessionID string) error {
		return ingestInboundFiles(ctx, fileService, msg, sessionID)
	})
	maiAgent.SetExternalFileSender(func(ctx context.Context, platform, conversationKind string, targetID int64, fileURL, name string) (int64, uint64, error) {
		return sendPlatformFile(ctx, platformIOMgr, platform, conversationKind, targetID, fileURL, name)
	})
	pluginHost.SubscribeStatusChanges(maiAgent.SchedulePluginReload)
	if instHolder != nil {
		maiAgent.SetInstanceHolder(instHolder)
		zap.L().Info("Agent 已绑定 MaiBot model_config 任务路由")
		// MCP 有效配置（servers 或 client 宿主能力）变化时才重建 generation 与共享连接
		instHolder.RegisterReloadCallback(func(_ *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
			botChanged := false
			for _, scope := range scopes {
				if scope == maibotcfg.ReloadScopeBot {
					botChanged = true
					break
				}
			}
			if !botChanged || !maiAgent.Ready() {
				return
			}
			signature := maiAgent.MCPConfigSignature()
			if signature == "" || signature == mcp.DefaultService().Signature() {
				return
			}
			zap.L().Info("MCP 配置已变化，重建共享连接")
			if err := maiAgent.Reload(); err != nil {
				zap.L().Warn("MCP 配置热重载失败", zap.Error(err))
			}
		})
	}
	if promptMgr != nil {
		emojisystem.DefaultRegistry().SetDescribeDeps(boot.StickerDir, promptMgr, maiAgent.LLMRouter())
	}
	var emojiMaintCancel context.CancelFunc
	if instHolder != nil {
		cfg := instHolder.Config()
		emojiMaintCtx, cancel := context.WithCancel(context.Background())
		emojiMaintCancel = cancel
		startupTasksWG.Add(1)
		go func() {
			defer startupTasksWG.Done()
			emojisystem.DefaultRegistry().RunMaintenance(emojiMaintCtx, emojisystem.MaintenanceOptions{
				StealEmoji:        cfg.Bot.EmojiStealEnabled(),
				MaxRegNum:         cfg.Bot.EmojiMaxRegNum(),
				DoReplace:         cfg.Bot.EmojiDoReplace(),
				CheckInterval:     cfg.Bot.EmojiCheckInterval(),
				MaxEmojiSizeBytes: cfg.Bot.EmojiMaxSizeBytes(),
				ContentFiltration: cfg.Bot.EmojiContentFiltration(),
				BotName:           maiAgent.BotName(),
			})
		}()
		startup.AddCleanup("停止 Emoji 维护", func(context.Context) error {
			emojiMaintCancel()
			return nil
		})
	}
	maiAgent.SetPlatformIOManager(platformIOMgr)
	if err := maiAgent.Start(); err != nil {
		zap.L().Error("Agent 启动失败", zap.Error(err))
		return 1
	}
	if !startup.Check("Agent 启动") {
		return 1
	}
	startScheduleService(instanceDir, db, maiAgent, desktopPetSvc, promptMgr)
	desktopPetSvc.SetPromptRenderer(func(name string) (string, error) {
		return promptMgr.Render(name, nil)
	})
	desktopPetSvc.SetOwnerTaskEnqueuer(func(ctx context.Context, sourceKind, text, reason string, images []media.Ref) error {
		kind, targetID, err := resolveOwnerAgentTarget(ctx, db)
		if err != nil {
			return err
		}
		_, err = maiAgent.EnqueueInternalTaskWithImages(ctx, kind, targetID, strings.TrimSpace(text), sourceKind, reason, images)
		return err
	})
	desktopPetSvc.SetOwnerSessionID(func(ctx context.Context) (string, error) {
		return resolveOwnerSessionID(ctx, db)
	})
	desktopPetSvc.SetCaptionSummarizer(func(ctx context.Context, data map[string]any) (string, error) {
		template, err := promptMgr.Render("desktop_pet_thinking_caption", nil)
		if err != nil {
			return "", err
		}
		stageContext := map[string]string{}
		for _, key := range []string{"stage", "detail", "round_text"} {
			value := []rune(anyString(data[key]))
			if len(value) > 240 {
				value = value[:240]
			}
			stageContext[key] = string(value)
		}
		encoded, err := sonic.Marshal(stageContext)
		if err != nil {
			return "", err
		}
		router := maiAgent.LLMRouter()
		if router == nil {
			return "", fmt.Errorf("LLM 路由器未接入")
		}
		_, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskFastModel)
		if err != nil {
			return "", err
		}
		message, err := client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(template + "\n" + string(encoded))})
		if err != nil {
			return "", err
		}
		var builder strings.Builder
		for _, block := range message.ContentBlocks {
			if block != nil && block.AssistantGenText != nil {
				builder.WriteString(block.AssistantGenText.Text)
			}
		}
		return builder.String(), nil
	})
	monitor.StageObserver = desktopPetSvc.ObserveStage
	maisaka.TodoContextProvider = func(sessionID string) string {
		ownerID, err := resolveOwnerSessionID(context.Background(), db)
		if err != nil || ownerID != sessionID {
			return ""
		}
		items := make([]map[string]any, 0)
		for _, item := range schedules.Default().ListItems() {
			itemSession := anyString(item["session_id"])
			if itemSession != "" && itemSession != sessionID {
				continue
			}
			copied := map[string]any{}
			for key, value := range item {
				copied[key] = value
			}
			if due, ok := copied["due_at"].(float64); ok {
				copied["due_at_local"] = time.Unix(0, int64(due*1e9)).In(time.Local).Format("2006-01-02T15:04:05-07:00")
			}
			items = append(items, copied)
		}
		encoded, err := sonic.Marshal(items)
		if err != nil {
			return ""
		}
		rendered, err := promptMgr.Render("desktop_pet_todo_context", map[string]string{"todos": string(encoded)})
		if err != nil {
			return ""
		}
		return rendered
	}
	maisaka.ExplorationReuseContextProvider = func(sessionID string) string {
		if !desktopPetSvc.Config().ExplorationEnabled {
			return ""
		}
		ownerID, err := resolveOwnerSessionID(context.Background(), db)
		if err != nil || ownerID != sessionID {
			return ""
		}
		rendered, err := promptMgr.Render("desktop_pet_exploration_reuse", nil)
		if err != nil {
			return ""
		}
		return rendered
	}
	desktopPetSvc.SetProactiveWake(func(ctx context.Context) error {
		text, err := promptMgr.Render("desktop_pet_proactive_wake", nil)
		if err != nil {
			return err
		}
		kind, targetID, err := resolveOwnerAgentTarget(ctx, db)
		if err != nil {
			return err
		}
		_, err = maiAgent.EnqueueInternalTask(ctx, kind, targetID, strings.TrimSpace(text), "desktop_pet_proactive_wake", "桌宠定期主动思考")
		return err
	})
	startup.AddCleanup("停止日程服务", func(context.Context) error {
		schedules.Default().Stop()
		return nil
	})
	// 记忆自动化：入站身份观察 + 发送后人物事实写回，历史重分类后台循环随宿主启停
	automationCtx, automationCancel := context.WithCancel(context.Background())
	defer automationCancel()
	automationEnabled := func() bool { return nativeAMemorixEnabled(nativePlan, instHolder) }
	automationFactEnabled := func() bool {
		if instHolder == nil {
			return true
		}
		cfg := instHolder.Config()
		return cfg == nil || cfg.Bot.AMemorix.Integration.PersonFactWritebackEnabled
	}
	automationSummaryEnabled := func() bool {
		if instHolder == nil {
			return true
		}
		cfg := instHolder.Config()
		return cfg == nil || cfg.Bot.AMemorix.Integration.ChatSummaryWritebackEnabled
	}
	// 聊天摘要与图片记忆写回 worker：发送后按阈值触发摘要、入站图片登记任务库后重放
	// 对齐 Python main.py:197-203 的 memory_automation_service.start() 与 :328-333 的逆序 shutdown
	instanceConfig := func() *maibotcfg.InstanceConfig {
		if instHolder == nil {
			return nil
		}
		return instHolder.Config()
	}
	imageDescriptionHook := &memorywriteback.ImageDescriptionHook{}
	chatSummaryWriteback := memorywriteback.NewChatSummaryWritebackService(memorywriteback.ChatSummaryWritebackOptions{
		Enabled: automationSummaryEnabled,
		Settings: func() memorywriteback.ChatSummaryWritebackSettings {
			return memorywriteback.ChatSummaryWritebackSettingsFromInstance(instanceConfig())
		},
		Kernel: memorywriteback.CurrentChatSummaryKernel,
		CountMessages: func(ctx context.Context, sessionID string, endTime *time.Time) (int64, error) {
			return memoryMgr.CountMessagesBySessionID(ctx, sessionID, endTime)
		},
	})
	imageWriteback := memorywriteback.NewImageWritebackService(memorywriteback.ImageWritebackOptions{
		Enabled: func() bool {
			return automationEnabled() && memorywriteback.ImageMemoryEnabledFromInstance(instanceConfig())
		},
		Settings: func() memorywriteback.ImageWritebackSettings {
			return memorywriteback.ImageWritebackSettingsFromInstance(instanceConfig())
		},
		JournalPath: func() string { return memorywriteback.ImageWritebackJournalPath(instanceDir) },
		FindMessage: func(ctx context.Context, sessionID string, messageID string) (*memory.MessageLog, error) {
			return memoryMgr.FindMessageBySessionIDAndMessageID(ctx, sessionID, messageID)
		},
		ImageRuntime:      memorywriteback.CurrentImageMemoryRuntime,
		ImageMemory:       memorywriteback.CurrentImageMemoryAction,
		Kernel:            memorywriteback.CurrentChatSummaryKernel,
		CachedDescription: imageDescriptionHook.Describe,
	})
	if err := chatSummaryWriteback.Start(); err != nil {
		zap.L().Warn("聊天摘要写回 worker 启动失败", zap.Error(err))
	}
	if err := imageWriteback.Start(); err != nil {
		zap.L().Warn("图片记忆写回 worker 启动失败", zap.Error(err))
	}
	startup.AddCleanup("停止记忆写回 worker", func(ctx context.Context) error {
		return errors.Join(chatSummaryWriteback.Stop(ctx), imageWriteback.Stop(ctx))
	})
	memoryAutomation := personinfo.NewMemoryAutomation(personinfo.MemoryAutomationOptions{
		Registry:       personinfo.DefaultRegistry(),
		Enabled:        automationEnabled,
		FactEnabled:    automationFactEnabled,
		SummaryEnabled: automationSummaryEnabled,
		FactHandler:    maiAgent.HandleSentPersonFact,
		ImageIncoming: func(ctx context.Context, message personinfo.IncomingMessage) error {
			return imageWriteback.EnqueueIncoming(ctx, message.SessionID, message.MessageID, message.Source)
		},
		SummaryEnqueue: func(ctx context.Context, message personinfo.SentMessage) error {
			return chatSummaryWriteback.EnqueueSent(message.SessionID, message.UserID, message.GroupID, message.Timestamp)
		},
	})
	if err := memoryAutomation.Start(automationCtx); err != nil {
		zap.L().Warn("记忆自动化启动失败，人物事实写回未接线", zap.Error(err))
		memoryAutomation = nil
	} else {
		startup.AddCleanup("停止记忆自动化", func(ctx context.Context) error {
			automationCancel()
			botservices.SetDefaultSendMessageHook(nil)
			return memoryAutomation.Stop(ctx)
		})
		maiAgent.SetSentMessageHandler(memoryAutomation.OnAgentMessageSent)
		botservices.SetDefaultSendMessageHook(memoryAutomation)
	}
	// 图片缓存、描述与入站过大图片处理必须在 Agent 消费消息前就绪
	imageStore := imagesystem.NewGORMStore(db)
	if err := imageStore.AutoMigrate(); err != nil {
		zap.L().Error("初始化图片元数据表失败", zap.Error(err))
		return 1
	}
	instanceRoot := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if instanceRoot == "" {
		instanceRoot, _ = os.Getwd()
	}
	imageManager, err := imagesystem.NewManager(imagesystem.ManagerOptions{
		Store:     imageStore,
		Root:      instanceRoot,
		Describer: &imagesystem.RouterDescriber{Router: maiAgent.LLMRouter(), Prompts: promptMgr},
	})
	if err != nil {
		zap.L().Error("图片管理器创建失败", zap.Error(err))
		return 1
	}
	// 图片写回 worker 复用同一图片管理器读取缓存描述，避免重复解码
	imageDescriptionHook.Set(imageManager.GetCachedImageDescription)
	imageCleanupCtx, imageCleanupCancel := context.WithCancel(context.Background())
	startup.AddCleanup("停止图片同步", func(ctx context.Context) error {
		imageCleanupCancel()
		return imageManager.Shutdown(ctx)
	})
	startupTasksWG.Add(1)
	go func() {
		defer startupTasksWG.Done()
		imagesystem.NewImageCacheCleanupRunner(imageStore, imageManager.Root()).
			RunPeriodicImageCacheCleanup(imageCleanupCtx, func() imagesystem.ImageCacheCleanupSettings {
				return imagesystem.LoadImageSettingsFromRoot(imageManager.Root()).CacheCleanup
			})
	}()
	var maimHub maimbridge.Hub
	apiRunning := func() bool { return false }
	if maimMsgServer != nil {
		maimHub = maimMsgServer.Hub()
		apiRunning = func() bool { return true }
	}
	replyTiming := func() *maibotcfg.ChatReplyTimingConfig {
		if instHolder == nil {
			return nil
		}
		cfg := instHolder.Config()
		if cfg == nil {
			return nil
		}
		timing := cfg.Bot.Chat.ReplyTiming
		return &timing
	}
	messageReceive := func() *maibotcfg.MessageReceiveConfig {
		if instHolder == nil {
			return nil
		}
		cfg := instHolder.Config()
		if cfg == nil {
			return nil
		}
		receive := cfg.Bot.MessageReceive
		return &receive
	}
	inboundFilter, filterErr := chatwiring.NewConfigMessageFilter(messageReceive)
	if filterErr != nil {
		zap.L().Error("消息接收配置校验失败", zap.Error(filterErr))
		return 1
	}
	// 与 WebUI 设置页使用同一实例根目录，避免页面和启动读取不同的 runtime.toml。
	executionDir := config.InstanceRoot()
	executionProfile, executionErr := execution.LoadProfile(executionDir)
	if executionErr != nil {
		if errors.Is(executionErr, execution.ErrInvalidMode) {
			zap.L().Error("执行模式配置无效，拒绝启动", zap.Error(executionErr))
			return 1
		}
		// 文件损坏时仍要尊重 MAIBOT_EXECUTION_MODE，否则运维显式指定的模式会被静默丢弃。
		zap.L().Warn("读取执行配置失败，按默认配置与环境变量继续启动", zap.Error(executionErr))
		fallbackProfile, envErr := execution.ApplyEnvMode(execution.DefaultProfile())
		if envErr != nil {
			zap.L().Error("执行模式环境变量无效，拒绝启动", zap.Error(envErr))
			return 1
		}
		executionProfile = fallbackProfile
	}
	featureToggles, featureErr := execution.LoadFeatureToggles(executionDir)
	if featureErr != nil {
		zap.L().Warn("读取功能开关失败，全部按关闭处理", zap.Error(featureErr))
		featureToggles = execution.DefaultFeatureToggles()
	}
	nativeMode := executionProfile.Mode == execution.ModeNative
	nativeSessionScheduler := nativeMode && execution.IsFeatureImplemented("native_session_scheduler") && featureToggles.NativeSessionScheduler
	nativeMessageCommitter := nativeMode && execution.IsFeatureImplemented("native_message_committer") && featureToggles.NativeMessageCommitter
	zap.L().Info("执行配置已加载",
		zap.String("execution_mode", string(executionProfile.Mode)),
		zap.Bool("native_session_scheduler", nativeSessionScheduler),
		zap.Bool("native_message_committer", nativeMessageCommitter),
		zap.Int("worker_pool_size", executionProfile.WorkerPoolSize),
	)
	executionPolicies := policy.NewRegistry()
	if nativeSessionScheduler {
		nativeScheduler := chatscheduler.New(context.Background(), chatscheduler.Options{
			Workers: executionProfile.WorkerPoolSize,
			OnError: func(sessionKey string, err error) {
				zap.L().Error("原生会话任务失败", zap.String("session_id", sessionKey), zap.Error(err))
			},
		})
		executionPolicies.SessionScheduler.Set(nativeSessionSchedulerAdapter{engine: nativeScheduler})
		zap.L().Info("原生会话调度器已启动",
			zap.String("execution_mode", string(execution.ModeNative)),
			zap.Int("workers", nativeScheduler.Workers()),
		)
	}
	if nativeMessageCommitter {
		nativeCommitter := committer.NewNative()
		executionPolicies.MessageCommitter.Set(nativeCommitter)
		maiAgent.SetMessageCommitter(nativeCommitter)
		zap.L().Info("原生动入站提交器已启动", zap.String("execution_mode", string(execution.ModeNative)))
	}
	chatPipeline := newChatRuntimePipelineWithOptions(context.Background(), chatPipelineOptions{
		Agent:        maiAgent,
		Images:       newChatImagePreprocessor(imageManager),
		Platform:     platformIOMgr,
		DB:           db,
		Broadcaster:  webuichat.NewChatBroadcaster(chatHub),
		Files:        fileService,
		ImageManager: imageManager,
		People:       personinfo.DefaultRegistry(),
		Messages:     newMessageService(db, 0, maiAgent.BotName()),
		Memory:       memoryAutomation,
		SentHook:     memoryAutomation,
		APIHub:       maimHub,
		APIEnabled: func() bool {
			if maimMsgServer == nil {
				return false
			}
			return maimMsgServer.Config().APIEnabled
		},
		APIRunning:    apiRunning,
		ReplyTiming:   replyTiming,
		MessageFilter: inboundFilter,
		// 对齐 Python bot.py:305-315：命令权限读 plugin.permission 与 plugin.command_permissions
		CommandPermissions: func() ([]string, map[string]localoperator.CommandPermissionRule) {
			if instHolder == nil || instHolder.Config() == nil {
				return nil, nil
			}
			pluginCfg := instHolder.Config().Bot.Plugin
			return pluginCfg.Permission, commandPermissionRules(pluginCfg.CommandPermissions)
		},
		// 对齐 Python global_config.plugin.silent_permission_denied
		SilentPermission: func() bool {
			if instHolder == nil || instHolder.Config() == nil {
				return false
			}
			return instHolder.Config().Bot.Plugin.SilentPermissionDenied
		},
		// 对齐 Python global_config.plugin.command_require_self_mention
		RequireSelfMention: func() bool {
			if instHolder == nil || instHolder.Config() == nil {
				return false
			}
			return instHolder.Config().Bot.Plugin.CommandRequireSelfMention
		},
		LocalOperator: func(platform, userID string) bool {
			// 对齐 Python is_local_operator：按 platform 与 additional_config 判定
			return localoperator.IsLocalOperator(platform, map[string]any{"user_id": userID})
		},
		// 对齐 Python integration.py:1319-1400：适配器上下线卸载/重载 adapter 类型插件
		AdapterLifecycle: &chatwiring.AdapterLifecyclePort{
			TakeOffline: func(ctx context.Context) (messagereceive.AdapterLifecycleResult, error) {
				result, err := pluginHost.TakeAdaptersOffline(ctx)
				return adapterLifecycleResult(result), err
			},
			BringOnline: func(ctx context.Context) (messagereceive.AdapterLifecycleResult, error) {
				result, err := pluginHost.BringAdaptersOnline(ctx)
				return adapterLifecycleResult(result), err
			},
		},
		BotNickname:              maiAgent.BotName(),
		LLMRouter:                maiAgent.LLMRouter(),
		VoiceResolver:            newChatVoiceResolver(),
		EnableVoiceTranscription: chatVoiceASREnabled(instHolder),
		Policies:                 executionPolicies,
	})

	gatewayPort.SetInboundHandler(chatPipeline.HandlePluginMessage)

	// 对齐 Python integration.py:1349-1352：适配器下线后清理失去发送链路的聊天流
	if chatPipeline != nil && chatPipeline.manager != nil {
		pluginruntime.SetAdapterPolicyBlockedCloser(chatPipeline.manager.ClosePolicyBlockedChats)
	}

	// 对齐 Python main.py:85：旧版适配器回送真实消息 ID 时回填本地消息
	if maimMsgServer != nil && chatPipeline != nil && chatPipeline.chat != nil && chatPipeline.chat.ChatBot != nil {
		maimMsgServer.RegisterCustomHandler("message_id_echo", func(ctx context.Context, payload map[string]any, _ map[string]any) error {
			return chatPipeline.chat.ChatBot.EchoMessageProcess(ctx, payload)
		})
	}

	// 外部 ACP Agent 默认关闭；启用失败只记录真实错误，不影响主链路
	acpAgentHost := newACPAgentHost(memoryMgr, maiAgent, maiAgent.BotName())
	// 传话附图落盘到 Agent 工作区，对齐 Python service.workspace
	// 实例 TOML 加载失败时 holder 为 nil，回退实例根目录，沿用环境变量配置继续启动
	workspaceRoot := strings.TrimSpace(nativePlan.LoadInstanceDir)
	if workspaceRoot == "" {
		workspaceRoot = strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	}
	if instHolder != nil {
		workspaceRoot = instHolder.Dir()
	}
	acpAgentHost.workspace = filepath.Join(workspaceRoot, "data", "agent", "workspace")
	acpFileHost := newACPFileHost(fileService, memoryMgr)
	acpService = startACPAgentService(instHolder, promptMgr, maiAgent, acpAgentHost, acpFileHost)
	maiAgent.SetACPService(acpService)
	if acpService != nil {
		startup.AddCleanup("停止外接 Agent", func(context.Context) error {
			stopACPAgentService(acpService)
			return nil
		})
		wireACPAgentReload(instHolder, acpService, maiAgent)
	}
	chatRelay.SetDeliver(func(msg *onebot.ConversationMessage) error {
		if msg == nil {
			return nil
		}
		submit := chatPipeline.SessionTaskSubmitter()
		if submit == nil {
			maiAgent.HandleInternalChatMessage(msg)
			return nil
		}
		channelKey := internalchat.ConversationChannelKey(msg)
		submitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := submit(submitCtx, channelKey, func(context.Context) error {
			maiAgent.HandleInternalChatMessage(msg)
			return nil
		}); err != nil {
			zap.L().Warn("插件聊天消息被原生会话调度拒绝",
				zap.String("channel", channelKey),
				zap.Error(err))
			return err
		}
		return nil
	})
	if botName := maiAgent.BotName(); botName != "" {
		chatHub.SetBotIdentity(botName)
	}
	if desktopPetSvc != nil {
		desktopPetSvc.SetChatRelay(func(ctx context.Context, pluginID, content string, images []desktoppet.ChatRequestImage) (string, error) {
			payloadImages := make([]map[string]any, 0, len(images))
			for _, image := range images {
				payloadImages = append(payloadImages, map[string]any{
					"name":      "",
					"mime_type": image.MimeType,
					"base64":    image.Base64,
				})
			}
			return chatRelay.RelayPluginChatMessage(ctx, pluginID, content, payloadImages)
		})
	}
	if pluginSpawnCfg.Enabled {
		// 对齐 config.py：旧 chinese_typo 设置在写回主配置前迁入内置插件
		if instHolder != nil {
			if instanceCfg := instHolder.Config(); instanceCfg != nil {
				if err := pluginruntime.MigrateBuiltinTypoConfig(pluginSpawnCfg.ProjectRoot, instanceCfg.Bot.LegacyChineseTypo, instanceCfg.Bot.LoadMeta.ChineseTypoKeys); err != nil {
					zap.L().Error("错字配置迁移到内置插件失败", zap.Error(err))
					return 1
				}
				if err := pluginruntime.MigrateBuiltinKeywordReactionConfig(pluginSpawnCfg.ProjectRoot, &instanceCfg.Bot.KeywordReaction); err != nil {
					zap.L().Error("关键词反应配置迁移到内置插件失败", zap.Error(err))
					return 1
				}
			}
		}
// 对齐 integration.py：索引和加载前先把旧版官方适配器迁入内置配置布局。
			// 单个适配器迁移失败只隔离对应插件，其余插件继续启动。
			adapterMigrationErrors := map[string]string{}
			backup, migrateErr := pluginruntime.MigrateQQOfficialAdapter(pluginSpawnCfg.ProjectRoot, pluginruntime.ResolveInstanceRoot())
			if migrateErr != nil {
				reason := "适配器迁移失败，请修复后重启 Bot：" + migrateErr.Error()
				zap.L().Error("QQ 官方适配器迁移失败，已隔离该适配器", zap.Error(migrateErr))
				adapterMigrationErrors["Mai-with-u.qq-adapter"] = reason
			}
			if backup != "" {
				zap.L().Info("QQ 官方适配器旧安装已备份", zap.String("backup", backup))
			}
			telegramBackup, telegramErr := pluginruntime.MigrateTelegramAdapter(pluginSpawnCfg.ProjectRoot, pluginruntime.ResolveInstanceRoot())
			if telegramErr != nil {
				reason := "适配器迁移失败，请修复后重启 Bot：" + telegramErr.Error()
				zap.L().Error("Telegram 适配器迁移失败，已隔离该适配器", zap.Error(telegramErr))
				adapterMigrationErrors["maibot-team.telegram-adapter"] = reason
				adapterMigrationErrors["Sengoku.telegram-adapter"] = reason
			}
			if telegramBackup != "" {
				zap.L().Info("Telegram 适配器旧安装已备份", zap.String("backup", telegramBackup))
			}
			weixinBackup, weixinErr := pluginruntime.MigrateWeixinClawbotAdapter(pluginSpawnCfg.ProjectRoot, pluginruntime.ResolveInstanceRoot())
			if weixinErr != nil {
				reason := "适配器迁移失败，请修复后重启 Bot：" + weixinErr.Error()
				zap.L().Error("微信 ClawBot 适配器迁移失败，已隔离该适配器", zap.Error(weixinErr))
				adapterMigrationErrors["maibot-team.weixin-clawbot-adapter"] = reason
				adapterMigrationErrors["local.weixin-clawbot-adapter"] = reason
			}
			if weixinBackup != "" {
				zap.L().Info("微信 ClawBot 适配器旧安装已备份", zap.String("backup", weixinBackup))
			}
			pluginHost.SetAdapterMigrationErrors(adapterMigrationErrors)

			// 登记进程内原生适配器骨架（不 Construct）；可承接后写入 nativeInProcessReasons
			if err := pluginruntime.RegisterOfficialNativeAdapterPlaceholders(pluginruntime.ResolveInstanceRoot(), pluginSpawnCfg.ProjectRoot); err != nil {
				zap.L().Error("登记原生适配器失败", zap.Error(err))
			} else {
pluginHost.EnsureNativeMessageGateways()
					pluginHost.EnsureSnowlumaLocalAPIs()
					for _, pluginID := range []string{
						"maibot-team.snowluma-adapter",
						"maibot-team.telegram-adapter",
						"maibot-team.weixin-clawbot-adapter",
						"Mai-with-u.qq-adapter",
					} {
						pluginHost.SetNativeInProcessReason(pluginID, "已由进程内 Go 适配器承接")
						if err := pluginHost.StartNativeRuntimeIfEnabled(context.Background(), pluginID); err != nil {
							zap.L().Warn("原生适配器冷启失败", zap.String("plugin_id", pluginID), zap.Error(err))
							pluginHost.RememberNativeLoadStatus(pluginID, "failed")
							continue
						}
						if platformio.DefaultNativeRuntime().IsStarted(pluginID) {
							pluginHost.RememberNativeLoadStatus(pluginID, "success")
						} else {
							pluginHost.RememberNativeLoadStatus(pluginID, "inactive")
						}
					}
			}

			// 对齐 Python integration.py:904-908 的 plugin_runtime.sync_dependencies 阶段：
			// 启动 Runner 前先分析并补装插件依赖，环境变化时 Runner 直接加载最新环境
			syncPipeline := pluginruntime.NewPluginDependencyPipeline(pluginSpawnCfg.ProjectRoot)
			syncResult, syncChanged := pluginHost.SyncPluginDependencies(context.Background(), syncPipeline, pluginHost.PluginDirs(), nil)
			if syncResult.EnvironmentChanged {
				zap.L().Info("插件依赖流水线已更新当前 Python 环境，启动时将直接加载最新环境")
			}
			if syncResult.InstallError != "" {
				zap.L().Warn("插件依赖自动安装失败，插件仍将尝试加载", zap.String("error", syncResult.InstallError))
			}
			if len(syncChanged) > 0 {
				zap.L().Info("插件依赖同步更新了阻止加载列表", zap.Strings("plugin_ids", syncChanged))
			}

			// 公共：原生 Repair + 配置 watcher（不依赖 StartRunner 成功）
			if repairedNative := pluginHost.RepairNativeVersionlessPluginConfigs(context.Background()); len(repairedNative) > 0 {
				zap.L().Info("原生适配器配置已按默认配置重建", zap.Strings("plugin_ids", repairedNative))
			}
			nativeWatchCtx, cancelNativeWatch := context.WithCancel(context.Background())
			startup.AddCleanup("停止原生适配器配置监听", func(context.Context) error {
				cancelNativeWatch()
				return nil
			})
			pluginruntime.StartNativePluginConfigWatcher(nativeWatchCtx, pluginruntime.NativePluginConfigWatcherOptions{
				ConfigPaths: func() map[string]string {
					out := map[string]string{}
					for _, pluginID := range platformio.DefaultNativeRuntime().ListRegistered() {
						if path, err := pluginHost.ResolveNativePluginConfigPath(pluginID); err == nil {
							out[pluginID] = path
						}
					}
					return out
				},
				OnConfigChanged: func(ctx context.Context, pluginID string) {
					if _, err := pluginHost.ApplyPluginConfig(ctx, pluginID); err != nil {
						zap.L().Warn("原生适配器配置变更同步失败",
							zap.String("plugin_id", pluginID), zap.Error(err))
					}
				},
			})

			startRunner := pluginHost.HasEnabledNonNativePythonPlugins()
			if !startRunner {
				zap.L().Info("无启用中的社区 Python 插件，跳过 StartRunner；原生适配器由进程内承接")
			}
			var runnerState *pluginruntime.RunnerState
			var err error
			if startRunner {
				runnerState, err = pluginHost.StartRunner(context.Background(), pluginRunnerOpts)
				if err != nil {
					pluginruntime.LogSpawnSkipped(pluginSpawnCfg, err)
				} else {
					// 缺 config_version 的插件配置会让 Runner 拒绝加载，启动后按默认配置重建并重载
					if repaired := pluginHost.RepairVersionlessPluginConfigs(context.Background()); len(repaired) > 0 {
						if _, err := pluginHost.ReloadPlugins(context.Background(), repaired, "config_repaired", nil); err != nil {
							zap.L().Warn("重建插件配置后重载失败", zap.Strings("plugin_ids", repaired), zap.Error(err))
						} else {
							zap.L().Info("插件配置已按默认配置重建并重载", zap.Strings("plugin_ids", repaired))
						}
					}
					zap.L().Info("插件运行时 Runner 已启动",
						zap.Int("hook_specs", len(pluginHost.HookSpecs().ListHookSpecs())),
						zap.Strings("host_capabilities", registeredCaps),
						zap.String("ipc", runnerState.IPCAddress),
						zap.String("group", string(runnerState.Group)),
						zap.Bool("python_spawned", runnerState.PythonSpawned()))
					// 对齐 Python integration.py:1857-1877 与 :2138-2152：源码变更后重新同步依赖并重启 Runner
					watchCtx, cancelWatch := context.WithCancel(context.Background())
					startup.AddCleanup("停止插件源码监听", func(context.Context) error {
						cancelWatch()
						return nil
					})
					// 对齐 Python integration.py:1998-2043：每个插件的 config.toml 单独监听，变更只定向通知该插件
					pluginruntime.StartPluginConfigWatcher(watchCtx, pluginruntime.PluginConfigWatcherOptions{
						PluginDirs: func() []string {
							dirs := pluginHost.PluginDirs()
							return append(dirs, pluginHost.NativePluginConfigRoots()...)
						},
						OnConfigChanged: func(ctx context.Context, pluginID string) {
							if _, err := pluginHost.ApplyPluginConfig(ctx, pluginID); err != nil {
								zap.L().Warn("插件配置变更同步失败",
									zap.String("plugin_id", pluginID), zap.Error(err))
							}
						},
					})
					pluginruntime.StartPluginSourceWatcher(watchCtx, pluginruntime.PluginSourceWatcherOptions{
						PluginDirs: pluginHost.PluginDirs,
						OnSourceChanged: func(ctx context.Context) {
							syncPipeline := pluginruntime.NewPluginDependencyPipeline(pluginSpawnCfg.ProjectRoot)
							syncResult, syncChanged := pluginHost.SyncPluginDependencies(ctx, syncPipeline, pluginHost.PluginDirs(), nil)
							reason := "file_watcher"
							if syncResult.EnvironmentChanged {
								reason = "file_watcher_dependency_install"
							} else if len(syncChanged) > 0 {
								reason = "file_watcher_blocklist_changed"
							}
							if err := pluginHost.RestartRunner(ctx, pluginRunnerOpts); err != nil {
								zap.L().Warn("插件源码变更后重启 Runner 失败",
									zap.String("reason", reason), zap.Error(err))
							}
						},
					})
				}
			}
	} else {
		zap.L().Info("插件运行时 Host 已就绪（MAIBOT_PLUGIN_SPAWN=0，仅宿主内置组件）",
			zap.Int("hook_specs", len(pluginHost.HookSpecs().ListHookSpecs())),
			zap.Strings("host_capabilities", registeredCaps))
	}
	healthCtx, healthCancel := context.WithCancel(context.Background())
	stopHealthMonitor := pluginHost.StartHealthMonitor(healthCtx, 0, func(ctx context.Context) error {
		zap.L().Warn("Runner 健康检查连续失败，尝试重启")
		return pluginHost.RestartRunner(ctx, pluginRunnerOpts)
	})
	startup.AddCleanup("停止插件健康检查", func(context.Context) error {
		stopHealthMonitor()
		healthCancel()
		return nil
	})
	startup.AddCleanup("关闭聊天主链", func(ctx context.Context) error {
		return chatPipeline.Close(ctx)
	})
	platformIOMgr.SetPluginGatewayInvoker(func(ctx context.Context, pluginID, componentName string, args map[string]any, timeoutMS int64) (map[string]any, error) {
		envelope, err := pluginHost.InvokeMessageGateway(ctx, pluginID, componentName, args, timeoutMS)
		if err != nil {
			return nil, err
		}
		if len(envelope.Error) > 0 {
			return map[string]any{"error": envelope.Error}, nil
		}
		return envelope.Payload, nil
	})
	platformIOMgr.SetPluginFileURLInvoker(func(ctx context.Context, pluginID string, source map[string]any, timeoutMS int64) (string, error) {
		entry := pluginHost.APIRegistry().GetAPI(pluginID, "adapter.files.resolve", "1", true)
		if entry == nil {
			return "", fmt.Errorf("适配器尚未实现文件解析能力")
		}
		envelope, err := pluginHost.InvokeAPI(ctx, pluginID, entry.HandlerName, map[string]any{"source": source}, timeoutMS)
		if err != nil {
			return "", err
		}
		if len(envelope.Error) > 0 {
			return "", fmt.Errorf("适配器解析文件失败")
		}
		success, _ := envelope.Payload["success"].(bool)
		if !success {
			return "", fmt.Errorf("适配器解析文件失败")
		}
		result, _ := envelope.Payload["result"].(map[string]any)
		urlText, _ := result["url"].(string)
		urlText = strings.TrimSpace(urlText)
		if urlText == "" {
			return "", fmt.Errorf("适配器没有返回有效下载地址")
		}
		return urlText, nil
	})
	platformIOMgr.SetInboundDispatcher(func(ctx context.Context, envelope platformio.InboundMessageEnvelope) error {
		if envelope.DriverID == maimbridge.DriverIDMaimMsg {
			msg, ok := maimbridge.ConversationMessageFromInbound(envelope)
			if !ok || msg == nil {
				zap.L().Debug("maim_message 入站无法转为 Agent 消息，已跳过",
					zap.String("platform", envelope.RouteKey.Platform),
					zap.String("external_message_id", envelope.ExternalMessageID))
				return nil
			}
			if !maimbridge.ReadyForAgent(msg) {
				zap.L().Debug("maim_message 入站缺少可转换的文本/图片身份，已跳过",
					zap.String("platform", envelope.RouteKey.Platform),
					zap.String("external_message_id", envelope.ExternalMessageID))
				return nil
			}
			chatPipeline.Handle(ctx, msg)
			return nil
		}
		zap.L().Warn("Platform IO 入站来自未识别的驱动，已跳过",
			zap.String("driver_id", envelope.DriverID),
			zap.String("external_message_id", envelope.ExternalMessageID))
		return nil
	})
	if err := platformIOMgr.Start(context.Background()); err != nil {
		zap.L().Error("Platform IO 启动失败", zap.Error(err))
		return 1
	}
	startup.AddCleanup("停止 Platform IO", func(ctx context.Context) error {
		return platformIOMgr.Stop(ctx)
	})
	if maimMsgServer != nil {
		// 协议只挂在 Worker 的进程间 HTTP 上，由宿主 /maim/{bot_id}/{protocol} 转发。
		if maimMsgCancel != nil {
			maimMsgCancel()
		}
		startup.AddCleanup("停止 maim_message 服务", func(context.Context) error {
			maimMsgClosed.Store(true)
			return nil
		})
	}

	telemetryCtx, telemetryCancel := context.WithCancel(context.Background())
	telemetry := stats.NewTelemetry(version.String(), telemetryAdapters(platformIOMgr), stats.TelemetryOptions{
		InstanceDir: instanceDir,
		DB:          db,
		StatsEnabled: func() bool {
			return telemetryConfigEnabled(instHolder)
		},
		ModelAssignments: func() map[string][]string {
			return maiAgent.LLMRouter().ModelTaskAssignments()
		},
		BotAccounts: func() []stats.BotAccount {
			return telemetryBotAccounts(platformIOMgr, 0)
		},
	})
	telemetry.Start(telemetryCtx)
	startup.AddCleanup("停止遥测", func(context.Context) error {
		telemetryCancel()
		telemetry.Close()
		return nil
	})
	zap.L().Info("文件服务已就绪", zap.String("blob_root", fileStore.Config().RootDir))
	if cleanupStats, err := fileService.Cleanup(true); err != nil {
		zap.L().Warn("文件启动清理失败", zap.Error(err))
	} else if len(cleanupStats) > 0 {
		zap.L().Info("文件启动清理完成", zap.Int("expired", cleanupStats["expired"]), zap.Int("interrupted", cleanupStats["interrupted"]))
	}
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	defer backgroundCancel()
	backgroundStop := make(chan struct{})
	var backgroundStopOnce sync.Once
	stopBackground := func() {
		backgroundStopOnce.Do(func() {
			backgroundCancel()
			close(backgroundStop)
		})
	}
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-backgroundStop:
				return
			case <-ticker.C:
				if _, err := fileService.Cleanup(false); err != nil {
					zap.L().Warn("文件清理失败，保留文件并在下一次巡检重试", zap.Error(err))
				}
			}
		}
	}()

	// 图片路径一次性规整：启动时判断 phase，后台分批执行，中断后下次启动继续
	imagePathMaintenance := botservices.NewImagePathMaintenanceService(db, instanceRoot)
	if shouldNormalize, scheduleErr := imagePathMaintenance.ShouldScheduleBackground(backgroundCtx); scheduleErr != nil {
		zap.L().Warn("检查图片路径规整状态失败", zap.Error(scheduleErr))
	} else if shouldNormalize {
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			imagePathMaintenance.RunBackground(backgroundCtx)
		}()
	}
	// 数据迁移临时文件清理：启动立即一次，之后每小时一次
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		webuiapi.RunPeriodicTransferTempCleanup(backgroundCtx)
	}()
	// Prompt 预览孤立图片巡检：启动立即一次，之后每小时一次
	backgroundWG.Add(1)
	go func() {
		defer backgroundWG.Done()
		display.DefaultPromptPreviewLogger().RunPeriodicCleanup(backgroundCtx)
	}()

	// Maisaka CLI（maibot maisaka）：挂载 BufferCLI；与 BotConsole 互斥以免抢 stdin
	maisakaDecision := cli.DecideMaisakaCLIStart(os.Stdin)
	switch {
	case maisakaDecision.Start && chatPipeline.chat != nil && chatPipeline.chat.ChatBot != nil:
		zap.L().Info(maisakaDecision.Notice)
		modelName := func() string { return "未配置" }
		if instHolder != nil {
			if cfg := instHolder.Config(); cfg != nil {
				label := cli.FirstPlannerModelName(cfg.Model)
				modelName = func() string {
					if label == "" {
						return "未配置"
					}
					return label
				}
				if nick := strings.TrimSpace(cfg.Bot.Bot.Nickname); nick != "" {
					cli.SetCLIMessageNickname(nick)
				}
			}
		}
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			err := cli.RunMaisakaCLI(backgroundCtx, cli.MaisakaCLIRunOptions{
				Platform:  platformIOMgr,
				Receiver:  chatPipeline.chat.ChatBot,
				Nickname:  maiAgent.BotName,
				ModelName: modelName,
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				zap.L().Error("Maisaka CLI 处理失败", zap.Error(err))
			}
			// BufferCLI 结束（EOF）时请求关停，对齐「CLI 拥有进程寿命」的交互预期
			shutdownSignal.Trigger("maisaka_cli_eof")
		}()
	case maisakaDecision.Start:
		zap.L().Warn("maibot maisaka 已请求启动，但消息处理链未接入，已跳过 BufferCLI")
	case maisakaDecision.Notice != "":
		zap.L().Warn(maisakaDecision.Notice)
	}

	// 终端输入：配置开启且 stdin 为交互式终端时启动；MaisakaCLI 占用 stdin 时跳过
	consoleEnabled := false
	if !maisakaDecision.Start && instHolder != nil {
		if cfg := instHolder.Config(); cfg != nil {
			consoleEnabled = cfg.Bot.Debug.EnableConsoleInput
		}
	}
	consoleDecision := cli.DecideBotConsoleStart(consoleEnabled, os.Stdin)
	var hostConsole *cli.HeadlessConsole
	if host.HasRuntimeContext() && consoleEnabled && chatPipeline.chat != nil && chatPipeline.chat.ChatBot != nil {
		hostConsole = cli.NewHeadlessConsole(backgroundCtx, platformIOMgr, chatPipeline.chat.ChatBot, maiAgent.BotName)
	}
	switch {
	case consoleDecision.Start && chatPipeline.chat != nil && chatPipeline.chat.ChatBot != nil:
		zap.L().Info(consoleDecision.Notice)
		backgroundWG.Add(1)
		go func() {
			defer backgroundWG.Done()
			err := cli.RunBotConsole(backgroundCtx, cli.BotConsoleRunOptions{
				Platform: platformIOMgr,
				Receiver: chatPipeline.chat.ChatBot,
				Nickname: maiAgent.BotName,
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				zap.L().Error("终端输入处理失败", zap.Error(err))
			}
		}()
	case consoleDecision.Start:
		zap.L().Warn("终端输入已配置为启用，但消息处理链未接入，已跳过")
	case consoleDecision.Notice != "":
		zap.L().Warn(consoleDecision.Notice)
	}

	maiAgent.SetWebChatSender(chatHub)
	if desktopPetSvc != nil {
		chatHub.SubscribeBotMessages(func(payload map[string]any) {
			// 桌宠只跟随普通内部聊天的主人私聊，虚拟群消息不进入桌宠气泡
			if payload["group_id"] != nil {
				return
			}
			text, _ := payload["content"].(string)
			segments, _ := payload["segments"].([]map[string]any)
			desktopPetSvc.NotifyReply(segments, text)
		})
		maiAgent.SetThinkingNotifier(func(_ string, _ int64) {
			desktopPetSvc.NotifyThinking()
		})
	}
	botAccountPairs := func(ctx context.Context) ([][2]string, error) {
		if botAccountService == nil {
			return nil, nil
		}
		return botAccountService.GetAllBotAccountPairs(ctx)
	}
	// typed-nil 不得装进接口：*agent.Service 为 nil 时直接传 nil 接口，
	// 否则 webui 侧 `ACPAgent == nil` 判断失效，Status 会在 nil 接收者上 panic
	var acpAgentService webuiapi.AgentControlService
	if acpService != nil {
		acpAgentService = acpService
	}
	desktopPetChat := func(ctx context.Context, text string) (string, error) {
		return chatRelay.RelayPluginChatMessage(ctx, "builtin.desktop_pet", text, nil)
	}
	app := webapp.New(boot, memoryMgr, maiAgent, modelStore, mcpStore, fileService, newsSvc, htmlRenderSvc, chatHub, pluginHost, acpAgentService, desktopPetSvc, desktopPetChat, botAccountService, platformIOMgr.PluginAccountOnline, botAccountPairs, platformIOMgr, avatarService)
	if maimMsgServer != nil {
		var apiHandler http.HandlerFunc
		if maimMsgCfg.APIEnabled {
			apiHandler = maimMsgServer.MaimAPIHandler()
		}
		app.SetMaimHandlers(maimMsgServer.MaimLegacyHandler(), apiHandler)
	}
	if hostConsole != nil {
		app.SetHostConsole(hostConsole.Submit, hostConsole.Completions)
		startup.AddCleanup("关闭宿主终端输入", func(ctx context.Context) error {
			hostConsole.Close(ctx)
			return nil
		})
	}
	app.SetSessionSchedulerStats(chatPipeline.SessionSchedulerStats)
	app.SetSessionTaskSubmitter(chatPipeline.SessionTaskSubmitter())
	app.SetEnforcePublicOutboundProvider(func() bool {
		if instHolder == nil || instHolder.Config() == nil {
			return true
		}
		return instHolder.Config().Bot.Webui.EnforcePublicOutboundUrl
	})
	selfUpdateManager := selfupdate.DefaultManager()
	selfUpdateManager.StartBackgroundRefresh()
	app.PluginMarket().SetRuntime(pluginmarket.NewRuntimeCoordinatorWithRunner(pluginHost, pluginRunnerOpts))
	// Python src/runtime.py:165 在 Worker 初始化前输出终端更新公告；Go 需要市场服务就绪后再检查插件兼容更新
	terminalNoticePluginsDir, terminalNoticeDirErr := pluginmarket.ResolvePluginsDir(instanceRoot)
	if terminalNoticeDirErr != nil {
		zap.L().Warn("解析插件目录失败，终端公告跳过插件兼容性检查", zap.Error(terminalNoticeDirErr))
		terminalNoticePluginsDir = ""
	}
	webuiapi.WireTerminalUpdateNoticeWithPlugins(
		instanceRoot, version.String(), terminalNoticePluginsDir, webuiapi.NewMarketplaceLoader(app.PluginMarket()),
	)
	marketPort.SetService(app.PluginMarket())
	fileService.SetPluginSender(app.SendFileToChat)
	// 对齐 Python http_service/app.py:57-60：WebUI 挂载后输出登录 Token
	webuiapi.LogStartupAccessToken(os.Getenv("MAIBOT_INSTANCE_DIR"))
	httpServer := app.Server()
	startup.AddCleanup("关闭 HTTP 服务", func(ctx context.Context) error {
		selfUpdateManager.StopBackgroundRefresh()
		return httpServer.Shutdown(ctx)
	})

	// 好友删除没有 OneBot 事件，靠定期同步兜底；被踢出群即使漏了事件也会在这里纠正
	startup.AddCleanup("停止后台任务", func(ctx context.Context) error {
		stopBackground()
		return waitGroupContext(ctx, &backgroundWG)
	})

	// 对齐 Python http_service/server.py:126-178：按 http.host 列表多地址绑定并输出访问地址
	// 在宿主下运行时只监听宿主分配的进程间通道，端口由宿主持有
	bindHosts := []string{""}
	if host.HasRuntimeContext() {
		if contract, err := host.CurrentRuntimeContext(); err == nil {
			zap.L().Info("Worker 仅在宿主分配的通道上监听", zap.String("endpoint", contract.HTTPEndpoint))
		}
	} else {
		bindHosts = app.BindHosts()
		if len(bindHosts) == 0 {
			zap.L().Error("没有可绑定的 HTTP 监听地址，管理后台未启动")
		} else {
			app.LogBindAddresses(bindHosts)
		}
	}
	httpCtx, cancelHTTP := context.WithCancel(context.Background())
	startup.AddCleanup("取消 HTTP 监听", func(context.Context) error {
		if host.HasRuntimeContext() {
			host.ReportPhase(host.PhaseStopping, "")
		}
		cancelHTTP()
		return nil
	})
	startupTasksWG.Add(1)
	go func() {
		defer startupTasksWG.Done()
		if len(bindHosts) == 0 {
			return
		}
		zap.L().Info("管理后台启动", zap.Strings("hosts", bindHosts), zap.Int("port", boot.ServerPort))
		if err := app.Listen(httpCtx, bindHosts); err != nil && err != http.ErrServerClosed {
			zap.L().Error("管理后台异常退出", zap.Error(err))
		}
	}()

	if !startup.Check("Platform IO 与 HTTP 启动") {
		return 1
	}
	if host.HasRuntimeContext() {
		host.ReportPhase(host.PhaseReady, "")
	}
	// 模型来源有两处：model_config.toml 的任务路由与 WebUI 模型档案。
	// 不能只看档案表是否存在活跃记录，否则纯 TOML 部署永远不会 Activate。
	if err := maiAgent.Activate(); err != nil {
		if errors.Is(err, maisaruntime.ErrNoModel) {
			zap.L().Info("尚未配置模型，等待后台设置")
		} else {
			zap.L().Warn("首次模型激活失败，等待后台修正", zap.Error(err))
		}
	}

	// 启动完成后触发 on_start，对齐 main.py 的 event_bus.emit(EventType.ON_START)
	startEventCtx, startEventCancel := context.WithTimeout(startup.Context(), 10*time.Second)
	if !pluginHost.EmitLifecycleEvent(startEventCtx, pluginruntime.EventOnStart) {
		zap.L().Warn("on_start 事件被插件中止")
	}
	startEventCancel()
	if !startup.Check("on_start 事件") {
		return 1
	}
	startup.MarkStarted()

	restartRequest := make(chan struct{}, 1)
	runtime.SetRestartTrigger(restartRequest)

	zap.L().Info("麦麦已上线，按 Ctrl+C 退出")
	select {
	case <-shutdownSignal.Requested():
		zap.L().Info("正在关闭...", zap.String("reason", shutdownSignal.Reason()))
	case <-restartRequest:
		zap.L().Info("WebUI 请求重启，正在停止...")
		exitCode = runtime.RestartExitCode
	}

	// 关停计划覆盖前置入口、记忆内核与最终资源；未接线的能力不加入计划，避免伪造成功
	hooks := workerShutdownHooks{
		StopLogBroadcast: func(context.Context) error {
			logger.StopWebUIBroadcasts()
			return nil
		},
		StopMessageEntry: func(ctx context.Context) error {
			if maimMsgCancel != nil {
				maimMsgCancel()
			}
			return platformIOMgr.Stop(ctx)
		},
		StopDesktopPet: func(ctx context.Context) error {
			if desktopPetSvc == nil {
				return nil
			}
			return desktopPetSvc.Stop(ctx)
		},
		StopAsyncTasks: func(ctx context.Context) error {
			stopBackground()
			return waitGroupContext(ctx, &backgroundWG)
		},
		StopACPAgent: func(context.Context) error {
			stopACPAgentService(acpService)
			return nil
		},
		StopChatLoop: func(ctx context.Context) error {
			if chatPipeline != nil {
				if err := chatPipeline.Close(ctx); err != nil {
					zap.L().Warn("关闭心流主链失败", zap.Error(err))
					maiAgent.Stop()
					return err
				}
			}
			maiAgent.Stop()
			return nil
		},
		StopHTTP: func(ctx context.Context) error {
			selfUpdateManager.StopBackgroundRefresh()
			return httpServer.Shutdown(ctx)
		},
		StopConfigWatcher: func(context.Context) error {
			if instConfigWatchCancel != nil {
				instConfigWatchCancel()
			}
			if instHolder != nil {
				watchStats := instHolder.ConfigWatcherStats()
				zap.L().Info("配置监听统计",
					zap.Int64("batches", watchStats.BatchesSeen),
					zap.Int64("changes", watchStats.ChangesSeen),
					zap.Int64("succeeded", watchStats.CallbacksSucceeded),
					zap.Int64("failed", watchStats.CallbacksFailed),
					zap.Int64("timed_out", watchStats.CallbacksTimedOut),
					zap.Int64("skipped_cooldown", watchStats.CallbacksSkippedCooldown))
			}
			return nil
		},
		EmitOnStop: func(ctx context.Context) error {
			if !pluginHost.EmitLifecycleEvent(ctx, pluginruntime.EventOnStop) {
				return fmt.Errorf("on_stop 事件被插件中止")
			}
			return nil
		},
		StopPluginRuntime: func(ctx context.Context) error {
			stopHealthMonitor()
			healthCancel()
			if err := pluginHost.Stop(ctx); err != nil {
				return fmt.Errorf("停止插件运行时: %w", err)
			}
			return nil
		},
		StopImageSync: func(ctx context.Context) error {
			imageCleanupCancel()
			if imageManager == nil {
				return nil
			}
			if err := imageManager.Shutdown(ctx); err != nil {
				zap.L().Warn("关闭图片管理器失败", zap.Error(err))
				return err
			}
			return nil
		},
		StopMemoryWriter: func(ctx context.Context) error {
			queue := personinfo.DefaultRegistry().WritebackQueue()
			if !queue.Stats().Started {
				return nil
			}
			return queue.Shutdown(ctx)
		},
		StopMemoryKernel: func(ctx context.Context) error {
			if nativeHost == nil {
				return nil
			}
			return nativeHost.Stop(ctx)
		},
		ShutdownEmoji: func(context.Context) error {
			if emojiMaintCancel != nil {
				emojiMaintCancel()
			}
			return nil
		},
		CloseMCP: func(context.Context) error {
			return mcp.DefaultService().Close()
		},
		CancelRemaining: func(ctx context.Context) error {
			return waitGroupContext(ctx, &startupTasksWG)
		},
		StopStats: func(context.Context) error {
			aggregationCancel()
			stopAggregation()
			statsRecorder.Close()
			msgCounters.Close()
			onlineTracker.Close()
			return nil
		},
		CloseTelemetry: func(context.Context) error {
			telemetryCancel()
			telemetry.Close()
			return nil
		},
		CloseHTMLRender: func(context.Context) error {
			return htmlRenderSvc.Close()
		},
		CleanupUpdateTemp: func(context.Context) error {
			return cleanupSelfUpdateTempFiles(os.Getenv("MAIBOT_INSTANCE_DIR"))
		},
		CloseMemoryDB: func(context.Context) error {
			return memoryMgr.Close()
		},
	}
	plan := newWorkerShutdownPlan(hooks)
	coordinator := runtime.NewShutdownCoordinator()
	report, completed := coordinator.RunWithSignal(context.Background(), plan, shutdownSignal)
	if !completed {
		zap.L().Error("收到第二次中断信号，跳过剩余关停步骤并强制退出")
		os.Exit(1)
	}
	if !report.Success {
		failed := make([]string, 0, len(report.Steps))
		for _, step := range report.Steps {
			if !step.Success {
				failed = append(failed, step.Name)
			}
		}
		zap.L().Error("应用优雅关闭未完成；退出状态为失败",
			zap.Duration("duration", report.Duration),
			zap.Strings("failed_steps", failed),
			zap.Strings("failures", report.Failures))
		if exitCode == 0 {
			exitCode = 1
		}
	} else {
		zap.L().Info("应用优雅关闭完成", zap.Duration("duration", report.Duration))
	}

	if exitCode == runtime.RestartExitCode {
		zap.L().Info("Worker 将以退出码 42 结束，交由 Runner 重启")
	} else {
		zap.L().Info("再见！")
	}
	return exitCode
}

// wirePluginConfigReloadBridge 把实例配置热重载广播给订阅了 bot/model 全局配置的插件
func wirePluginConfigReloadBridge(host *pluginruntime.Host, holder *maibotcfg.InstanceHolder) {
	if host == nil || holder == nil {
		return
	}
	holder.RegisterReloadCallback(func(cfg *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
		if cfg == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, scope := range scopes {
			switch scope {
			case maibotcfg.ReloadScopeBot:
				notifyPluginConfigScope(ctx, host, "bot", cfg.Bot)
			case maibotcfg.ReloadScopeModel:
				notifyPluginConfigScope(ctx, host, "model", cfg.Model)
			}
		}
	})
}

// notifyPluginConfigScope 按全局作用域向订阅插件推送配置快照
func notifyPluginConfigScope(ctx context.Context, host *pluginruntime.Host, scope string, value any) {
	subscribers := host.GetConfigReloadSubscribers(scope)
	if len(subscribers) == 0 {
		return
	}
	raw, err := sonic.Marshal(value)
	if err != nil {
		zap.L().Warn("序列化全局配置失败，跳过插件配置广播", zap.String("scope", scope), zap.Error(err))
		return
	}
	data := map[string]any{}
	if err := sonic.Unmarshal(raw, &data); err != nil {
		zap.L().Warn("反序列化全局配置失败，跳过插件配置广播", zap.String("scope", scope), zap.Error(err))
		return
	}
	for _, pluginID := range subscribers {
		if _, err := host.NotifyPluginConfigUpdated(ctx, pluginID, data, "", scope); err != nil {
			zap.L().Debug("插件配置热更新推送失败",
				zap.String("plugin_id", pluginID),
				zap.String("scope", scope),
				zap.Error(err))
		}
	}
}

// personaConfigFromInstance 从实例 TOML 构造运行时人格配置；未声明时回退内置文本
func personaConfigFromInstance(holder *maibotcfg.InstanceHolder) config.PersonaConfig {
	card, simulation := config.BuiltinPersonaTexts()
	if holder != nil {
		card = maibotcfg.PersonaCardFor(holder, card)
	}
	return config.PersonaConfig{
		PresetName:  config.PersonaPresetFallbackName,
		PersonaCard: card,
		Simulation:  simulation,
	}
}

// wirePersonalityReloadBridge 在 bot_config 热重载后刷新人格与动态配置快照
func wirePersonalityReloadBridge(holder *maibotcfg.InstanceHolder) {
	if holder == nil {
		return
	}
	holder.RegisterReloadCallback(func(_ *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
		for _, scope := range scopes {
			if scope != maibotcfg.ReloadScopeBot {
				continue
			}
			if err := config.Reload(); err != nil {
				zap.L().Warn("bot_config 热重载后刷新人格快照失败", zap.Error(err))
				return
			}
			zap.L().Info("bot_config [personality] 已刷新到运行时人格快照")
			return
		}
	})
}

// telemetryAdapters 汇总 Platform IO 已注册驱动，供遥测上报适配器快照
func telemetryAdapters(mgr *platformio.Manager) []stats.AdapterSnapshot {
	out := make([]stats.AdapterSnapshot, 0, 4)
	seen := make(map[string]struct{}, 4)
	if mgr != nil {
		for _, desc := range mgr.AdapterDescriptors() {
			platform := strings.TrimSpace(desc.Platform)
			if platform == "" {
				continue
			}
			protocol := strings.TrimSpace(desc.DriverID)
			if raw, ok := desc.Metadata["protocol"].(string); ok && strings.TrimSpace(raw) != "" {
				protocol = strings.TrimSpace(raw)
			}
			key := platform + "\x00" + protocol
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, stats.AdapterSnapshot{Platform: platform, Protocol: protocol})
		}
	}
	if len(out) == 0 {
		out = append(out, stats.AdapterSnapshot{Platform: "qq", Protocol: "onebot11"})
	}
	return out
}

// telemetryBotAccounts 汇总已注册账号与 OneBot 自身账号，供遥测区分 bot_sent
func telemetryBotAccounts(mgr *platformio.Manager, selfID int64) []stats.BotAccount {
	out := make([]stats.BotAccount, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(platform, userID string) {
		platform = strings.TrimSpace(platform)
		userID = strings.TrimSpace(userID)
		if platform == "" || userID == "" {
			return
		}
		key := platform + "\x00" + userID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, stats.BotAccount{Platform: platform, UserID: userID})
	}
	if mgr != nil {
		for _, desc := range mgr.AdapterDescriptors() {
			add(desc.Platform, desc.AccountID)
		}
	}
	if selfID > 0 {
		add("qq", strconv.FormatInt(selfID, 10))
	}
	return out
}

// telemetryConfigEnabled 读取 bot_config [telemetry].enable；实例 TOML 未加载时按 MaiBot 默认启用
func telemetryConfigEnabled(holder *maibotcfg.InstanceHolder) bool {
	if holder == nil {
		return true
	}
	cfg := holder.Config()
	if cfg == nil {
		return true
	}
	return cfg.Bot.TelemetryEnabled()
}

const (
	memorixBackendAuto   = "auto"
	memorixBackendNative = "native"
)

// acpConfigFromBot 把实例 [agent] 配置转换为 agent.Config；空值走 Python 默认
func acpConfigFromBot(raw maibotcfg.AgentConfig) agent.Config {
	cfg := agent.DefaultConfig()
	cfg.Enabled = raw.Enabled
	cfg.AllowUsers = append([]string(nil), raw.AllowUsers...)
	cfg.AllowGroups = append([]string(nil), raw.AllowGroups...)
	cfg.PlannerEnabled = raw.PlannerEnabled
	cfg.PlannerSessions = append([]string(nil), raw.PlannerSessions...)
	switch raw.Connection {
	case string(agent.ConnectionDSHDesktop):
		cfg.Connection = agent.ConnectionDSHDesktop
	case string(agent.ConnectionCustom):
		cfg.Connection = agent.ConnectionCustom
	case string(agent.ConnectionDSH), "":
		cfg.Connection = agent.ConnectionDSH
	default:
		cfg.Connection = agent.ConnectionKind(raw.Connection)
	}
	if strings.TrimSpace(raw.DesktopManifest) != "" {
		cfg.DesktopManifest = raw.DesktopManifest
	}
	if strings.TrimSpace(raw.AcpCommand) != "" {
		cfg.ACPCommand = raw.AcpCommand
	}
	if raw.AcpArgs != nil {
		cfg.ACPArgs = append([]string(nil), raw.AcpArgs...)
	}
	cfg.ACPCwd = raw.AcpCwd
	cfg.ACPRemoteCwd = raw.AcpRemoteCwd
	cfg.MCPPort = raw.McpPort
	if raw.AcpEnv != nil {
		cfg.ACPEnv = make(map[string]string, len(raw.AcpEnv))
		for key, value := range raw.AcpEnv {
			cfg.ACPEnv[key] = value
		}
	}
	cfg.ACPModel = raw.AcpModel
	if raw.AcpTimeout > 0 {
		cfg.ACPTimeoutSec = raw.AcpTimeout
	}
	return cfg
}

// startACPAgentService 启动外部 ACP Agent 接线；默认关闭时只创建空服务，不启动任务
func startACPAgentService(holder *maibotcfg.InstanceHolder, promptMgr *prompt.Manager, maiAgent *maisaruntime.Agent, agentHost agent.AgentToolHost, fileHost agent.FileToolHost) *agent.Service {
	if holder == nil {
		return nil
	}
	instCfg := holder.Config()
	if instCfg == nil {
		return nil
	}
	opts := agent.ServiceOptions{
		DataDir:       filepath.Join(holder.Dir(), "data", "agent"),
		AgentToolHost: agentHost,
		FileHost:      fileHost,
	}
	if promptMgr != nil {
		opts.RenderPrompt = promptMgr.Render
	}
	if maiAgent != nil {
		opts.Profile = maiAgent.BotName()
		name := maiAgent.BotName()
		var mem *memory.Manager
		if host, ok := agentHost.(*acpAgentHost); ok && host != nil {
			mem = host.mem
			if strings.TrimSpace(host.name) != "" {
				name = host.name
			}
		}
		render := opts.RenderPrompt
		opts.ReportFailure = func(target string, taskIDs []string, errMsg string) {
			reportACPFailure(maiAgent, mem, render, name, target, taskIDs, errMsg)
		}
	}
	service := agent.NewService(acpConfigFromBot(instCfg.Bot.Agent), opts)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.Start(ctx); err != nil {
		zap.L().Warn("外部 ACP Agent 启动失败，保持未连接", zap.Error(err))
		return service
	}
	if service.Status().Enabled {
		zap.L().Info("外部 ACP Agent 已启用",
			zap.String("connection", string(service.Status().Backend)),
			zap.String("state", service.Status().AgentState))
	}
	return service
}

// stopACPAgentService 关闭外部 ACP Agent；失败只记录真实错误
func stopACPAgentService(service *agent.Service) {
	if service == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		zap.L().Warn("外部 ACP Agent 停止失败", zap.Error(err))
	}
}

// wireACPAgentReload 在 bot_config 热重载后按真实配置启停 ACP 服务
func wireACPAgentReload(holder *maibotcfg.InstanceHolder, service *agent.Service, maiAgent *maisaruntime.Agent) {
	if holder == nil || service == nil {
		return
	}
	holder.RegisterReloadCallback(func(cfg *maibotcfg.InstanceConfig, scopes []maibotcfg.ReloadScope) {
		if cfg == nil {
			return
		}
		service.SetProfile(profileForACPReload(maiAgent))
		next := acpConfigFromBot(cfg.Bot.Agent)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if !next.Enabled {
			if err := service.Stop(ctx); err != nil {
				zap.L().Warn("外部 ACP Agent 关闭失败", zap.Error(err))
			}
			return
		}
		if err := service.Reload(ctx, next); err != nil {
			zap.L().Warn("外部 ACP Agent 配置热重载失败，保留旧连接", zap.Error(err))
		}
	})
}

func profileForACPReload(maiAgent *maisaruntime.Agent) string {
	if maiAgent == nil {
		return ""
	}
	return maiAgent.BotName()
}

// resolveMemorixBackend 解析后端选择，默认 auto 以原生内核优先
// 返回值 explicit 表示显式指定后端
func resolveMemorixBackend() (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("MAIBOT_MEMORIX_BACKEND")))
	switch raw {
	case memorixBackendAuto, memorixBackendNative:
		return raw, true
	case "":
		return memorixBackendAuto, false
	default:
		zap.L().Warn("MAIBOT_MEMORIX_BACKEND 取值无效，按 auto 处理",
			zap.String("value", raw))
		return memorixBackendAuto, false
	}
}

// startNativeAMemorixHost 启动 A_Memorix 原生宿主并注册 Host
// 只有真正进入 ready 才视为可用，失败时释放实例并返回真实错误供调用方降级
// newAMemorixHostService 构造原生宿主（不启动内核）
func newAMemorixHostService(holder *maibotcfg.InstanceHolder, db *gorm.DB, kernelFactory func() memorix.KernelFactory) (*memorix.AMemorixHostService, error) {
	if holder == nil {
		return nil, fmt.Errorf("实例配置未加载，无法装配 A_Memorix 原生内核")
	}
	if kernelFactory == nil {
		return nil, fmt.Errorf("A_Memorix 原生内核工厂未注入")
	}
	// 对齐 Python host_service.py：按相通组解析长期记忆共享范围
	return memorix.NewAMemorixHostService(memorix.HostServiceOptions{
		ConfigStore:   memorix.NewHolderHostConfigStore(holder),
		KernelFactory: kernelFactory(),
		SharedScope: func(sessionID string) ([]string, bool) {
			ids, global, err := botservices.ResolveSharingScope(sessionID, "memory")
			if err != nil {
				return nil, false
			}
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			return list, global
		},
	}), nil
}

func startNativeAMemorixHost(holder *maibotcfg.InstanceHolder, db *gorm.DB, kernelFactory func() memorix.KernelFactory) (*memorix.AMemorixHostService, error) {
	service, err := newAMemorixHostService(holder, db, kernelFactory)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := service.Start(ctx); err != nil {
		return nil, fmt.Errorf("启动 A_Memorix 原生宿主失败: %w", err)
	}
	if err := service.WaitStartup(ctx); err != nil {
		stopNativeAMemorixHost(service)
		return nil, fmt.Errorf("等待 A_Memorix 原生宿主初始化失败: %w", err)
	}
	if state := service.State(); state != "ready" {
		stopNativeAMemorixHost(service)
		return nil, fmt.Errorf("A_Memorix 原生宿主未进入 ready: %s", state)
	}
	service.RegisterConfigReloadCallback()
	memorix.DefaultRegistry().SetHost(service)
	return service, nil
}

// nativeAMemorixEnabled 判断本次 native 尝试对应的 A_Memorix 启用状态
// auto 以启动门读取的 [a_memorix.plugin].enabled 为准；显式 native 以实例配置为准
func nativeAMemorixEnabled(plan nativeAMemorixStartupPlan, holder *maibotcfg.InstanceHolder) bool {
	if plan.Decision.Backend != memorixBackendNative {
		return plan.Decision.ConfigEnabled
	}
	if holder != nil {
		if cfg := holder.Config(); cfg != nil {
			return cfg.Bot.AMemorix.Plugin.Enabled
		}
	}
	// 实例配置未加载时，显式 native 仍按需要原生内核处理，失败保留真实错误
	return true
}

// registerDormantAMemorixHost 在 A_Memorix 未启用时注册休眠宿主
//
// 只有一个目的：让配置热重载能把内核拉起来。Python 无条件注册回调，
// 所以 WebUI 打开开关立即生效；不注册就只能重启进程。
// 内核未启动时 Invoke 走 disabledResponse，与 DisabledHost 语义一致。
func registerDormantAMemorixHost(plan nativeAMemorixStartupPlan, holder *maibotcfg.InstanceHolder, db *gorm.DB, kernelFactory func() memorix.KernelFactory) {
	if plan.Decision.Backend != memorixBackendNative && plan.Decision.Backend != memorixBackendAuto {
		return
	}
	if holder == nil || kernelFactory == nil || memorix.DefaultRegistry().Host() != (memorix.DisabledHost{}) {
		return
	}
	service, err := newAMemorixHostService(holder, db, kernelFactory)
	if err != nil {
		zap.L().Warn("A_Memorix 休眠宿主装配失败，热重载无法自动启用", zap.Error(err))
		return
	}
	service.RegisterConfigReloadCallback()
	memorix.DefaultRegistry().SetHost(service)
	zap.L().Info("A_Memorix 未启用，已注册休眠宿主等待配置开启")
}

// wireMemorixHostAfterNativeStart 在原生内核未就绪时注册 Host
// 未启用→DisabledHost，启用但启动失败→FailedHost
func wireMemorixHostAfterNativeStart(plan nativeAMemorixStartupPlan, start nativeAMemorixStart) {
	backend, explicit := start.Backend, start.Explicit
	if start.Err != nil {
		if !nativeAMemorixEnabled(plan, start.Holder) && explicit {
			memorix.DefaultRegistry().SetHost(memorix.DisabledHost{})
			zap.L().Error("A_Memorix 原生内核启动失败，已按显式配置保持未启用", zap.Error(start.Err))
			return
		}
		// 启用但内核失败：保留初始化失败状态
		memorix.DefaultRegistry().SetHost(memorix.NewFailedHost(start.Err.Error(), nil))
		zap.L().Error("A_Memorix 原生内核启动失败，Host 保持初始化失败状态",
			zap.String("backend", backend), zap.Error(start.Err))
		return
	}
	// 未进入 ready 且没有错误：保持未启用
	memorix.DefaultRegistry().SetHost(memorix.DisabledHost{})
	if explicit && backend != memorixBackendAuto {
		zap.L().Warn("显式 native 后端未进入 ready，保持未启用")
		return
	}
	zap.L().Info("Memorix Host 未配置（DisabledHost）")
}

// stopNativeAMemorixHost 关闭宿主并释放内核，用于退出或启动失败回滚
func stopNativeAMemorixHost(service *memorix.AMemorixHostService) {
	if service == nil {
		return
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := service.Stop(stopCtx); err != nil {
		zap.L().Warn("关闭 A_Memorix 原生内核失败", zap.Error(err))
	}
}

// startScheduleService 装配聊天日程：加载待办存储，按相交组定时投递内部任务
func startScheduleService(
	instanceDir string,
	db *gorm.DB,
	agent *maisaruntime.Agent,
	pet *desktoppet.Service,
	prompts *prompt.Manager,
) {
	if db == nil {
		return
	}
	hooks := schedules.Hooks{
		OwnerCheck: func(ctx context.Context, sessionID string) bool {
			var record chatsession.Record
			if err := db.WithContext(ctx).Where("session_id = ?", sessionID).First(&record).Error; err != nil {
				return false
			}
			return scheduleIsOwnerRecord(record)
		},
		DesktopReady: func() bool {
			return pet != nil && pet.Status().Ready
		},
		DesktopReminder: func(ctx context.Context, text string) error {
			if pet == nil {
				return fmt.Errorf("桌宠服务未初始化")
			}
			if _, err := pet.Control(ctx, "show", "", false); err != nil {
				return err
			}
			_, err := pet.Control(ctx, "say", "⏰ 提醒："+text, false)
			return err
		},
		RefreshDesktop: func() {
			if pet == nil || !pet.Status().Ready {
				return
			}
			refreshCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := pet.RefreshTodos(refreshCtx); err != nil {
				zap.L().Debug("刷新桌宠待办失败", zap.Error(err))
			}
		},
		Dispatch: func(ctx context.Context, item map[string]any) error {
			return dispatchScheduleItem(ctx, db, agent, prompts, item)
		},
	}
	path := filepath.Join(instanceDir, "data", "plugins", "sengokucola.desktop-pet", "todos.json")
	if err := schedules.Default().Start(context.Background(), path, hooks); err != nil {
		zap.L().Error("启动日程服务失败", zap.Error(err))
	}
}

// dispatchScheduleItem 解析日程所属真实聊天流并投递内部任务
func dispatchScheduleItem(
	ctx context.Context,
	db *gorm.DB,
	agent *maisaruntime.Agent,
	prompts *prompt.Manager,
	item map[string]any,
) error {
	if agent == nil || prompts == nil {
		return fmt.Errorf("日程投递依赖未就绪")
	}
	sessionID := strings.TrimSpace(anyString(item["session_id"]))
	var record chatsession.Record
	if sessionID != "" {
		if err := db.WithContext(ctx).Where("session_id = ?", sessionID).First(&record).Error; err != nil {
			return fmt.Errorf("日程所属聊天流已不存在")
		}
	} else {
		kind, targetID, resolveErr := resolveOwnerAgentTarget(ctx, db)
		if resolveErr != nil {
			return resolveErr
		}
		payload, err := sonic.Marshal(map[string]any{
			"id": anyString(item["id"]), "text": anyString(item["text"]), "due_at": item["due_at"],
		})
		if err != nil {
			return err
		}
		text, err := prompts.Render("schedule_due", map[string]string{"schedule": string(payload)})
		if err != nil {
			return err
		}
		_, err = agent.EnqueueInternalTask(ctx, kind, targetID, strings.TrimSpace(text), "schedule", "日程到期")
		return err
	}
	kind, targetID, ok := scheduleConversationTarget(record)
	if !ok {
		return fmt.Errorf("当前聊天平台暂不支持日程投递")
	}
	payload, err := sonic.Marshal(map[string]any{
		"id": anyString(item["id"]), "text": anyString(item["text"]), "due_at": item["due_at"],
	})
	if err != nil {
		return err
	}
	text, err := prompts.Render("schedule_due", map[string]string{"schedule": string(payload)})
	if err != nil {
		return err
	}
	_, err = agent.EnqueueInternalTask(ctx, kind, targetID, strings.TrimSpace(text), "schedule", "日程到期")
	return err
}

func scheduleIsOwnerRecord(record chatsession.Record) bool {
	if record.Platform != internalchat.Platform || record.IsGroup() || record.UserID == nil {
		return false
	}
	userID := strings.TrimSpace(*record.UserID)
	return userID == internalchat.OwnerUserID || userID == strconv.FormatInt(internalchat.OwnerNumericID, 10)
}

func scheduleConversationTarget(record chatsession.Record) (string, int64, bool) {
	if record.IsGroup() {
		if record.GroupID == nil {
			return "", 0, false
		}
		targetID, err := strconv.ParseInt(strings.TrimSpace(*record.GroupID), 10, 64)
		if err != nil || targetID <= 0 {
			return "", 0, false
		}
		return memory.ConversationKindGroup, targetID, true
	}
	if scheduleIsOwnerRecord(record) {
		return memory.ConversationKindPrivate, internalchat.OwnerNumericID, true
	}
	if record.UserID == nil {
		return "", 0, false
	}
	targetID, err := strconv.ParseInt(strings.TrimSpace(*record.UserID), 10, 64)
	if err != nil || targetID <= 0 {
		return "", 0, false
	}
	return memory.ConversationKindPrivate, targetID, true
}

// resolveOwnerAgentTarget 解析主人内部私聊对应的 Agent 会话目标
func resolveOwnerAgentTarget(ctx context.Context, db *gorm.DB) (string, int64, error) {
	ownerID := strconv.FormatInt(internalchat.OwnerNumericID, 10)
	var record chatsession.Record
	if err := db.WithContext(ctx).
		Where("platform = ? AND (group_id IS NULL OR group_id = '') AND (user_id = ? OR user_id = ?)",
			internalchat.Platform, internalchat.OwnerUserID, ownerID).
		First(&record).Error; err != nil {
		return "", 0, fmt.Errorf("无法确定主人聊天流，请先建立唯一的主人会话")
	}
	kind, targetID, ok := scheduleConversationTarget(record)
	if !ok {
		return "", 0, fmt.Errorf("主人聊天流缺少可投递身份")
	}
	return kind, targetID, nil
}

// resolveOwnerSessionID 解析唯一的主人内部私聊 session_id
func resolveOwnerSessionID(ctx context.Context, db *gorm.DB) (string, error) {
	ownerID := strconv.FormatInt(internalchat.OwnerNumericID, 10)
	var records []chatsession.Record
	if err := db.WithContext(ctx).
		Where("platform = ? AND (group_id IS NULL OR group_id = '') AND (user_id = ? OR user_id = ?)",
			internalchat.Platform, internalchat.OwnerUserID, ownerID).
		Limit(2).Find(&records).Error; err != nil {
		return "", err
	}
	if len(records) != 1 {
		return "", fmt.Errorf("请先在桌宠或网页聊天中建立唯一的主人会话")
	}
	return records[0].SessionID, nil
}
