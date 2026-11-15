package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"MaiGo/internal/common/version"

	"github.com/bytedance/sonic"
)

// ApplyPending 执行已登记的更新请求；没有请求时返回 nil
func ApplyPending(ctx context.Context, root string) (*UpdateResult, error) {
	resolved, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	pendingPath := PendingPath(resolved)
	raw, err := os.ReadFile(pendingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pending PendingUpdate
	if err := sonic.ConfigStd.Unmarshal(raw, &pending); err != nil {
		return nil, err
	}
	if err := os.Remove(pendingPath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	result := &UpdateResult{
		StartedAt:   float64(time.Now().UnixNano()) / 1e9,
		Branch:      pending.Branch,
		FromCommit:  pending.BaseCommit,
		ToCommit:    pending.TargetCommit,
		Stage:       "request",
		SourceName:  sourceDisplayName(pending.SourceID),
		FromVersion: localVersion(resolved),
	}
	if err := applyPendingInto(ctx, resolved, pending, result); err != nil {
		result.ErrorCode = ErrorCode(err)
		result.Error = err.Error()
		result.FinishedAt = float64(time.Now().UnixNano()) / 1e9
		_ = writeJSONAtomic(ResultPath(resolved), result, false)
		return result, err
	}
	result.Success = true
	result.Stage = "done"
	result.FinishedAt = float64(time.Now().UnixNano()) / 1e9
	if err := writeJSONAtomic(ResultPath(resolved), result, false); err != nil {
		return result, err
	}
	return result, nil
}

func applyPendingInto(ctx context.Context, root string, pending PendingUpdate, result *UpdateResult) error {
	if isPackagePending(pending) {
		return applyPackageUpdate(ctx, root, pending, result)
	}
	return applyGitUpdate(ctx, root, pending, result)
}

func applyGitUpdate(ctx context.Context, root string, pending PendingUpdate, result *UpdateResult) error {
	if time.Since(pendingRequestedAt(pending)) > pendingMaxAge {
		return errorf("request_expired", "更新请求已过期，请重新检查")
	}
	local, err := InspectLocal(ctx, root, result.FromVersion)
	if err != nil {
		return err
	}
	if local.Blocker != "" {
		return NewError(local.Blocker, BlockerMessage(local.Blocker))
	}
	if local.Branch != pending.Branch || local.Commit != pending.BaseCommit {
		return errorf("state_changed", "本地分支或提交已变化，请重新检查更新")
	}
	source, err := selectUpdateSource(pending.SourceID, pending.CustomPrefix, enforcePublicOutboundValue(pending.EnforcePublicOutbound))
	if err != nil {
		return err
	}
	result.SourceName = source.Name
	fetched, err := gitFetchBranch(ctx, root, source.ID, source.CloneURL, pending.Branch)
	if err != nil {
		return err
	}
	result.Stage = "verify"
	ancestor, err := isAncestor(ctx, root, pending.TargetCommit, fetched)
	if err != nil {
		return err
	}
	if !ancestor {
		return errorf("untrusted_commit", "目标提交不在官方分支历史中，已拒绝更新")
	}
	changed, err := changedFiles(ctx, root, pending.BaseCommit, pending.TargetCommit)
	if err != nil {
		return err
	}
	blockers, err := collectLocalBlockers(ctx, root, local, pending.TargetCommit, changed)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return NewError(blockers[0], BlockerMessage(blockers[0]))
	}
	result.ToVersion = remoteVersionAt(ctx, root, pending.TargetCommit)
	result.Stage = "merge"
	if err := gitMergeFF(ctx, root, pending.TargetCommit); err != nil {
		rollbackGitUpdate(ctx, root, pending.BaseCommit, "", result)
		return NewError("git_failed", rollbackMessage("快进合并失败", result, "代码", err))
	}
	dependencyTool := ""
	if dependenciesChanged(changed) {
		dependencyTool = detectDependencyTool(root)
		if dependencyTool == "" {
			dependencyTool = local.DependencyTool
		}
		result.Stage = "dependencies"
		if err := syncDependencies(ctx, root, dependencyTool); err != nil {
			rollbackGitUpdate(ctx, root, pending.BaseCommit, dependencyTool, result)
			return NewError("dependency_failed", rollbackMessage("依赖同步失败", result, "代码与依赖", err))
		}
		result.DependenciesSynced = true
	}
	if pathExists(filepath.Join(root, "go.mod")) {
		result.Stage = "build"
		if _, err := buildPendingBinary(ctx, root); err != nil {
			if result.DependenciesSynced {
				rollbackGitUpdate(ctx, root, pending.BaseCommit, dependencyTool, result)
			} else {
				rollbackGitUpdate(ctx, root, pending.BaseCommit, "", result)
			}
			return NewError("build_failed", rollbackMessage("构建新二进制失败", result, "代码", err))
		}
	}
	return nil
}

// rollbackGitUpdate 恢复更新前的提交；dependencyTool 非空时把依赖也同步回来
func rollbackGitUpdate(ctx context.Context, root, baseCommit, dependencyTool string, result *UpdateResult) {
	result.RolledBack = false
	if err := gitResetHard(ctx, root, baseCommit); err != nil {
		result.RollbackError = err.Error()
		return
	}
	if dependencyTool != "" {
		if err := syncDependencies(ctx, root, dependencyTool); err != nil {
			result.RollbackError = err.Error()
			return
		}
	}
	result.RolledBack = true
}

func rollbackMessage(action string, result *UpdateResult, scope string, cause error) string {
	if result.RolledBack {
		return action + "，已回滚" + scope + "：" + cause.Error()
	}
	if result.RollbackError != "" {
		return action + "，回滚" + scope + "失败（" + result.RollbackError + "），请手动检查：" + cause.Error()
	}
	return action + "，回滚" + scope + "未完成，请手动检查：" + cause.Error()
}

func buildPendingBinary(ctx context.Context, root string) (string, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", errorf("build_failed", "未找到 go，无法构建新二进制")
	}
	if err := os.MkdirAll(StateDir(root), 0o755); err != nil {
		return "", err
	}
	output := filepath.Join(StateDir(root), "maibot-next")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, "go", "build", "-o", output, ".")
	command.Dir = root
	if data, err := command.CombinedOutput(); err != nil {
		text := strings.TrimSpace(string(data))
		if len(text) > 4000 {
			text = text[len(text)-4000:]
		}
		return "", errorf("build_failed", "go build 失败：%s", text)
	}
	marker := struct {
		BinaryPath  string  `json:"binary_path"`
		RequestedAt float64 `json:"requested_at"`
	}{BinaryPath: output, RequestedAt: float64(time.Now().UnixNano()) / 1e9}
	if err := writeJSONAtomic(filepath.Join(StateDir(root), "pending_binary.json"), marker, false); err != nil {
		return "", err
	}
	return output, nil
}

func localVersion(root string) string {
	if raw, err := readLocalFile(root, "pyproject.toml"); err == nil {
		if value, _ := readProjectVersion(raw); value != "" {
			return value
		}
	}
	for _, name := range []string{"VERSION", "version.txt"} {
		if raw, err := readLocalFile(root, name); err == nil {
			value := strings.TrimSpace(raw)
			if value != "" {
				return value
			}
		}
	}
	return version.String()
}

func remoteVersionAt(ctx context.Context, root, commit string) string {
	raw, err := gitShowFile(ctx, root, commit, "pyproject.toml")
	if err != nil {
		return ""
	}
	value, _ := readProjectVersion(raw)
	return value
}

func syncDependencies(ctx context.Context, root, tool string) error {
	switch tool {
	case "go":
		return runDependencyCommand(ctx, root, "go", "mod", "download")
	case "uv":
		return syncUVDependencies(ctx, root)
	case "pip":
		python, err := findPython()
		if err != nil {
			return err
		}
		return runDependencyCommand(ctx, root, python, "-m", "pip", "install", "-r", "requirements.txt")
	default:
		return errorf("no_dependency_tool", "%s", BlockerMessage("no_dependency_tool"))
	}
}

func runDependencyCommand(ctx context.Context, root, name string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	command := exec.CommandContext(runCtx, name, args...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if len(text) > 4000 {
			text = text[len(text)-4000:]
		}
		return errorf("dependency_failed", "依赖命令 %s 执行失败：%s", name, text)
	}
	return nil
}

func findPython() (string, error) {
	for _, name := range []string{"python", "python3"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errorf("no_dependency_tool", "未找到 Python，无法同步依赖")
}
