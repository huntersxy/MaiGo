package runner

import (
	"path/filepath"
	"strings"
)

// DefaultRunnerModule 默认 Runner 模块名。父包同名常量与此保持一致。
const DefaultRunnerModule = "maibot_plugin_runner"

// ExecArgs 将 ScriptPath 转为传给 python 的参数（-m 模块或 .py 脚本）
func ExecArgs(scriptPath string) []string {
	return runnerExecArgs(scriptPath)
}

// WorkingDir 决定子进程 cwd：模块模式用项目根，脚本模式用脚本所在目录或项目根
func WorkingDir(projectRoot, scriptPath string) string {
	return runnerWorkingDir(projectRoot, scriptPath)
}

// runnerExecArgs 将 ScriptPath 转为传给 python 的参数（-m 模块或 .py 脚本）
func runnerExecArgs(scriptPath string) []string {
	scriptPath = strings.TrimSpace(scriptPath)
	if scriptPath == "" {
		return []string{"-m", DefaultRunnerModule}
	}
	if strings.HasPrefix(scriptPath, "-m") {
		return strings.Fields(scriptPath)
	}
	lower := strings.ToLower(scriptPath)
	if strings.HasSuffix(lower, ".py") {
		return []string{scriptPath}
	}
	return []string{"-m", scriptPath}
}

// runnerWorkingDir 决定子进程 cwd：模块模式用项目根，脚本模式用脚本所在目录或项目根
func runnerWorkingDir(projectRoot, scriptPath string) string {
	scriptPath = strings.TrimSpace(scriptPath)
	if strings.HasSuffix(strings.ToLower(scriptPath), ".py") {
		if abs, err := filepath.Abs(scriptPath); err == nil {
			return filepath.Dir(abs)
		}
	}
	if projectRoot != "" {
		return projectRoot
	}
	return ""
}
