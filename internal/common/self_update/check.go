package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"MaiGo/internal/common/version"
)

// CheckOptions 一次检查的参数
type CheckOptions struct {
	Root           string
	CurrentVersion string
	SourceID       string
	CustomPrefix   string
	Force          bool
	// EnforcePublicOutbound 为 nil 时按 Python 默认开启公网校验
	EnforcePublicOutbound *bool
	OnProgress            func(string)
}

// Manager 进程内更新检查缓存
type Manager struct {
	// mu 只保护缓存与来源选择，联网检查不得持有该锁，避免状态查询被检查阻塞
	mu             sync.RWMutex
	cache          *UpdateCheck
	expiresAt      time.Time
	sourceID       string
	customPrefix   string
	root           string
	currentVersion string

	// checkMu 与 inFlight 负责检查串行化：同一组参数的并发请求共享一次联网结果
	checkMu  sync.Mutex
	inFlight *checkCall

	// 可注入点仅用于包内测试，生产路径使用 InspectLocal 与真实远端检查
	ttl            time.Duration
	nowFn          func() time.Time
	inspectLocalFn func(context.Context, string, string) (LocalState, error)
	remoteCheckFn  func(context.Context, LocalState, string, string, string, bool, func(string)) (UpdateCheck, error)

	// refreshMu 保护后台刷新生命周期，停止时必须立即返回，不等待联网结束
	refreshMu       sync.Mutex
	refreshActive   bool
	refreshStop     chan struct{}
	refreshCancel   context.CancelFunc
	refreshInterval time.Duration
	refreshTimeout  time.Duration
}

var (
	defaultManager = &Manager{}
	cacheTTL       = 60 * time.Minute
)

// DefaultManager 返回全局更新检查缓存
func DefaultManager() *Manager {
	return defaultManager
}

// InspectLocal 读取本地安装状态，不联网
func InspectLocal(ctx context.Context, root, currentVersion string) (LocalState, error) {
	resolved, err := resolveRoot(root)
	if err != nil {
		return LocalState{}, err
	}
	state := LocalState{
		Version:        strings.TrimSpace(currentVersion),
		DeploymentType: DeploymentGit,
	}
	if state.Version == "" {
		state.Version = version.String()
	}
	if pathExists(filepath.Join(resolved, ".git")) {
		state.DeploymentType = DeploymentGit
		state.DependencyTool = detectDependencyTool(resolved)
		if !gitAvailable() {
			state.Blocker = "git_missing"
			return state, nil
		}
		commit, err := gitRevParse(ctx, resolved, "HEAD")
		if err != nil {
			return state, err
		}
		state.Commit = commit
		branch, err := gitCurrentBranch(ctx, resolved)
		if err != nil {
			if ErrorCode(err) == "detached_head" {
				state.Blocker = "detached_head"
				return state, nil
			}
			return state, err
		}
		state.Branch = branch
		if !isSupportedBranch(branch) {
			state.Blocker = "unsupported_branch"
			return state, nil
		}
		dirty, err := gitDirtyFiles(ctx, resolved)
		if err != nil {
			return state, err
		}
		state.DirtyFiles = dirty
		return state, nil
	}

	state.DeploymentType = DeploymentPackage
	state.DependencyTool = detectDependencyTool(resolved)
	if !packageToolEnvironmentExists(resolved) {
		state.Blocker = "not_git_repo"
		return state, nil
	}
	if _, err := os.Stat(filepath.Join(resolved, "uv-receipt.toml")); err != nil {
		state.Blocker = "not_uv_tool"
		return state, nil
	}
	if _, lookErr := exec.LookPath("uv"); lookErr != nil {
		state.Blocker = "uv_missing"
		return state, nil
	}
	if os.Getenv("MAIBOT_PACKAGE_RUNNER") != "1" {
		state.Blocker = "package_runner_missing"
	}
	return state, nil
}

func isSupportedBranch(branch string) bool {
	for _, supported := range SupportedBranches {
		if branch == supported {
			return true
		}
	}
	return false
}

func detectDependencyTool(root string) string {
	if pathExists(filepath.Join(root, "go.mod")) {
		if _, err := exec.LookPath("go"); err == nil {
			return "go"
		}
	}
	if pathExists(filepath.Join(root, "pyproject.toml")) {
		if _, err := exec.LookPath("uv"); err == nil {
			return "uv"
		}
	}
	if pathExists(filepath.Join(root, "requirements.txt")) {
		if _, err := exec.LookPath("python"); err == nil {
			return "pip"
		}
		if _, err := exec.LookPath("python3"); err == nil {
			return "pip"
		}
	}
	return ""
}

func packageToolEnvironmentExists(root string) bool {
	if pathExists(filepath.Join(root, "uv-receipt.toml")) {
		return true
	}
	if value := strings.TrimSpace(os.Getenv("VIRTUAL_ENV")); value != "" {
		return pathExists(filepath.Join(value, "uv-receipt.toml"))
	}
	return pathExists(filepath.Join(root, ".venv", "uv-receipt.toml"))
}

// checkCall 表示一次进行中的联网检查；相同参数的并发请求等待并共享结果
type checkCall struct {
	key   checkKey
	done  chan struct{}
	check UpdateCheck
	err   error
}

// checkKey 区分手动/自动、来源与目标目录；不同参数必须串行但各自联网
type checkKey struct {
	root                  string
	currentVersion        string
	sourceID              string
	customPrefix          string
	force                 bool
	enforcePublicOutbound bool
}

// Check 联网检查更新，force=false 时优先使用未过期的缓存
// 同一组参数的并发检查只发起一次联网；不同参数严格串行，避免锁外并发重复请求
func (m *Manager) Check(ctx context.Context, opts CheckOptions) (UpdateCheck, error) {
	enforcePublicOutbound := enforcePublicOutboundValue(opts.EnforcePublicOutbound)
	if normalizeSourceID(opts.SourceID) == "custom" {
		if _, err := validatePublicPrefix(opts.CustomPrefix, enforcePublicOutbound); err != nil {
			return UpdateCheck{}, err
		}
	}
	key := checkKey{
		root:                  opts.Root,
		currentVersion:        opts.CurrentVersion,
		sourceID:              opts.SourceID,
		customPrefix:          opts.CustomPrefix,
		force:                 opts.Force,
		enforcePublicOutbound: enforcePublicOutbound,
	}
	for {
		m.checkMu.Lock()
		if call := m.inFlight; call != nil {
			if call.key == key {
				m.checkMu.Unlock()
				select {
				case <-call.done:
					return call.check, call.err
				case <-ctx.Done():
					return UpdateCheck{}, ctx.Err()
				}
			}
			done := call.done
			m.checkMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return UpdateCheck{}, ctx.Err()
			}
		}
		call := &checkCall{key: key, done: make(chan struct{})}
		m.inFlight = call
		m.checkMu.Unlock()

		check, err := m.checkOnce(ctx, opts)

		m.checkMu.Lock()
		call.check, call.err = check, err
		if m.inFlight == call {
			m.inFlight = nil
		}
		close(call.done)
		m.checkMu.Unlock()
		return check, err
	}
}

// checkOnce 执行一次完整检查，调用方必须已通过 checkMu 取得串行化资格
func (m *Manager) checkOnce(ctx context.Context, opts CheckOptions) (UpdateCheck, error) {
	if opts.OnProgress != nil {
		opts.OnProgress("检查本地安装与仓库状态")
	}
	root, err := resolveRoot(opts.Root)
	if err != nil {
		return UpdateCheck{}, err
	}
	m.rememberOptions(opts)
	local, err := m.inspectLocalState(ctx, root, opts.CurrentVersion)
	if err != nil {
		return UpdateCheck{}, err
	}
	m.mu.RLock()
	cached := m.peekLocked(local)
	if local.Blocker == "" && !opts.Force && cached.CheckedAt != nil && m.now().Before(m.expiresAt) {
		m.mu.RUnlock()
		if opts.OnProgress != nil {
			opts.OnProgress("使用已有的更新检查结果")
		}
		return cached, nil
	}
	m.mu.RUnlock()

	m.mu.Lock()
	if opts.SourceID != "" {
		m.sourceID = opts.SourceID
		m.customPrefix = opts.CustomPrefix
	} else if m.sourceID == "" {
		if local.DeploymentType == DeploymentPackage {
			m.sourceID = "pypi"
		} else {
			m.sourceID = OfficialSourceID
		}
	}
	sourceID, customPrefix := m.sourceID, m.customPrefix
	m.mu.Unlock()

	if local.Blocker != "" {
		check := UpdateCheck{Local: local, Blockers: []string{local.Blocker}}
		return check, nil
	}
	enforcePublicOutbound := enforcePublicOutboundValue(opts.EnforcePublicOutbound)
	check, err := m.remoteCheck(ctx, local, root, sourceID, customPrefix, enforcePublicOutbound, opts.OnProgress)
	if err != nil {
		return UpdateCheck{}, err
	}
	m.mu.Lock()
	m.cache = &check
	m.expiresAt = m.now().Add(m.cacheDuration())
	m.mu.Unlock()
	return check, nil
}

// Peek 只读取缓存，不联网
func (m *Manager) Peek(ctx context.Context, opts CheckOptions) (UpdateCheck, error) {
	m.rememberOptions(opts)
	root, err := resolveRoot(opts.Root)
	if err != nil {
		return UpdateCheck{}, err
	}
	local, err := m.inspectLocalState(ctx, root, opts.CurrentVersion)
	if err != nil {
		return UpdateCheck{}, err
	}
	if local.Blocker != "" {
		return UpdateCheck{Local: local, Blockers: []string{local.Blocker}}, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.peekLocked(local), nil
}

func (m *Manager) rememberOptions(opts CheckOptions) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value := strings.TrimSpace(opts.Root); value != "" {
		m.root = value
	}
	if value := strings.TrimSpace(opts.CurrentVersion); value != "" {
		m.currentVersion = value
	}
}

func (m *Manager) backgroundOptions() CheckOptions {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return CheckOptions{Root: m.root, CurrentVersion: m.currentVersion}
}

func (m *Manager) inspectLocalState(ctx context.Context, root, currentVersion string) (LocalState, error) {
	if m.inspectLocalFn != nil {
		return m.inspectLocalFn(ctx, root, currentVersion)
	}
	return InspectLocal(ctx, root, currentVersion)
}

func (m *Manager) remoteCheck(ctx context.Context, local LocalState, root, sourceID, customPrefix string, enforcePublicOutbound bool, progress func(string)) (UpdateCheck, error) {
	if m.remoteCheckFn != nil {
		return m.remoteCheckFn(ctx, local, root, sourceID, customPrefix, enforcePublicOutbound, progress)
	}
	if local.DeploymentType == DeploymentPackage {
		return checkPackage(ctx, local, root, sourceID, progress)
	}
	return checkGit(ctx, local, root, sourceID, customPrefix, enforcePublicOutbound, progress)
}

func enforcePublicOutboundValue(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func (m *Manager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

func (m *Manager) cacheDuration() time.Duration {
	if m.ttl > 0 {
		return m.ttl
	}
	return cacheTTL
}

func (m *Manager) peekLocked(local LocalState) UpdateCheck {
	cached := m.cache
	if cached == nil {
		return UpdateCheck{Local: local}
	}
	if cached.Local.Branch != local.Branch || cached.Local.Commit != local.Commit ||
		cached.Local.Version != local.Version || cached.Local.DeploymentType != local.DeploymentType {
		return UpdateCheck{Local: local}
	}
	blockers := make([]string, 0, len(cached.Blockers)+1)
	for _, blocker := range cached.Blockers {
		if blocker != "dirty_worktree" {
			blockers = append(blockers, blocker)
		}
	}
	if len(local.DirtyFiles) > 0 {
		blockers = append(blockers, "dirty_worktree")
	}
	copied := *cached
	copied.Local = local
	copied.Blockers = blockers
	return copied
}

func checkPackage(ctx context.Context, local LocalState, root, sourceID string, progress func(string)) (UpdateCheck, error) {
	selected := strings.TrimSpace(sourceID)
	if selected == "" {
		selected = "pypi"
	} else {
		selected = normalizeSourceID(selected)
	}
	if _, err := packageIndex(selected); err != nil {
		return UpdateCheck{}, err
	}
	if progress != nil {
		progress(packageSourceName(selected) + "：读取正式版本列表")
	}
	latest, err := latestRelease(ctx, local.Version, selected)
	if err != nil {
		return UpdateCheck{}, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	check := UpdateCheck{
		Local: local, CheckedAt: &now, LatestCommit: latest, LatestVersion: latest,
		Source: &UpdateSource{ID: selected, Name: packageSourceName(selected), URL: mustPackageIndex(selected)},
	}
	if version.IsValidSemVer(local.Version) && version.IsValidSemVer(latest) {
		compared, compareErr := version.CompareSemVer(latest, local.Version)
		if compareErr != nil {
			return UpdateCheck{}, compareErr
		}
		if compared > 0 {
			check.Behind = 1
			check.DependenciesChanged = true
		}
	}
	if local.Blocker != "" {
		check.Blockers = []string{local.Blocker}
	}
	return check, nil
}

func mustPackageIndex(sourceID string) string {
	index, _ := packageIndex(sourceID)
	return index
}
