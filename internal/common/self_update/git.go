package selfupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	gitDefaultTimeout = 120 * time.Second
	gitFetchTimeout   = 300 * time.Second
	maxListedCommits  = 30
	maxDirtyFiles     = 20
)

var gitStallOptions = []string{"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=30"}

type gitResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func runGit(ctx context.Context, root string, timeout time.Duration, args ...string) (gitResult, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	command := exec.CommandContext(runCtx, "git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := gitResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		return result, nil
	}
	commandText := "git " + strings.Join(args, " ")
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return result, errorf("git_timeout", "Git 命令超时（%g 秒）: %s\n%s", timeout.Seconds(), commandText, gitDiagnostics(result))
	}
	return result, errorf("git_failed", "Git 命令失败: %s\n%s", commandText, gitDiagnostics(result))
}

func gitDiagnostics(result gitResult) string {
	text := strings.TrimSpace(result.Stderr)
	if text == "" {
		text = strings.TrimSpace(result.Stdout)
	}
	if len(text) > 4000 {
		text = text[len(text)-4000:]
	}
	if text == "" {
		return "命令没有输出"
	}
	return text
}

func gitOutput(root string, timeout time.Duration, args ...string) (string, error) {
	ctx := context.Background()
	result, err := runGit(ctx, root, timeout, args...)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func gitRevParse(ctx context.Context, root, revision string) (string, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "rev-parse", revision)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func gitCurrentBranch(ctx context.Context, root string) (string, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", errorf("detached_head", "%s", BlockerMessage("detached_head"))
	}
	return strings.TrimSpace(result.Stdout), nil
}

func gitDirtyFiles(ctx context.Context, root string) ([]string, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) <= 3 {
			continue
		}
		files = append(files, line[3:])
		if len(files) >= maxDirtyFiles {
			break
		}
	}
	return files, nil
}

func isAncestor(ctx context.Context, root, ancestor, descendant string) (bool, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		if result.ExitCode == 1 {
			return false, nil
		}
		return false, errorf("git_failed", "无法判断提交关系: %s", gitDiagnostics(result))
	}
	return true, nil
}

func changedFiles(ctx context.Context, root, base, target string) (map[string]struct{}, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "diff", "--name-only", base, target)
	if err != nil {
		return nil, err
	}
	files := make(map[string]struct{})
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files[line] = struct{}{}
		}
	}
	return files, nil
}

func listCommits(ctx context.Context, root, base, target string) ([]UpdateCommit, error) {
	format := strings.Join([]string{"%h", "%s", "%cI"}, "\x1f")
	result, err := runGit(ctx, root, gitDefaultTimeout, "log", "--format="+format, "-n", "30", base+".."+target)
	if err != nil {
		return nil, err
	}
	commits := make([]UpdateCommit, 0)
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		commits = append(commits, UpdateCommit{ShortID: parts[0], Summary: parts[1], CommittedAt: parts[2]})
	}
	return commits, nil
}

func gitShowFile(ctx context.Context, root, commit, path string) (string, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "show", commit+":"+path)
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func gitCatFileCommit(ctx context.Context, root, commit string) (bool, error) {
	result, err := runGit(ctx, root, gitDefaultTimeout, "cat-file", "-e", commit+"^{commit}")
	if err != nil {
		if result.ExitCode > 0 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func gitFetchBranch(ctx context.Context, root, sourceID, sourceURL, branch string) (string, error) {
	args := make([]string, 0, 16)
	if sourceID == "cnb" {
		args = append(args, "-c", "credential.helper=", "-c", "http.extraHeader=", "-c", "http.https://cnb.cool/.extraHeader=")
	}
	args = append(args, gitStallOptions...)
	args = append(args, "fetch", "--no-tags", "--quiet", sourceURL, "refs/heads/"+branch)
	if _, err := runGit(ctx, root, gitFetchTimeout, args...); err != nil {
		return "", errorf("fetch_failed", "%s 获取更新失败：%s", sourceDisplayName(sourceID), err.Error())
	}
	return gitRevParse(ctx, root, "FETCH_HEAD^{commit}")
}

func gitMergeFF(ctx context.Context, root, target string) error {
	_, err := runGit(ctx, root, gitDefaultTimeout, "merge", "--ff-only", target)
	return err
}

func gitResetHard(ctx context.Context, root, commit string) error {
	_, err := runGit(ctx, root, gitDefaultTimeout, "reset", "--hard", commit)
	return err
}

func resolveRoot(root string) (string, error) {
	value := strings.TrimSpace(root)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	}
	if value == "" {
		value = strings.TrimSpace(os.Getenv("MAIBOT_PROJECT_ROOT"))
	}
	if value == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		value = cwd
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func pathExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info != nil
}

func sourceDisplayName(sourceID string) string {
	switch sourceID {
	case "cnb":
		return "CNB 镜像"
	case "custom":
		return "自定义源"
	default:
		return "GitHub 官方源"
	}
}

func validateCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
