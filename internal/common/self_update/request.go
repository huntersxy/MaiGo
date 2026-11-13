package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maibot-go/internal/common/version"

	"github.com/bytedance/sonic"
)

// RequestOptions 登记更新请求的参数
type RequestOptions struct {
	Root           string
	CurrentVersion string
	SourceID       string
	CustomPrefix   string
	// EnforcePublicOutbound 为 nil 时按 Python 默认开启公网校验
	EnforcePublicOutbound *bool
}

// StateDir 返回自更新状态目录
func StateDir(root string) string {
	return filepath.Join(root, "data", "self_update")
}

// PendingPath 返回待执行更新请求路径
func PendingPath(root string) string {
	return filepath.Join(StateDir(root), "pending.json")
}

// ResultPath 返回最近一次更新结果路径
func ResultPath(root string) string {
	return filepath.Join(StateDir(root), "last_result.json")
}

// RequestUpdate 登记更新请求，等待 Runner 在下次重启时执行
func RequestUpdate(ctx context.Context, expected string, opts RequestOptions) (PendingUpdate, error) {
	enforcePublicOutbound := enforcePublicOutboundValue(opts.EnforcePublicOutbound)
	if normalizeSourceID(opts.SourceID) == "custom" {
		if _, err := validatePublicPrefix(opts.CustomPrefix, enforcePublicOutbound); err != nil {
			return PendingUpdate{}, err
		}
	}
	root, err := resolveRoot(opts.Root)
	if err != nil {
		return PendingUpdate{}, err
	}
	local, err := InspectLocal(ctx, root, opts.CurrentVersion)
	if err != nil {
		return PendingUpdate{}, err
	}
	if local.Blocker != "" {
		return PendingUpdate{}, NewError(local.Blocker, BlockerMessage(local.Blocker))
	}
	if local.DeploymentType == DeploymentPackage {
		return requestPackageUpdate(ctx, root, local, expected, opts.SourceID)
	}
	selected, err := selectUpdateSource(opts.SourceID, opts.CustomPrefix, enforcePublicOutbound)
	if err != nil {
		return PendingUpdate{}, err
	}
	if !validateCommitSHA(expected) {
		return PendingUpdate{}, errorf("invalid_commit", "目标提交格式不正确")
	}
	if local.Branch == "" || local.Commit == "" {
		return PendingUpdate{}, errorf("state_changed", "本地仓库状态缺少当前分支或提交，无法登记更新")
	}
	if expected == local.Commit {
		return PendingUpdate{}, errorf("already_up_to_date", "当前已经是这个版本")
	}
	if _, err := gitFetchBranch(ctx, root, selected.ID, selected.CloneURL, local.Branch); err != nil {
		return PendingUpdate{}, err
	}
	exists, err := gitCatFileCommit(ctx, root, expected)
	if err != nil {
		return PendingUpdate{}, err
	}
	if !exists {
		return PendingUpdate{}, errorf("unknown_commit", "本地没有这个目标提交，请重新检查更新")
	}
	changed, err := changedFiles(ctx, root, local.Commit, expected)
	if err != nil {
		return PendingUpdate{}, err
	}
	blockers, err := collectLocalBlockers(ctx, root, local, expected, changed)
	if err != nil {
		return PendingUpdate{}, err
	}
	if len(blockers) > 0 {
		return PendingUpdate{}, NewError(blockers[0], BlockerMessage(blockers[0]))
	}
	pending := PendingUpdate{
		TargetCommit:          expected,
		BaseCommit:            local.Commit,
		Branch:                local.Branch,
		RequestedAt:           float64(time.Now().UnixNano()) / 1e9,
		SourceID:              selected.ID,
		EnforcePublicOutbound: opts.EnforcePublicOutbound,
	}
	if selected.ID == "custom" {
		pending.CustomPrefix = opts.CustomPrefix
	}
	if err := writeJSONAtomic(PendingPath(root), pending, true); err != nil {
		if errors.Is(err, os.ErrExist) {
			return PendingUpdate{}, errorf("package_busy", "已有更新请求，请等待更新完成")
		}
		return PendingUpdate{}, err
	}
	return pending, nil
}

func requestPackageUpdate(ctx context.Context, root string, local LocalState, expected, sourceID string) (PendingUpdate, error) {
	selected := strings.TrimSpace(sourceID)
	if selected == "" {
		selected = "pypi"
	} else {
		selected = normalizeSourceID(selected)
	}
	if _, err := packageIndex(selected); err != nil {
		return PendingUpdate{}, err
	}
	if !version.IsValidSemVer(expected) {
		return PendingUpdate{}, errorf("invalid_version", "只能更新到形如 1.3.9 的正式版本")
	}
	compared, err := version.CompareSemVer(expected, local.Version)
	if err != nil {
		return PendingUpdate{}, errorf("invalid_version", "当前版本不是正式版本，无法执行包更新")
	}
	if compared <= 0 {
		return PendingUpdate{}, errorf("already_up_to_date", "目标版本必须高于当前版本")
	}
	payload, err := fetchPackageReleases(ctx, selected)
	if err != nil {
		return PendingUpdate{}, err
	}
	if !releaseExists(payload, expected) {
		return PendingUpdate{}, errorf("unknown_version", "所选包源没有这个可用的正式版本，请重新检查")
	}
	pending := PendingUpdate{
		TargetCommit: expected, BaseCommit: local.Version, Branch: "pypi",
		RequestedAt: float64(time.Now().UnixNano()) / 1e9, SourceID: selected,
	}
	if err := writeJSONAtomic(PendingPath(root), pending, true); err != nil {
		if errors.Is(err, os.ErrExist) {
			return PendingUpdate{}, errorf("package_busy", "已有更新请求，请等待更新完成")
		}
		return PendingUpdate{}, err
	}
	return pending, nil
}

// ClearPendingUpdate 丢弃尚未执行的更新请求
func ClearPendingUpdate(root string) error {
	resolved, err := resolveRoot(root)
	if err != nil {
		return err
	}
	err = os.Remove(PendingPath(resolved))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadLastResult 读取最近一次更新的执行结果
func ReadLastResult(root string) (*UpdateResult, error) {
	resolved, err := resolveRoot(root)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(ResultPath(resolved))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var result UpdateResult
	if err := sonic.ConfigStd.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func writeJSONAtomic(path string, payload any, exclusive bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if exclusive {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := file.Write(raw); err != nil {
			file.Close()
			_ = os.Remove(path)
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			_ = os.Remove(path)
			return err
		}
		return file.Close()
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		_ = os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return err
	}
	return nil
}

func collectLocalBlockers(ctx context.Context, root string, local LocalState, target string, changed map[string]struct{}) ([]string, error) {
	blockers := make([]string, 0)
	if len(local.DirtyFiles) > 0 {
		blockers = append(blockers, "dirty_worktree")
	}
	ancestor, err := isAncestor(ctx, root, "HEAD", target)
	if err != nil {
		return nil, err
	}
	if !ancestor {
		blockers = append(blockers, "diverged")
	}
	if dependenciesChanged(changed) && local.DependencyTool == "" {
		blockers = append(blockers, "no_dependency_tool")
	}
	localPyproject, readErr := readLocalFile(root, "pyproject.toml")
	if readErr == nil {
		targetPyproject, showErr := gitShowFile(ctx, root, target, "pyproject.toml")
		if showErr == nil {
			_, localRequires := readProjectVersion(localPyproject)
			_, targetRequires := readProjectVersion(targetPyproject)
			if localRequires != targetRequires {
				blockers = append(blockers, "python_requirement_changed")
			}
		}
	}
	return blockers, nil
}

func pendingRequestedAt(pending PendingUpdate) time.Time {
	return time.Unix(int64(pending.RequestedAt), 0)
}

func isPackagePending(pending PendingUpdate) bool {
	return strings.EqualFold(strings.TrimSpace(pending.Branch), "pypi")
}
