package host

// 宿主维护：创建、克隆、归档 Bot，共享 Python 环境依赖和自更新。
// 对齐 src/host/maintenance.py。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"MaiGo/internal/common/self_update"
	"MaiGo/internal/common/version"
	pluginruntime "MaiGo/internal/plugin_runtime"
)

// HostMaintenance 串行执行会改动共享环境的维护操作
type HostMaintenance struct {
	supervisor               *Supervisor
	mu                       sync.Mutex
	depMu                    sync.Mutex
	busy                     atomic.Bool
	dependencyWG             sync.WaitGroup
	dependencyRunning        atomic.Bool
	updateRequested          atomic.Bool
	dependencyError          string
	failedDependencySnapshot string
}

// NewHostMaintenance 创建维护控制器
func NewHostMaintenance(supervisor *Supervisor) *HostMaintenance {
	return &HostMaintenance{supervisor: supervisor}
}

// DependencyError 最近一次自动依赖准备失败原因
func (m *HostMaintenance) DependencyError() string {
	m.depMu.Lock()
	defer m.depMu.Unlock()
	return m.dependencyError
}

// Busy 是否有维护操作正在执行
func (m *HostMaintenance) Busy() bool {
	return m.busy.Load() || m.updateRequested.Load()
}

// tryLock 获取维护权；失败时调用方返回“正在执行其他维护操作”
func (m *HostMaintenance) tryLock() (func(), error) {
	if !m.mu.TryLock() {
		return nil, errors.New("宿主正在执行其他维护操作")
	}
	if m.updateRequested.Load() {
		m.mu.Unlock()
		return nil, errors.New("宿主正在更新")
	}
	m.busy.Store(true)
	once := sync.Once{}
	return func() {
		once.Do(func() {
			m.busy.Store(false)
			m.mu.Unlock()
		})
	}, nil
}

// CreateBot 在维护锁内创建并初始化 Bot
func (m *HostMaintenance) CreateBot(name string) (BotRecord, error) {
	unlock, err := m.tryLock()
	if err != nil {
		return BotRecord{}, err
	}
	defer unlock()
	return CreateBot(m.supervisor.workspace, name)
}

// ManageBot 在维护锁内重命名、归档或克隆
func (m *HostMaintenance) ManageBot(botID, action, name string) (map[string]any, error) {
	unlock, err := m.tryLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.supervisor.Manage(botID, action, name)
}

// QueuePluginDependencies 在控制回执后异步准备插件依赖
func (m *HostMaintenance) QueuePluginDependencies() {
	if !m.dependencyRunning.CompareAndSwap(false, true) {
		return
	}
	m.dependencyWG.Add(1)
	go func() {
		defer m.dependencyWG.Done()
		defer m.dependencyRunning.Store(false)
		if _, err := m.InstallPluginDependencies(context.Background()); err != nil {
			m.depMu.Lock()
			m.dependencyError = err.Error()
			m.depMu.Unlock()
		}
	}()
}

// Finish 等待后台依赖准备结束，避免安装中途关闭宿主
func (m *HostMaintenance) Finish(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		m.dependencyWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// UpdateRoot 自更新检查的代码根目录
func (m *HostMaintenance) UpdateRoot() string {
	return maintenanceRoot()
}

func maintenanceRoot() string {
	if explicit := strings.TrimSpace(os.Getenv("MAIBOT_SOURCE_ROOT")); explicit != "" {
		return filepath.Clean(explicit)
	}
	if working, err := os.Getwd(); err == nil {
		for current := filepath.Clean(working); ; current = filepath.Dir(current) {
			if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
				return current
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
	}
	if executable, err := os.Executable(); err == nil {
		for current := filepath.Dir(executable); ; current = filepath.Dir(current) {
			if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
				return current
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
	}
	return workspaceRootFallback()
}

func workspaceRootFallback() string {
	if root := strings.TrimSpace(os.Getenv(InstanceDirEnv)); root != "" {
		return filepath.Clean(root)
	}
	if working, err := os.Getwd(); err == nil {
		return working
	}
	return "."
}

// CheckUpdate 联网检查新提交
func (m *HostMaintenance) CheckUpdate(ctx context.Context) (selfupdate.UpdateCheck, error) {
	return selfupdate.DefaultManager().Check(ctx, selfupdate.CheckOptions{
		Root: m.UpdateRoot(), CurrentVersion: version.String(), Force: true,
	})
}

// UpdateStatus 最近一次更新结果与本地状态
func (m *HostMaintenance) UpdateStatus(ctx context.Context) map[string]any {
	root := m.UpdateRoot()
	local, err := selfupdate.InspectLocal(ctx, root, version.String())
	payload := map[string]any{"busy": m.Busy(), "local": local}
	if err != nil {
		payload["local_error"] = err.Error()
	}
	if result, resultErr := selfupdate.ReadLastResult(root); resultErr == nil && result != nil {
		payload["last_result"] = result
	} else {
		payload["last_result"] = nil
	}
	return payload
}

// PrepareUpdate 登记更新并停止全部 Bot；调用方随后关闭宿主
func (m *HostMaintenance) PrepareUpdate(target string) (map[string]any, error) {
	unlock, err := m.tryLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	root := m.UpdateRoot()
	lease := selfupdate.NewInstanceLease(root)
	if err := lease.Register(); err != nil {
		return nil, err
	}
	if err := lease.EnsureOtherInstancesStopped(); err != nil {
		return nil, err
	}
	if _, err := selfupdate.RequestUpdate(context.Background(), target, selfupdate.RequestOptions{
		Root: root, CurrentVersion: version.String(),
	}); err != nil {
		return nil, err
	}
	if err := m.supervisor.EnterMaintenance(); err != nil {
		_ = selfupdate.ClearPendingUpdate(root)
		return nil, err
	}
	m.updateRequested.Store(true)
	return map[string]any{"status": "restarting", "target": target}, nil
}

// TransferData 停止目标 Bot，在实例锁内执行数据事务；结束后按期望状态恢复
func (m *HostMaintenance) TransferData(botID string, operation func() error) error {
	unlock, err := m.tryLock()
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			unlock()
		}
	}()
	if err := m.supervisor.RunFileOperation(botID, operation); err != nil {
		return err
	}
	unlock()
	released = true
	bot, err := m.supervisor.workspace.Registry.GetBot(botID)
	if err != nil {
		return err
	}
	journal := importJournalPath(bot)
	if bot.DesiredState == "running" {
		if _, err := os.Stat(journal); os.IsNotExist(err) {
			if _, startErr := m.supervisor.Start(botID); startErr != nil {
				return fmt.Errorf("数据传输已完成，但恢复 Bot 失败: %w", startErr)
			}
		}
	}
	return nil
}

// PluginDependencyReport 汇总全部 Bot 的依赖声明
type PluginDependencyReport struct {
	Bots            []map[string]any `json:"bots"`
	Requirements    []map[string]any `json:"requirements"`
	AllRequirements []map[string]any `json:"all_requirements"`
	Conflicts       []string         `json:"conflicts"`
}

// AnalyzePluginDependencies 只读汇总所有已初始化 Bot 的完整插件依赖声明
func (m *HostMaintenance) AnalyzePluginDependencies(ctx context.Context) (PluginDependencyReport, error) {
	bots, err := m.supervisor.workspace.Registry.ListBots()
	if err != nil {
		return PluginDependencyReport{}, err
	}
	report := PluginDependencyReport{Bots: []map[string]any{}, Requirements: []map[string]any{}, AllRequirements: []map[string]any{}, Conflicts: []string{}}
	merged := map[string]*pluginruntime.PluginRequirement{}
	for _, bot := range bots {
		if bot.Initialization != "initialized" {
			continue
		}
		dirs := pluginDirsForBot(bot.Directory)
		analysis, err := pluginruntime.AnalyzePluginRequirements(ctx, maintenanceRoot(), dirs)
		if err != nil {
			return PluginDependencyReport{}, err
		}
		names := make([]string, 0, len(analysis.Requirements))
		for _, item := range analysis.Requirements {
			names = append(names, item.Requirement)
			entry := merged[item.Requirement]
			if entry == nil {
				copyItem := item
				entry = &copyItem
				entry.PluginIDs = nil
				merged[item.Requirement] = entry
			}
			for _, pluginID := range item.PluginIDs {
				origin := bot.Name + " (" + bot.BotID + ") / " + pluginID
				entry.PluginIDs = append(entry.PluginIDs, origin)
				if item.InstalledVersion != nil && !item.Satisfied {
					report.Conflicts = append(report.Conflicts, origin+" 要求 "+item.Requirement+"，当前安装 "+*item.InstalledVersion)
				}
			}
		}
		report.Bots = append(report.Bots, map[string]any{"bot_id": bot.BotID, "name": bot.Name, "requirements": names})
	}
	for _, entry := range merged {
		sort.Strings(entry.PluginIDs)
		entry.PluginIDs = uniqueStringValues(entry.PluginIDs)
		payload := map[string]any{
			"requirement": entry.Requirement, "package": entry.PackageName, "version_spec": entry.VersionSpec,
			"satisfied": entry.Satisfied, "installed_version": entry.InstalledVersion, "plugin_ids": entry.PluginIDs,
		}
		report.AllRequirements = append(report.AllRequirements, payload)
		if entry.InstalledVersion == nil {
			report.Requirements = append(report.Requirements, payload)
		}
	}
	sort.Slice(report.AllRequirements, func(i, j int) bool {
		return report.AllRequirements[i]["requirement"].(string) < report.AllRequirements[j]["requirement"].(string)
	})
	sort.Slice(report.Requirements, func(i, j int) bool {
		return report.Requirements[i]["requirement"].(string) < report.Requirements[j]["requirement"].(string)
	})
	return report, nil
}

// InstallPluginDependencies 停止全部 Bot 后安装共享环境缺失依赖
func (m *HostMaintenance) InstallPluginDependencies(ctx context.Context) (map[string]any, error) {
	return m.installPluginDependencies(ctx, false)
}

// InstallPluginDependenciesAutomatic 启动流程自动补装；相同失败声明只自动尝试一次
func (m *HostMaintenance) InstallPluginDependenciesAutomatic(ctx context.Context) (map[string]any, error) {
	return m.installPluginDependencies(ctx, true)
}

func (m *HostMaintenance) installPluginDependencies(ctx context.Context, automatic bool) (result map[string]any, err error) {
	unlock, err := m.tryLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	report, err := m.AnalyzePluginDependencies(ctx)
	if err != nil {
		return nil, err
	}
	snapshotBytes, _ := json.Marshal(report.Bots)
	snapshot := string(snapshotBytes)
	if automatic {
		m.depMu.Lock()
		failed := m.failedDependencySnapshot
		message := m.dependencyError
		m.depMu.Unlock()
		if failed == snapshot && (len(report.Requirements) > 0 || len(report.Conflicts) > 0) {
			if message == "" {
				message = "插件依赖准备失败"
			}
			return nil, errors.New(message)
		}
	}
	defer func() {
		m.depMu.Lock()
		if err != nil {
			m.failedDependencySnapshot = snapshot
			m.dependencyError = err.Error()
		} else {
			m.failedDependencySnapshot = ""
			m.dependencyError = ""
		}
		m.depMu.Unlock()
	}()
	if len(report.Conflicts) > 0 {
		return nil, errors.New("插件依赖与共享环境冲突，未修改环境：\n" + strings.Join(report.Conflicts, "\n"))
	}
	if len(report.Requirements) == 0 {
		return map[string]any{"installed": false, "bots": report.Bots, "requirements": report.Requirements, "conflicts": report.Conflicts}, nil
	}
	if err := m.supervisor.EnterMaintenance(); err != nil {
		return nil, err
	}
	installed := false
	defer m.supervisor.LeaveMaintenance()
	// 停止期间插件可能变化，安装前重新读取
	current, err := m.AnalyzePluginDependencies(ctx)
	if err != nil {
		return nil, err
	}
	if len(current.Requirements) != len(report.Requirements) {
		return nil, errors.New("插件依赖发生变化，已取消安装，请重新检查")
	}
	bots, err := m.supervisor.workspace.Registry.ListBots()
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(bots))
	for _, bot := range bots {
		if bot.Initialization == "initialized" {
			dirs = append(dirs, pluginDirsForBot(bot.Directory)...)
		}
	}
	changed, message, err := pluginruntime.InstallMissingRequirements(ctx, maintenanceRoot(), dirs)
	if err != nil {
		return nil, err
	}
	if !changed && message != "" {
		return nil, errors.New(message)
	}
	installed = changed
	verified, err := m.AnalyzePluginDependencies(ctx)
	if err != nil {
		return nil, err
	}
	if len(verified.Requirements) > 0 || len(verified.Conflicts) > 0 {
		return nil, errors.New("安装后依赖检查未通过")
	}
	return map[string]any{"installed": installed, "bots": verified.Bots, "requirements": verified.Requirements, "conflicts": verified.Conflicts}, nil
}

func pluginDirsForBot(directory string) []string {
	dirs := make([]string, 0, 2)
	if builtin := filepath.Join(maintenanceRoot(), "plugins", "built_in"); directoryExists(builtin) {
		dirs = append(dirs, builtin)
	}
	if thirdParty := filepath.Join(directory, "plugins"); directoryExists(thirdParty) {
		dirs = append(dirs, thirdParty)
	}
	return dirs
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func uniqueStringValues(values []string) []string {
	if len(values) < 2 {
		return values
	}
	kept := values[:1]
	for _, value := range values[1:] {
		if value != kept[len(kept)-1] {
			kept = append(kept, value)
		}
	}
	return kept
}
