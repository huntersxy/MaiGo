package utils

// Python 解释器解析，对齐 Python 侧外部脚本固定使用 sys.executable 的行为
// Python 没有解释器路径配置项；Go 侧额外提供显式配置、环境变量与虚拟环境探测，
// 避免 Debian 等仅提供 python3 别名的环境无法执行外部导入脚本

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// pythonResolverEnv 解析器依赖，测试通过替换实现覆盖不同环境
type pythonResolverEnv struct {
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	getenv   func(string) string
	goos     string
}

// ResolvePythonExecutable 解析外部脚本使用的 Python 解释器
// 顺序为显式配置、MAIBOT_MEMORIX_PYTHON、VIRTUAL_ENV、仓库 .venv/venv、PATH
func ResolvePythonExecutable(configured string, repoRoot string) (string, error) {
	return resolvePythonExecutable(configured, repoRoot, pythonResolverEnv{
		lookPath: exec.LookPath,
		stat:     os.Stat,
		getenv:   os.Getenv,
		goos:     runtime.GOOS,
	})
}

// resolvePythonExecutable 是 ResolvePythonExecutable 的可注入实现
func resolvePythonExecutable(configured, repoRoot string, env pythonResolverEnv) (string, error) {
	if value := strings.TrimSpace(configured); value != "" {
		return resolvePythonCandidate(value, "web.import.python_executable", env)
	}
	if value := strings.TrimSpace(env.getenv("MAIBOT_MEMORIX_PYTHON")); value != "" {
		return resolvePythonCandidate(value, "MAIBOT_MEMORIX_PYTHON", env)
	}
	if venv := strings.TrimSpace(env.getenv("VIRTUAL_ENV")); venv != "" {
		if path, ok := pythonInVenv(venv, env); ok {
			return path, nil
		}
	}
	if root := strings.TrimSpace(repoRoot); root != "" {
		for _, name := range []string{".venv", "venv"} {
			if path, ok := pythonInVenv(filepath.Join(root, name), env); ok {
				return path, nil
			}
		}
	}
	names := []string{"python3", "python"}
	if strings.EqualFold(env.goos, "windows") {
		names = []string{"python", "python3"}
	}
	for _, name := range names {
		if path, err := env.lookPath(name); err == nil && strings.TrimSpace(path) != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("未找到可用的 Python 解释器（已尝试 %s；可设置 MAIBOT_MEMORIX_PYTHON 或 web.import.python_executable）", strings.Join(names, "、"))
}

// resolvePythonCandidate 解析显式配置或环境变量指定的解释器
func resolvePythonCandidate(value, source string, env pythonResolverEnv) (string, error) {
	if strings.ContainsAny(value, "/\\") || filepath.IsAbs(value) {
		path, err := requirePythonExecutable(value, env)
		if err != nil {
			return "", fmt.Errorf("%s 指定的 Python 解释器不可用: %v", source, err)
		}
		return path, nil
	}
	path, err := env.lookPath(value)
	if err != nil || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s 指定的 Python 解释器不可用: 无法在 PATH 中找到 %s", source, value)
	}
	return path, nil
}

// pythonInVenv 返回虚拟环境中可用的 Python 解释器
func pythonInVenv(root string, env pythonResolverEnv) (string, bool) {
	names := []string{"python", "python3"}
	binDir := "bin"
	if strings.EqualFold(env.goos, "windows") {
		names = []string{"python.exe", "python3.exe"}
		binDir = "Scripts"
	}
	for _, name := range names {
		path, err := requirePythonExecutable(filepath.Join(root, binDir, name), env)
		if err == nil {
			return path, true
		}
	}
	return "", false
}

// requirePythonExecutable 校验解释器存在且可执行
func requirePythonExecutable(path string, env pythonResolverEnv) (string, error) {
	info, err := env.stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("是目录: %s", path)
	}
	if !strings.EqualFold(env.goos, "windows") && info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("文件不可执行: %s", path)
	}
	return path, nil
}
