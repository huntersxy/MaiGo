package selfupdate

import (
	"strings"
	"time"
)

// 自更新状态常量
const (
	DeploymentGit     = "git"
	DeploymentPackage = "package"

	OfficialRepositoryPath = "Mai-with-u/MaiBot"
	OfficialRepositoryURL  = "https://github.com/Mai-with-u/MaiBot.git"
	OfficialSourceID       = "github"

	pendingMaxAge = 10 * time.Minute
)

// SupportedBranches 允许自更新的分支
var SupportedBranches = []string{"main", "dev"}

// DependencyFiles 触发依赖同步的文件
var DependencyFiles = map[string]struct{}{
	"pyproject.toml":   {},
	"requirements.txt": {},
	"go.mod":           {},
	"go.sum":           {},
}

// LocalState 本地安装状态
type LocalState struct {
	Version        string   `json:"version"`
	Blocker        string   `json:"blocker,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	Commit         string   `json:"commit,omitempty"`
	DirtyFiles     []string `json:"dirty_files,omitempty"`
	DependencyTool string   `json:"dependency_tool,omitempty"`
	DeploymentType string   `json:"deployment_type"`
}

// UpdateSource 本次检查使用的来源
type UpdateSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// UpdateCommit 待更新的单个提交
type UpdateCommit struct {
	ShortID     string `json:"short_id"`
	Summary     string `json:"summary"`
	CommittedAt string `json:"committed_at"`
}

// UpdateCheck 一次更新检查的结论
type UpdateCheck struct {
	Local               LocalState     `json:"local"`
	CheckedAt           *float64       `json:"checked_at,omitempty"`
	LatestCommit        string         `json:"latest_commit,omitempty"`
	LatestVersion       string         `json:"latest_version,omitempty"`
	Behind              int            `json:"behind"`
	Ahead               int            `json:"ahead"`
	DependenciesChanged bool           `json:"dependencies_changed"`
	Commits             []UpdateCommit `json:"commits,omitempty"`
	Blockers            []string       `json:"blockers,omitempty"`
	Source              *UpdateSource  `json:"source,omitempty"`
}

// UpdateAvailable 是否有新提交或新版本
func (c UpdateCheck) UpdateAvailable() bool {
	return c.Behind > 0
}

// CanApply 是否可以执行更新
func (c UpdateCheck) CanApply() bool {
	return c.UpdateAvailable() && len(c.Blockers) == 0
}

// PendingUpdate Worker 登记、等待 Runner 执行的更新请求
type PendingUpdate struct {
	TargetCommit string  `json:"target_commit"`
	BaseCommit   string  `json:"base_commit"`
	Branch       string  `json:"branch"`
	RequestedAt  float64 `json:"requested_at"`
	SourceID     string  `json:"source_id,omitempty"`
	CustomPrefix string  `json:"custom_prefix,omitempty"`
	// EnforcePublicOutbound 记录登记时的公网校验开关，执行时按登记值校验
	EnforcePublicOutbound *bool `json:"enforce_public_outbound,omitempty"`
}

// UpdateResult Runner 执行一次更新后的结果
type UpdateResult struct {
	Success            bool    `json:"success"`
	StartedAt          float64 `json:"started_at"`
	FinishedAt         float64 `json:"finished_at"`
	Branch             string  `json:"branch"`
	FromCommit         string  `json:"from_commit"`
	ToCommit           string  `json:"to_commit"`
	FromVersion        string  `json:"from_version"`
	ToVersion          string  `json:"to_version"`
	Stage              string  `json:"stage"`
	ErrorCode          string  `json:"error_code"`
	Error              string  `json:"error"`
	RolledBack         bool    `json:"rolled_back"`
	RollbackError      string  `json:"rollback_error"`
	DependenciesSynced bool    `json:"dependencies_synced"`
	SourceName         string  `json:"source_name"`
}

// Status WebUI 自更新状态响应
type Status struct {
	Supported           bool           `json:"supported"`
	DeploymentType      string         `json:"deployment_type"`
	RepositoryURL       string         `json:"repository_url"`
	CurrentVersion      string         `json:"current_version"`
	CurrentCommit       *string        `json:"current_commit"`
	Branch              *string        `json:"branch"`
	CheckedAt           *float64       `json:"checked_at"`
	LatestVersion       *string        `json:"latest_version"`
	LatestCommit        *string        `json:"latest_commit"`
	Behind              int            `json:"behind"`
	Ahead               int            `json:"ahead"`
	UpdateAvailable     bool           `json:"update_available"`
	CanApply            bool           `json:"can_apply"`
	Blockers            []string       `json:"blockers"`
	DirtyFiles          []string       `json:"dirty_files"`
	DependenciesChanged bool           `json:"dependencies_changed"`
	DependencyTool      *string        `json:"dependency_tool"`
	Commits             []UpdateCommit `json:"commits"`
	SourceID            *string        `json:"source_id"`
	SourceName          *string        `json:"source_name"`
	LastResult          *UpdateResult  `json:"last_result"`
}

// BuildStatus 将检查结论转换为 WebUI JSON
func BuildStatus(check UpdateCheck, lastResult *UpdateResult) Status {
	local := check.Local
	deployment := local.DeploymentType
	if deployment == "" {
		deployment = DeploymentGit
	}
	repositoryURL := OfficialRepositoryURL
	if deployment == DeploymentPackage {
		repositoryURL = "https://pypi.org/project/maibot/"
	}
	status := Status{
		Supported:           local.Blocker == "",
		DeploymentType:      deployment,
		RepositoryURL:       repositoryURL,
		CurrentVersion:      local.Version,
		Behind:              check.Behind,
		Ahead:               check.Ahead,
		UpdateAvailable:     check.UpdateAvailable(),
		CanApply:            check.CanApply(),
		Blockers:            append([]string{}, check.Blockers...),
		DirtyFiles:          append([]string{}, local.DirtyFiles...),
		DependenciesChanged: check.DependenciesChanged,
		Commits:             append([]UpdateCommit{}, check.Commits...),
		LastResult:          lastResult,
	}
	if status.Blockers == nil {
		status.Blockers = []string{}
	}
	if status.DirtyFiles == nil {
		status.DirtyFiles = []string{}
	}
	if status.Commits == nil {
		status.Commits = []UpdateCommit{}
	}
	if local.Commit != "" {
		value := local.Commit
		status.CurrentCommit = &value
	}
	if local.Branch != "" {
		value := local.Branch
		status.Branch = &value
	}
	if check.CheckedAt != nil {
		status.CheckedAt = check.CheckedAt
	}
	if check.LatestVersion != "" {
		value := check.LatestVersion
		status.LatestVersion = &value
	}
	if check.LatestCommit != "" {
		value := check.LatestCommit
		status.LatestCommit = &value
	}
	if local.DependencyTool != "" {
		value := local.DependencyTool
		status.DependencyTool = &value
	}
	if check.Source != nil {
		id, name := check.Source.ID, check.Source.Name
		status.SourceID = &id
		status.SourceName = &name
	}
	return status
}

// BlockerMessage 返回阻断项展示文案
func BlockerMessage(code string) string {
	if message, ok := blockerMessages[code]; ok {
		return message
	}
	return code
}

var blockerMessages = map[string]string{
	"uv_missing":                 "未找到 uv，请将 uv 加入 MaiBot 启动环境的 PATH",
	"not_uv_tool":                "当前不是 PyPI 来源的 uv tool 安装，请使用原安装工具更新",
	"package_runner_missing":     "请通过 maibot run 启动，才能自动更新并重启",
	"other_instances_running":    "同一工具环境还有其他实例运行，请先停止它们再更新",
	"package_busy":               "工具环境正在更新，请稍后重试",
	"git_missing":                "未找到 Git，无法自更新",
	"not_git_repo":               "当前部署不是 Git 仓库（如 Docker 镜像或压缩包），请按对应部署方式更新",
	"detached_head":              "当前不在任何分支上（分离头指针），无法自更新",
	"unsupported_branch":         "仅支持在 main / dev 分支上自更新",
	"dirty_worktree":             "存在未提交的本地改动，请先提交或还原后再更新",
	"diverged":                   "本地存在官方分支没有的提交，无法快进更新",
	"no_dependency_tool":         "本次更新包含依赖变更，但未找到可用的依赖安装方式（uv、pip 或 go）",
	"python_requirement_changed": "本次更新修改了 Python 版本要求，请手动更新",
}

func normalizeSourceID(sourceID string) string {
	value := strings.TrimSpace(strings.ToLower(sourceID))
	if value == "" {
		return OfficialSourceID
	}
	return value
}
