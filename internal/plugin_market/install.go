package pluginmarket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

const (
	releaseReceiptName    = ".maibot-release.json"
	updateTmpDirectory    = ".update_tmp"
	updateBackupDirectory = ".update_backups"
)

// ReadReleaseReceipt 对齐 release_install.read_release_receipt
func ReadReleaseReceipt(pluginPath string) (map[string]any, error) {
	receiptPath, err := resolvePluginFilePath(pluginPath, releaseReceiptName, true)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var decoded any
	if err := sonic.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	receipt, ok := decoded.(map[string]any)
	if !ok {
		return nil, conflictError("插件安装记录损坏，请检查 .maibot-release.json")
	}
	return receipt, nil
}

// InstallRelease 对齐 release_install.install_release：失败时上报 error 进度
func (s *Service) InstallRelease(ctx context.Context, pluginID string, entry *PluginReleaseEntry, release *PluginRelease, updating, automatic bool, mirrorID string, force bool, expectedCommit *string) (InstallResult, error) {
	operation := "install"
	if updating {
		operation = "update"
	}
	result, err := s.installRelease(ctx, pluginID, entry, release, updating, automatic, mirrorID, force, expectedCommit)
	if err != nil {
		message := err.Error()
		if marketErr, ok := AsMarketError(err); ok {
			message = marketErr.Message
		}
		s.reportProgressError(operation, pluginID, "插件版本安装失败", message)
		return InstallResult{}, err
	}
	return result, nil
}

// installRelease 对齐 release_install._install_release
func (s *Service) installRelease(ctx context.Context, pluginID string, entry *PluginReleaseEntry, release *PluginRelease, updating, automatic bool, mirrorID string, force bool, expectedCommit *string) (InstallResult, error) {
	operation := "install"
	if updating {
		operation = "update"
	}
	if force && (expectedCommit == nil || release.Commit != *expectedCommit) {
		return InstallResult{}, conflictError("目标版本已变化，请重新发起更新并确认")
	}
	canonicalID := pluginID
	if entry.ManifestID != nil && *entry.ManifestID != "" {
		canonicalID = *entry.ManifestID
	}
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return InstallResult{}, err
	}
	existing, err := FindInstalledPlugin(pluginsRoot, canonicalID)
	if err != nil {
		return InstallResult{}, err
	}
	if updating && existing == "" {
		return InstallResult{}, notFoundError("插件未安装")
	}
	if !updating && existing != "" {
		return InstallResult{}, conflictError("插件已安装")
	}
	target, oldFormat, err := CandidatePaths(pluginsRoot, canonicalID)
	if err != nil {
		return InstallResult{}, err
	}
	if existing != "" {
		target = existing
	} else if pathExists(target) || pathExists(oldFormat) {
		return InstallResult{}, conflictError("插件目标目录已存在，请先处理现有目录")
	}
	var oldVersion *string
	if existing != "" {
		oldManifest, err := LoadManifestJSON(filepath.Join(target, "_manifest.json"))
		if err != nil {
			return InstallResult{}, err
		}
		if oldManifest != nil {
			if version := safeString(oldManifest["version"]); version != "" {
				oldVersion = &version
			}
		}
		// 安装记录损坏时在动文件之前报错
		if _, err := ReadReleaseReceipt(target); err != nil {
			return InstallResult{}, err
		}
	}
	if automatic && oldVersion != nil {
		comparison := compareReleaseVersions(release.Version, *oldVersion)
		if comparison < 0 || (comparison == 0 && !force) {
			return InstallResult{}, conflictError("当前已是最新兼容版本，不会自动降级或重装")
		}
	}
	tmpDirectory, err := workDirectory(filepath.Dir(target), updateTmpDirectory)
	if err != nil {
		return InstallResult{}, err
	}
	backupDirectory, err := workDirectory(filepath.Dir(target), updateBackupDirectory)
	if err != nil {
		return InstallResult{}, err
	}
	candidate := filepath.Join(tmpDirectory, filepath.Base(target)+"."+randomHex(16))
	backup := filepath.Join(backupDirectory, filepath.Base(target)+"."+randomHex(16))
	defer func() {
		if pathExists(candidate) {
			if removeErr := RemoveTree(candidate); removeErr != nil {
				zap.L().Warn("清理插件候选目录失败", zap.String("path", candidate), zap.Error(removeErr))
			}
		}
	}()

	s.reportStep(operation, pluginID, 15, "下载发布版本 "+release.Version)
	_, owner, repo, err := parseRepositoryURL(entry.RepositoryURL)
	if err != nil {
		return InstallResult{}, err
	}
	clone := s.cloneRepository(ctx, owner, repo, candidate, release.Tag, "", 1, operation, pluginID)
	if !clone.Success {
		return InstallResult{}, &MarketError{StatusCode: 502, Message: clone.Error}
	}
	// 文件校验与替换一旦开始就收尾完成，避免请求取消后留下半更新的插件目录。
	commitCtx := context.WithoutCancel(ctx)
	warnings, err := s.validateCandidate(commitCtx, candidate, entry, release)
	if err != nil {
		return InstallResult{}, err
	}
	if existing != "" {
		if err := s.checkExistingCode(commitCtx, target, candidate, force); err != nil {
			return InstallResult{}, err
		}
	}
	s.reportStep(operation, pluginID, 85, "文件校验通过，等待其他插件操作或运行环境重启完成...")

	swapped := false
	stopped := []string{}
	replaceAndReload := func() error {
		if existing != "" {
			s.reportStep(operation, pluginID, 88, "正在检查已安装插件目录...")
			if err := checkTree(target); err != nil {
				return err
			}
			s.reportStep(operation, pluginID, 90, "正在停止旧版本插件，等待插件退出...")
			unloaded, err := s.unloadRuntime(commitCtx, canonicalID)
			if err != nil {
				return err
			}
			stopped = unloaded
			s.reportStep(operation, pluginID, 92, "正在保留插件配置和本地数据...")
			if !force {
				if err := preserveUserFiles(commitCtx, target, candidate); err != nil {
					return err
				}
			}
		}
		s.reportStep(operation, pluginID, 95, "正在替换插件版本文件...")
		if err := s.replaceFiles(commitCtx, candidate, target, backup, existing != "", force, canonicalID, entry, release); err != nil {
			return err
		}
		swapped = true
		if existing == "" {
			// 整个目录移入时可能没有源码事件，首次安装必须主动同步运行时
			s.reportStep(operation, pluginID, 98, "正在同步插件依赖并加载插件...")
			if err := s.activateRuntime(commitCtx, canonicalID); err != nil {
				return &MarketError{StatusCode: 409, Message: "插件文件已安装，但运行时加载失败：" + err.Error()}
			}
		}
		return nil
	}

	failure := replaceAndReload()
	if len(stopped) > 0 {
		s.reportStep(operation, pluginID, 98, "正在加载新版本插件...")
		resumeError := s.resumeRuntime(commitCtx, stopped)
		if failure == nil && resumeError != nil {
			failure = resumeError
		} else if failure != nil && resumeError != nil {
			detail := failure.Error()
			if marketErr, ok := AsMarketError(failure); ok {
				detail = marketErr.Message
			}
			failure = &MarketError{StatusCode: 500, Message: fmt.Sprintf(
				"版本替换失败：%s；恢复插件运行也失败：%s", detail, resumeError.Error())}
		}
	}
	if failure != nil {
		return InstallResult{}, failure
	}

	message := "已安装发布版本 " + release.Version
	if len(warnings) > 0 {
		message += "；依赖提醒：" + strings.Join(warnings, "；")
	}
	s.reportSuccess(operation, pluginID, message)
	result := InstallResult{
		Success:    swapped,
		Message:    "插件版本安装成功",
		PluginID:   canonicalID,
		PluginName: safeString(release.Manifest["name"]),
		Version:    release.Version,
		OldVersion: oldVersion,
		NewVersion: release.Version,
		Commit:     release.Commit,
		UpdateMode: "release",
		Warnings:   warnings,
	}
	if existing != "" {
		result.BackupPath = &backup
	}
	return result, nil
}

// replaceFiles 对齐 replace_files：写入安装记录并原子替换目录
func (s *Service) replaceFiles(ctx context.Context, candidate, target, backup string, existing, force bool, canonicalID string, entry *PluginReleaseEntry, release *PluginRelease) error {
	receipt := map[string]any{
		"plugin_id":      canonicalID,
		"version":        release.Version,
		"tag":            release.Tag,
		"commit":         release.Commit,
		"repository_url": entry.RepositoryURL,
	}
	raw, err := sonic.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	receiptPayload := append(raw, '\n')
	if existing && force {
		if err := s.replaceExistingCode(ctx, target, candidate, backup, force); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, releaseReceiptName), receiptPayload, 0o644)
	}
	if err := os.WriteFile(filepath.Join(candidate, releaseReceiptName), receiptPayload, 0o644); err != nil {
		return err
	}
	if existing {
		if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
			return err
		}
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	} else if pathExists(target) {
		return conflictError("插件目标目录已存在")
	}
	if err := os.Rename(candidate, target); err != nil {
		if existing {
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				zap.L().Error("恢复插件原目录失败", zap.String("path", target), zap.Error(restoreErr))
			}
		}
		return err
	}
	return nil
}

// checkExistingCode 对齐 check_existing_code：检测已安装插件代码冲突，强制更新时允许覆盖
func (s *Service) checkExistingCode(ctx context.Context, target, candidate string, force bool) error {
	if !isGitRepository(target) {
		return nil
	}
	commitOutput, err := runPluginGit(ctx, candidate, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(commitOutput)
	candidateResolved, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	if _, err := runPluginGit(ctx, target, "fetch", "--no-tags", candidateResolved, commit); err != nil {
		return err
	}
	conflict, err := updateConflict(ctx, target, commit)
	if err != nil {
		return err
	}
	if conflict != nil && !force {
		return conflictPayloadError(conflict)
	}
	return nil
}

// replaceExistingCode 对齐 replace_existing_code：只备份插件代码与配置，原地保留配置和数据
func (s *Service) replaceExistingCode(ctx context.Context, target, candidate, backup string, force bool) error {
	if err := checkTree(target); err != nil {
		return err
	}
	if err := s.checkExistingCode(ctx, target, candidate, force); err != nil {
		return err
	}
	if !isGitRepository(target) {
		return badRequestError("无 Git 安装不需要处理本地提交冲突")
	}
	commitOutput, err := runPluginGit(ctx, candidate, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(commitOutput)
	if err := backupCode(ctx, target, backup, commit); err != nil {
		return err
	}
	if err := replaceCode(ctx, target, commit); err != nil {
		if restoreErr := restoreCode(ctx, target, backup); restoreErr != nil {
			zap.L().Error("恢复插件代码备份失败", zap.String("path", target), zap.Error(restoreErr))
		}
		return err
	}
	return nil
}

// conflictPayloadError 把代码冲突作为结构化 409 返回给前端确认流程
func conflictPayloadError(conflict *CodeConflict) error {
	return &MarketError{StatusCode: 409, Message: conflict.Message, Payload: conflict}
}

// unloadRuntime 对齐 _stop_runtime
func (s *Service) unloadRuntime(ctx context.Context, pluginID string) ([]string, error) {
	coordinator := s.runtimeCoordinator()
	if coordinator == nil {
		return nil, nil
	}
	unloaded, err := coordinator.UnloadPlugins(ctx, []string{pluginID}, "release_update")
	if err != nil {
		return nil, &MarketError{StatusCode: 409, Message: "停止插件失败：" + err.Error()}
	}
	return unloaded, nil
}

// activateRuntime 对齐 activate_installed_plugin
func (s *Service) activateRuntime(ctx context.Context, pluginID string) error {
	coordinator := s.runtimeCoordinator()
	if coordinator == nil {
		return nil
	}
	return coordinator.ActivateInstalledPlugin(ctx, pluginID)
}

// resumeRuntime 对齐 _resume_runtime
func (s *Service) resumeRuntime(ctx context.Context, pluginIDs []string) error {
	if len(pluginIDs) == 0 {
		return nil
	}
	coordinator := s.runtimeCoordinator()
	if coordinator == nil {
		return nil
	}
	success, err := coordinator.ReloadPluginsGlobally(ctx, pluginIDs, "release_update")
	if err != nil {
		return err
	}
	if !success {
		return &MarketError{StatusCode: 409, Message: "版本文件已替换，但插件重新加载失败，请检查插件运行日志"}
	}
	return nil
}

// validateCandidate 对齐 _validate_candidate
func (s *Service) validateCandidate(ctx context.Context, candidate string, entry *PluginReleaseEntry, release *PluginRelease) ([]string, error) {
	if err := checkTree(candidate); err != nil {
		return nil, err
	}
	head, err := runPluginGit(ctx, candidate, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(head) != release.Commit {
		return nil, conflictError("Tag 当前指向的 commit 与版本索引不一致，已停止安装")
	}
	manifestPath, err := resolvePluginFilePath(candidate, "_manifest.json", true)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifestJSON(manifestPath)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(manifest, release.Manifest) {
		return nil, conflictError("下载的 manifest 与版本索引不一致")
	}
	parsed, manifestErrors, _ := LoadManifestFromPluginPath(candidate, DefaultMaibotVersion(), true)
	if parsed == nil {
		return nil, badRequestError("%s", strings.Join(manifestErrors, "；"))
	}
	if entry.ManifestID == nil || parsed.ID != *entry.ManifestID {
		return nil, badRequestError("下载的插件 ID 与索引不一致")
	}
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	directories, err := iterPluginDirectories(pluginsRoot)
	if err != nil {
		return nil, err
	}
	available := map[string]string{}
	for _, directory := range directories {
		installed, loadErr := LoadManifestJSON(filepath.Join(directory, "_manifest.json"))
		if loadErr != nil || installed == nil {
			continue
		}
		id, idOK := installed["id"].(string)
		version, versionOK := installed["version"].(string)
		if idOK && versionOK {
			available[id] = version
		}
	}
	missing := GetUnsatisfiedPluginDependencies(parsed, available)
	if len(missing) > 0 {
		return nil, badRequestError("插件依赖不满足：%s", strings.Join(missing, "；"))
	}
	// 其他已安装插件的版本约束仅作提醒，不阻止用户切换基础插件的版本
	warnings := []string{}
	for _, directory := range directories {
		installed, loadErr := LoadManifestJSON(filepath.Join(directory, "_manifest.json"))
		if loadErr != nil || installed == nil {
			continue
		}
		if safeInt(installed["manifest_version"], 0) != 2 || safeString(installed["id"]) == parsed.ID {
			continue
		}
		declarations, ok := anyList(installed["dependencies"])
		if !ok {
			continue
		}
		for _, item := range declarations {
			declaration, ok := anyMap(item)
			if !ok || safeString(declaration["type"]) != "plugin" || safeString(declaration["id"]) != parsed.ID {
				continue
			}
			dependency := PluginDependency{
				ID:          safeString(declaration["id"]),
				VersionSpec: safeString(declaration["version_spec"]),
			}
			if _, specErr := parseVersionSpecifiers(dependency.VersionSpec); specErr != nil {
				continue
			}
			if !IsPluginDependencySatisfied(dependency, parsed.Version) {
				warnings = append(warnings, fmt.Sprintf(
					"已安装插件 %s（%s）要求 %s 版本 %s，本次安装 %s，该插件可能无法运行",
					safeString(installed["name"]), safeString(installed["id"]), parsed.ID,
					dependency.VersionSpec, parsed.Version))
			}
		}
	}
	return warnings, nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func randomHex(length int) string {
	buffer := make([]byte, (length+1)/2)
	if _, err := rand.Read(buffer); err != nil {
		return strings.Repeat("0", length)
	}
	return hex.EncodeToString(buffer)[:length]
}
