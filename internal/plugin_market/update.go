package pluginmarket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// UpdateRequest 对齐 routers/plugin/schemas.UpdatePluginRequest
type UpdateRequest struct {
	Force          bool
	ExpectedCommit *string
	PluginID       string
	RepositoryURL  string
	Branch         *string
	MirrorID       string
	Version        *string
}

// pullOutcome 对齐 GitMirrorService.pull_repository 的返回结构
type pullOutcome struct {
	Success    bool
	Error      string
	Payload    any
	StatusCode int
	Branch     string
	MirrorUsed string
	BackupPath string
	Commands   []string
}

const (
	// updateGitTimeout 对齐 _run_git_process 的 300 秒超时
	updateGitTimeout = 300 * time.Second
	// defaultUpdateBranch 对齐 UpdatePluginRequest.branch 的默认值
	defaultUpdateBranch = "main"
)

// Update 对齐 management.update_plugin：发布版本安装走 InstallRelease，Git 插件拉取，非 Git 插件重克隆替换
func (s *Service) Update(ctx context.Context, request UpdateRequest) (map[string]any, error) {
	pluginID := strings.TrimSpace(request.PluginID)
	if err := ValidatePluginID(pluginID); err != nil {
		return nil, err
	}
	if request.Version != nil {
		entry, release, err := s.ResolveRelease(ctx, pluginID, *request.Version)
		if err != nil {
			return nil, err
		}
		if release != nil {
			result, err := s.InstallRelease(ctx, pluginID, entry, release, true, *request.Version == "latest", request.MirrorID, request.Force, request.ExpectedCommit)
			if err != nil {
				return nil, err
			}
			return result.Payload(), nil
		}
		request.RepositoryURL = entry.RepositoryURL
	}
	s.reportStep("update", pluginID, 5, "开始更新插件: "+pluginID)
	pluginsRoot, err := ResolvePluginsDir(s.instanceDirectory())
	if err != nil {
		return nil, err
	}
	pluginPath, err := FindInstalledPlugin(pluginsRoot, pluginID)
	if err != nil {
		return nil, err
	}
	if pluginPath == "" {
		s.reportProgressError("update", pluginID, "插件不存在", "插件未安装，请先安装")
		return nil, notFoundError("插件未安装")
	}
	manifest, err := requiredManifest(pluginPath)
	if err != nil {
		return nil, err
	}
	receipt, err := ReadReleaseReceipt(pluginPath)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		return nil, conflictError("该插件按发布版本安装，请通过版本选择更新，不能使用分支拉取")
	}
	oldVersion := manifestVersionText(manifest)
	oldManifestID, err := requireManifestPluginID(manifest)
	if err != nil {
		return nil, err
	}
	s.reportStep("update", pluginID, 10, "当前版本: "+oldVersion+"，准备更新...")
	if !isGitRepository(pluginPath) {
		return s.updateNonGitPlugin(ctx, pluginID, pluginPath, oldManifestID, oldVersion, request)
	}

	s.reportStep("update", pluginID, 30, "正在通过 Git 拉取新版本...")
	sourceURL := strings.TrimSpace(request.RepositoryURL)
	if sourceURL == "" {
		sourceURL = updateSourceURL(manifest)
	}
	outcome := s.pullRepository(ctx, pluginPath, request.branchName(), sourceURL, request.Force, request.ExpectedCommit)
	if !outcome.Success {
		s.reportProgressError("update", pluginID, "Git 拉取新版本失败", outcome.Error)
		status := outcome.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return nil, &MarketError{StatusCode: status, Message: outcome.Error, Payload: outcome.Payload}
	}

	s.reportStep("update", pluginID, 90, "验证新版本...")
	manifestPath, err := resolvePluginFilePath(pluginPath, "_manifest.json", true)
	if err != nil {
		return nil, err
	}
	if !pathExists(manifestPath) {
		s.reportProgressError("update", pluginID, "新版本缺少 _manifest.json", "无效的插件格式")
		return nil, badRequestError("无效的插件：缺少 _manifest.json")
	}
	newManifest, err := validateUpdatedManifest(pluginPath, oldManifestID)
	if err != nil {
		s.reportProgressError("update", pluginID, "_manifest.json 无效", marketErrorDetail(err))
		return nil, err
	}
	newVersion := manifestVersionText(newManifest)
	newName := manifestNameText(newManifest, pluginID)
	s.reportSuccess("update", pluginID, fmt.Sprintf("成功更新 %s: %s → %s", newName, oldVersion, newVersion))
	return map[string]any{
		"success":     true,
		"message":     "插件更新成功",
		"plugin_id":   pluginID,
		"plugin_name": newName,
		"old_version": oldVersion,
		"new_version": newVersion,
		"update_mode": "git_pull",
		"backup_path": optionalEmptyString(outcome.BackupPath),
	}, nil
}

// optionalEmptyString 对齐 Python dict.get 的空值输出
func optionalEmptyString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// updateNonGitPlugin 对齐 _update_non_git_plugin：克隆到 .update_tmp，校验后备份并替换原目录
func (s *Service) updateNonGitPlugin(ctx context.Context, pluginID, pluginPath, oldManifestID, oldVersion string, request UpdateRequest) (map[string]any, error) {
	candidatePath, backupPath, err := updateWorkPaths(pluginPath, pluginID)
	if err != nil {
		return nil, err
	}
	oldMoved := false
	defer func() {
		if pathExists(candidatePath) {
			if removeErr := RemoveTree(candidatePath); removeErr != nil {
				zap.L().Warn("清理更新临时目录失败", zap.String("path", candidatePath), zap.Error(removeErr))
			}
		}
		if oldMoved && pathExists(backupPath) && !pathExists(pluginPath) {
			if renameErr := os.Rename(backupPath, pluginPath); renameErr != nil {
				zap.L().Error("更新失败后恢复插件目录失败", zap.String("path", pluginPath), zap.Error(renameErr))
			}
		}
	}()

	s.reportStep("update", pluginID, 30, "当前插件不是 Git 仓库，正在重新克隆新版本...")
	repoURL, owner, repo, err := parseRepositoryURL(request.RepositoryURL)
	if err != nil {
		return nil, err
	}
	var outcome cloneOutcome
	if strings.Contains(repoURL, "github.com") {
		outcome = s.cloneRepository(ctx, owner, repo, candidatePath, request.branchName(), "", 1, "update", pluginID)
	} else {
		outcome = s.cloneRepository(ctx, owner, repo, candidatePath, request.branchName(), repoURL, 1, "update", pluginID)
	}
	if !outcome.Success {
		status := outcome.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return nil, &MarketError{StatusCode: status, Message: outcome.Error}
	}

	s.reportStep("update", pluginID, 70, "正在校验新版本插件身份...")
	newManifest, err := validateUpdatedManifest(candidatePath, oldManifestID)
	if err != nil {
		return nil, err
	}
	if err := restoreKnownUserFiles(pluginPath, candidatePath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(backupPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(pluginPath, backupPath); err != nil {
		return nil, fmt.Errorf("更新插件失败: %w", err)
	}
	oldMoved = true
	if err := os.Rename(candidatePath, pluginPath); err != nil {
		return nil, fmt.Errorf("更新插件失败: %w", err)
	}

	newVersion := manifestVersionText(newManifest)
	newName := manifestNameText(newManifest, pluginID)
	s.reportSuccess("update", pluginID, fmt.Sprintf("成功更新 %s: %s → %s", newName, oldVersion, newVersion))
	return map[string]any{
		"success":     true,
		"message":     "插件更新成功",
		"plugin_id":   pluginID,
		"plugin_name": newName,
		"old_version": oldVersion,
		"new_version": newVersion,
		"update_mode": "reinstall_from_backup",
		"backup_path": backupPath,
	}, nil
}

// pullRepository 对齐 GitMirrorService.pull_repository：保留配置文件与未跟踪文件，只做快进更新
func (s *Service) pullRepository(ctx context.Context, repositoryPath, branch, remoteURL string, force bool, expectedCommit *string) pullOutcome {
	if info, err := os.Stat(repositoryPath); err != nil || !info.IsDir() {
		return pullOutcome{Error: "插件目录不存在", StatusCode: http.StatusNotFound}
	}
	if !isGitRepository(repositoryPath) {
		return pullOutcome{Error: "插件目录不是 Git 仓库，无法通过 Git 更新", StatusCode: http.StatusBadRequest}
	}

	if strings.TrimSpace(remoteURL) == "" {
		origin, _, getErr := runGitCommand(ctx, repositoryPath, updateGitTimeout, "remote", "get-url", "origin")
		if getErr == nil {
			candidate := strings.TrimSpace(origin)
			if parsed, parseErr := url.Parse(candidate); parseErr == nil && strings.EqualFold(parsed.Host, "github.com") {
				remoteURL = candidate
			}
		}
	}

	if strings.TrimSpace(remoteURL) != "" {
		parsed, parseErr := url.Parse(remoteURL)
		if parseErr != nil {
			return pullOutcome{Error: "仓库 URL 不合法", StatusCode: http.StatusBadRequest}
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			return pullOutcome{Error: "仓库 URL 不允许包含查询参数或片段", StatusCode: http.StatusBadRequest}
		}
		path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
		parts := strings.Split(path, "/")
		if strings.EqualFold(parsed.Scheme, "https") && strings.EqualFold(parsed.Host, "github.com") && len(parts) == 2 {
			return s.pullGitHubRepository(ctx, repositoryPath, branch, parts[0], parts[1], force, expectedCommit)
		}
	}

	commands := make([][]string, 0, 2)
	if strings.TrimSpace(remoteURL) != "" {
		validated, validateErr := s.validateDownloadURL(remoteURL, "自定义源")
		if validateErr != nil {
			return pullOutcome{Error: validateErr.Error(), StatusCode: http.StatusBadRequest}
		}
		remoteURL = validated
		commands = append(commands, []string{"remote", "set-url", "origin", remoteURL})
	}
	commands = append(commands, []string{"fetch", "origin", "--prune"})

	executed := make([]string, 0, len(commands))
	for _, args := range commands {
		executed = append(executed, "git "+strings.Join(args, " "))
		stdout, stderrText, runErr := runGitCommand(ctx, repositoryPath, updateGitTimeout, args...)
		if runErr == nil {
			continue
		}
		if errors.Is(runErr, errGitNotFound) {
			return pullOutcome{Error: "Git 未安装或不在 PATH 中", Commands: executed}
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return pullOutcome{Error: "Git 命令超时: git " + strings.Join(args, " "), Commands: executed}
		}
		return pullOutcome{
			Error:      "Git 更新失败: " + gitFailureDetail(stdout, stderrText),
			Commands:   executed,
			StatusCode: http.StatusInternalServerError,
		}
	}
	target := "@{upstream}"
	if branch != "" {
		target = "origin/" + branch
	}
	outcome := s.applyPluginCode(ctx, repositoryPath, target, force, expectedCommit)
	outcome.Commands = append(executed, outcome.Commands...)
	return outcome
}

// pullGitHubRepository 对齐 _pull_github_repository：从当前下载源取分支后应用固定提交
func (s *Service) pullGitHubRepository(ctx context.Context, repositoryPath, branch, owner, repo string, force bool, expectedCommit *string) pullOutcome {
	executed := make([]string, 0, 4)
	if branch == "" {
		stdout, _, runErr := runGitCommand(ctx, repositoryPath, updateGitTimeout, "symbolic-ref", "--short", "HEAD")
		if runErr != nil {
			return pullOutcome{Error: "当前处于 detached HEAD，请明确选择更新分支", StatusCode: http.StatusConflict, Commands: executed}
		}
		branch = strings.TrimSpace(stdout)
	}
	if _, _, runErr := runGitCommand(ctx, repositoryPath, updateGitTimeout, "check-ref-format", "refs/heads/"+branch); runErr != nil || strings.HasPrefix(branch, "-") {
		return pullOutcome{Error: "无效的更新分支", StatusCode: http.StatusBadRequest, Commands: executed}
	}
	source, err := s.downloadSource()
	if err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusBadRequest, Commands: executed}
	}
	cloneURL, err := source.CloneURL(owner, repo)
	if err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusBadRequest, MirrorUsed: source.ID, Commands: executed}
	}
	fetchArgs := []string{"fetch", "--no-tags", cloneURL, "refs/heads/" + branch}
	executed = append(executed, "git "+strings.Join(fetchArgs, " "))
	stdout, stderrText, runErr := runGitCommand(ctx, repositoryPath, updateGitTimeout, fetchArgs...)
	if runErr != nil {
		if errors.Is(runErr, errGitNotFound) {
			return pullOutcome{Error: "Git 未安装或不在 PATH 中", MirrorUsed: source.ID, Commands: executed}
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			return pullOutcome{Error: "Git 命令超时: git " + strings.Join(fetchArgs, " "), MirrorUsed: source.ID, Commands: executed}
		}
		return pullOutcome{
			Error:      "Git fetch 失败: " + gitFailureDetail(stdout, stderrText),
			Commands:   executed,
			MirrorUsed: source.ID,
			StatusCode: http.StatusBadGateway,
		}
	}
	outcome := s.applyPluginCode(ctx, repositoryPath, "FETCH_HEAD", force, expectedCommit)
	outcome.MirrorUsed = source.ID
	outcome.Commands = append(executed, outcome.Commands...)
	return outcome
}

// applyPluginCode 固定目标提交，备份并原地替换插件代码。
func (s *Service) applyPluginCode(ctx context.Context, repositoryPath, target string, force bool, expectedCommit *string) pullOutcome {
	// 一旦开始写文件，即使请求取消也要等待替换或恢复完成。
	applyCtx := context.WithoutCancel(ctx)
	commitOutput, err := gitOutput(applyCtx, repositoryPath, "rev-parse", target)
	if err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusInternalServerError}
	}
	commit := strings.TrimSpace(commitOutput)
	if force && (expectedCommit == nil || commit != *expectedCommit) {
		return pullOutcome{Error: "目标版本已变化，请重新发起更新并确认", StatusCode: http.StatusConflict}
	}
	selected := commit
	installedRaw, err := os.ReadFile(filepath.Join(repositoryPath, "_manifest.json"))
	if err != nil {
		return pullOutcome{Error: "读取已安装插件清单失败: " + err.Error(), StatusCode: http.StatusInternalServerError}
	}
	var installed map[string]any
	if err := sonic.Unmarshal(installedRaw, &installed); err != nil {
		return pullOutcome{Error: "读取已安装插件清单失败: " + err.Error(), StatusCode: http.StatusInternalServerError}
	}
	manifestOutput, err := gitOutput(applyCtx, repositoryPath, "show", selected+":_manifest.json")
	if err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusInternalServerError}
	}
	var manifest map[string]any
	if err := sonic.UnmarshalString(manifestOutput, &manifest); err != nil {
		return pullOutcome{Error: "解析新版本插件清单失败: " + err.Error(), StatusCode: http.StatusInternalServerError}
	}
	if safeString(manifest["id"]) != safeString(installed["id"]) {
		return pullOutcome{Error: "新版本插件 ID 与已安装插件不一致", StatusCode: http.StatusBadRequest}
	}
	conflict, err := updateConflict(applyCtx, repositoryPath, repositoryPath, selected)
	if err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusInternalServerError}
	}
	if conflict != nil && !force {
		return pullOutcome{
			Error: conflict.Message, Payload: conflict, StatusCode: http.StatusConflict,
		}
	}
	backup := filepath.Join(filepath.Dir(repositoryPath), ".update_backups", filepath.Base(repositoryPath)+"."+randomHex(16))
	if err := backupCode(applyCtx, repositoryPath, backup, selected); err != nil {
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusInternalServerError}
	}
	if err := replaceCode(applyCtx, repositoryPath, selected); err != nil {
		if restoreErr := restoreCode(applyCtx, repositoryPath, backup); restoreErr != nil {
			zap.L().Error("恢复插件代码备份失败", zap.String("path", repositoryPath), zap.Error(restoreErr))
		}
		return pullOutcome{Error: err.Error(), StatusCode: http.StatusInternalServerError}
	}
	return pullOutcome{Success: true, BackupPath: backup}
}

// gitFailureDetail 对齐 stderr.strip() or stdout.strip()
func gitFailureDetail(stdout, stderrText string) string {
	if detail := strings.TrimSpace(stderrText); detail != "" {
		return detail
	}
	return strings.TrimSpace(stdout)
}

// marketErrorDetail 提取业务错误文案，供进度回显
func marketErrorDetail(err error) string {
	if marketErr, ok := AsMarketError(err); ok {
		return marketErr.Message
	}
	return err.Error()
}

// isGitRepository 判断插件目录是否为 Git 工作区
func isGitRepository(pluginPath string) bool {
	info, err := os.Stat(filepath.Join(pluginPath, ".git"))
	return err == nil && info.IsDir()
}

// branchName 返回请求分支，缺省对齐 UpdatePluginRequest 的 main
func (r UpdateRequest) branchName() string {
	if r.Branch == nil {
		return defaultUpdateBranch
	}
	return strings.TrimSpace(*r.Branch)
}

// updateSourceURL 对齐 update_plugin 的 source_url 回退顺序：urls.repository 优先，其次 repository_url
func updateSourceURL(manifest map[string]any) string {
	urls, _ := anyMap(manifest["urls"])
	repository := ""
	if urls != nil {
		repository = safeString(urls["repository"])
	}
	if repository == "" {
		repository = safeString(manifest["repository_url"])
	}
	return repository
}

// updateWorkPaths 对齐 _build_update_work_path：插件根目录下的临时目录与备份目录
func updateWorkPaths(pluginPath, pluginID string) (candidatePath string, backupPath string, err error) {
	root := filepath.Dir(pluginPath)
	tmpDir, err := workDirectory(root, updateTmpDirectory)
	if err != nil {
		return "", "", err
	}
	backupDir, err := workDirectory(root, updateBackupDirectory)
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	name := fmt.Sprintf("%s.%s%06d", sanitizePluginID(pluginID), now.Format("20060102150405"), now.Nanosecond()/1000)
	return filepath.Join(tmpDir, name), filepath.Join(backupDir, name), nil
}

// sanitizePluginID 把非字母数字与 ._- 的字符替换为下划线，对齐 _build_update_work_path
func sanitizePluginID(pluginID string) string {
	var builder strings.Builder
	for _, char := range pluginID {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '.' || char == '_' || char == '-' {
			builder.WriteRune(char)
			continue
		}
		builder.WriteRune('_')
	}
	return builder.String()
}

// restoreKnownUserFiles 对齐 _restore_known_user_files：只恢复 WebUI 管理的配置与配置备份
func restoreKnownUserFiles(sourcePluginPath, targetPluginPath string) error {
	configPath := filepath.Join(sourcePluginPath, "config.toml")
	if info, err := os.Lstat(configPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return badRequestError("插件配置文件不能是符号链接")
	}
	if info, err := os.Stat(configPath); err == nil && info.Mode().IsRegular() {
		targetConfigPath := filepath.Join(targetPluginPath, "config.toml")
		if err := removePath(targetConfigPath); err != nil {
			return err
		}
		if err := copyFile(configPath, targetConfigPath); err != nil {
			return err
		}
	}
	configBackupDir := filepath.Join(sourcePluginPath, "config_back")
	if info, err := os.Lstat(configBackupDir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		targetBackupDir := filepath.Join(targetPluginPath, "config_back")
		if err := removePath(targetBackupDir); err != nil {
			return err
		}
		if err := copyDirectory(configBackupDir, targetBackupDir); err != nil {
			return err
		}
	}
	return nil
}

// removePath 对齐 _remove_path：拒绝符号链接，目录走 RemoveTree
func removePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("拒绝删除符号链接路径: %s", path)
	}
	if info.IsDir() {
		return RemoveTree(path)
	}
	return os.Remove(path)
}

// requiredManifest 对齐 _read_required_manifest：清单缺失或不可读都按 400 暴露
func requiredManifest(pluginPath string) (map[string]any, error) {
	manifestPath, err := resolvePluginFilePath(pluginPath, "_manifest.json", true)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifestJSON(manifestPath)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		return nil, badRequestError("无效的插件：_manifest.json 不存在或无法读取")
	}
	return manifest, nil
}

// requireManifestPluginID 对齐 _require_manifest_plugin_id
func requireManifestPluginID(manifest map[string]any) (string, error) {
	pluginID := strings.TrimSpace(safeString(manifest["id"]))
	if pluginID == "" {
		return "", badRequestError("无效的插件：_manifest.json 缺少 id")
	}
	return pluginID, nil
}

// validateUpdatedManifest 对齐 _validate_updated_manifest：新版本必须保持插件身份不变
func validateUpdatedManifest(pluginPath, expectedPluginID string) (map[string]any, error) {
	manifest, err := requiredManifest(pluginPath)
	if err != nil {
		return nil, err
	}
	newPluginID, err := requireManifestPluginID(manifest)
	if err != nil {
		return nil, err
	}
	if newPluginID != expectedPluginID {
		return nil, badRequestError("新版本插件 ID 不匹配：期望 %s，实际 %s", expectedPluginID, newPluginID)
	}
	return manifest, nil
}

// manifestVersionText 对齐 str(manifest.get("version", "unknown"))
func manifestVersionText(manifest map[string]any) string {
	value, ok := manifest["version"]
	if !ok || value == nil {
		return "unknown"
	}
	return safeString(value)
}

// manifestNameText 读取插件显示名，缺失时回退插件 ID
func manifestNameText(manifest map[string]any, fallback string) string {
	name := safeString(manifest["name"])
	if name == "" {
		return fallback
	}
	return name
}
