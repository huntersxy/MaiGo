package pluginmarket

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// errGitNotFound 对齐 FileNotFoundError：Git 未安装或不在 PATH 中
var errGitNotFound = errors.New("git 未安装或不在 PATH 中")

// cloneOutcome 对齐 git_mirror_service.clone_repository 的返回结构
type cloneOutcome struct {
	Success    bool
	Path       string
	Error      string
	MirrorUsed string
	URL        string
	Attempts   int
	StatusCode int
}

// cloneRepositoryFailure 对齐失败返回值
func cloneRepositoryFailure(message string, statusCode int) cloneOutcome {
	return cloneOutcome{Error: message, StatusCode: statusCode}
}

// validateDownloadURL 只校验 URL 格式；管理员配置的下载源可指向本机或内网服务
func (s *Service) validateDownloadURL(targetURL, _ string) (string, error) {
	options := s.guardForRequest()
	options.RequirePublicNetwork = false
	validated, err := validatePublicURL(targetURL, options)
	if err != nil {
		return "", fmt.Errorf("目标 URL 非法: %v", err)
	}
	return validated, nil
}

// cloneRepositoryHook 测试替身，参数与 cloneRepository 一致
type cloneRepositoryHook func(ctx context.Context, owner, repo, targetPath, branch, customURL string, depth int, operation, pluginID string) cloneOutcome

// cloneRepository 对齐 GitMirrorService.clone_repository：只使用设置中选中的下载源
func (s *Service) cloneRepository(ctx context.Context, owner, repo, targetPath, branch, customURL string, depth int, operation, pluginID string) cloneOutcome {
	if s.cloneHook != nil {
		return s.cloneHook(ctx, owner, repo, targetPath, branch, customURL, depth, operation, pluginID)
	}
	if strings.TrimSpace(customURL) != "" {
		validated, err := s.validateDownloadURL(customURL, "自定义源")
		if err != nil {
			return cloneRepositoryFailure(err.Error(), 400)
		}
		return s.cloneWithURL(ctx, validated, targetPath, branch, depth, "custom", operation, pluginID, "自定义源", 1, 1)
	}
	source, err := s.downloadSource()
	if err != nil {
		return cloneRepositoryFailure(err.Error(), 400)
	}
	cloneURL, err := source.CloneURL(owner, repo)
	if err != nil {
		return cloneRepositoryFailure(err.Error(), 400)
	}
	return s.cloneWithURL(ctx, cloneURL, targetPath, branch, depth, source.ID, operation, pluginID, source.Name, 1, 1)
}

// cloneWithURL 对齐 _clone_with_url：失败重试并清理半成品目录
func (s *Service) cloneWithURL(ctx context.Context, targetURL, targetPath, branch string, depth int, mirrorType, operation, pluginID, mirrorName string, mirrorIndex, totalMirrors int) cloneOutcome {
	attempts := 0
	lastError := ""
	retries := s.gitMaxRetries
	if retries < 1 {
		retries = 1
	}
	for attempt := 0; attempt < retries; attempt++ {
		attempts++
		if _, err := os.Stat(targetPath); err == nil {
			if cleanupErr := RemoveTree(targetPath); cleanupErr != nil {
				zap.L().Warn("清理上次克隆残留失败", zap.String("path", targetPath), zap.Error(cleanupErr))
			}
		}
		args := []string{"clone"}
		if branch != "" {
			args = append(args, "-b", branch)
		}
		if depth > 0 {
			args = append(args, "--depth", strconv.Itoa(depth))
		}
		args = append(args, targetURL, targetPath)

		mirrorProgressBase := 20
		if totalMirrors > 0 {
			mirrorProgressBase = 20 + int(float64(mirrorIndex-1)/float64(totalMirrors)*60)
		}
		attemptProgress := int(float64(attempt) / math.Max(float64(retries), 1) * 15)
		progress := mirrorProgressBase + attemptProgress
		if progress > 82 {
			progress = 82
		}
		s.reportProgress(ProgressUpdate{
			Operation:    operation,
			Stage:        "loading",
			Progress:     progress,
			Message:      fmt.Sprintf("正在从 %s 克隆仓库（镜像源 %d/%d，尝试 %d/%d）...", mirrorName, mirrorIndex, totalMirrors, attempt+1, retries),
			PluginID:     stringPointer(pluginID),
			MirrorID:     stringPointer(mirrorType),
			MirrorName:   stringPointer(mirrorName),
			MirrorIndex:  intPointer(mirrorIndex),
			TotalMirrors: intPointer(totalMirrors),
			Attempt:      intPointer(attempt + 1),
			MaxAttempts:  intPointer(retries),
		})

		_, stderrText, err := runGitCommand(ctx, "", 300*time.Second, args...)
		if err == nil {
			s.reportProgress(ProgressUpdate{
				Operation:    operation,
				Stage:        "loading",
				Progress:     82,
				Message:      fmt.Sprintf("已从 %s 克隆完成，正在校验插件文件...", mirrorName),
				PluginID:     stringPointer(pluginID),
				MirrorID:     stringPointer(mirrorType),
				MirrorName:   stringPointer(mirrorName),
				MirrorIndex:  intPointer(mirrorIndex),
				TotalMirrors: intPointer(totalMirrors),
				Attempt:      intPointer(attempts),
				MaxAttempts:  intPointer(retries),
			})
			return cloneOutcome{Success: true, Path: targetPath, MirrorUsed: mirrorType, Attempts: attempts, URL: targetURL}
		}
		switch {
		case errors.Is(err, errGitNotFound):
			lastError = "Git 未安装或不在 PATH 中"
			zap.L().Error("Git 未找到", zap.String("error", lastError))
			return cloneOutcome{Success: false, Error: lastError, MirrorUsed: mirrorType, Attempts: attempts, URL: targetURL}
		case errors.Is(err, context.DeadlineExceeded):
			lastError = "克隆超时（超过 5 分钟）"
		default:
			lastError = "Git 克隆失败: " + strings.TrimSpace(stderrText)
			if strings.TrimSpace(stderrText) == "" {
				lastError = "Git 克隆失败: " + err.Error()
			}
		}
		zap.L().Warn("克隆失败",
			zap.Int("attempt", attempt+1), zap.Int("max_attempts", retries), zap.String("error", lastError))
		if _, statErr := os.Stat(targetPath); statErr == nil {
			if cleanupErr := RemoveTree(targetPath); cleanupErr != nil {
				zap.L().Warn("清理克隆失败残留失败", zap.String("path", targetPath), zap.Error(cleanupErr))
			}
		}
		if attempt+1 < retries {
			retryProgress := mirrorProgressBase + int(float64(attempt+1)/float64(retries)*15)
			if retryProgress > 82 {
				retryProgress = 82
			}
			errorCopy := lastError
			s.reportProgress(ProgressUpdate{
				Operation:    operation,
				Stage:        "loading",
				Progress:     retryProgress,
				Message:      fmt.Sprintf("%s 克隆失败，准备重试 %d/%d", mirrorName, attempt+2, retries),
				Error:        &errorCopy,
				PluginID:     stringPointer(pluginID),
				MirrorID:     stringPointer(mirrorType),
				MirrorName:   stringPointer(mirrorName),
				MirrorIndex:  intPointer(mirrorIndex),
				TotalMirrors: intPointer(totalMirrors),
				Attempt:      intPointer(attempt + 1),
				MaxAttempts:  intPointer(retries),
			})
		}
	}
	return cloneOutcome{Success: false, Error: lastError, MirrorUsed: mirrorType, Attempts: attempts, URL: targetURL}
}

func intPointer(value int) *int {
	return &value
}

// runGitCommand 执行 Git 命令，返回标准输出与标准错误
//
// 对齐 Python _run_git_process：超时或取消时终止整棵进程树（git 会拉起 ssh/credential helper），
// 只杀父进程会留下孤儿子进程继续持有锁或网络连接。
func runGitCommand(ctx context.Context, workDir string, timeout time.Duration, args ...string) (string, string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", "", errGitNotFound
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.SysProcAttr = gitSysProcAttr()
	// 取消默认的仅杀父进程行为，改为杀进程组；WaitDelay 防止 stdout/stderr 被孙进程长期占用
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return killGitProcessTree(cmd.Process.Pid)
	}
	cmd.WaitDelay = 5 * time.Second
	if workDir != "" {
		cmd.Dir = workDir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if commandCtx.Err() == context.DeadlineExceeded {
		return stdout.String(), stderr.String(), context.DeadlineExceeded
	}
	return stdout.String(), stderr.String(), err
}

// RunGitCommand 执行 Git 命令，供 WebUI 插件安装等外部调用方复用同一超时与进程树终止语义
func RunGitCommand(ctx context.Context, workDir string, timeout time.Duration, args ...string) (string, string, error) {
	return runGitCommand(ctx, workDir, timeout, args...)
}

// runPluginGit 对齐 release_install._git：在插件目录执行 Git 命令
func runPluginGit(ctx context.Context, pluginPath string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", pluginPath}, args...)
	stdout, stderrText, err := runGitCommand(ctx, "", 120*time.Second, fullArgs...)
	if err != nil {
		return "", badRequestError("Git 操作失败：%s", strings.TrimSpace(stderrText))
	}
	return stdout, nil
}

// checkTree 对齐 _check_tree：拒绝链接，确保只访问工作目录内的真实文件
func checkTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				relative = path
			}
			return badRequestError("插件目录不能包含链接：%s", relative)
		}
		return nil
	})
}

// workDirectory 对齐 _work_directory：插件根目录下的固定工作目录
func workDirectory(pluginRoot, name string) (string, error) {
	rootResolved, err := filepath.Abs(pluginRoot)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(rootResolved, name)
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", badRequestError("插件工作目录越界或包含链接：%s", name)
	}
	if !isPathWithin(rootResolved, directory) {
		return "", badRequestError("插件工作目录越界或包含链接：%s", name)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	return directory, nil
}
