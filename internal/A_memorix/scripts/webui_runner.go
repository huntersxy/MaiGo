package scripts

// WebUI 批处理命令执行器
// 保留 WebImportCommandRunner 的接口形状，进程内直接调用 Go RunX，不再启动 Python 子进程

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// WebImportCommandRunner 供 internal/memorix/core/utils.WebImportDeps 使用
type WebImportCommandRunner struct{}

// HasScript 供 WebImportManager 判断脚本已由 Go 原生实现
func (WebImportCommandRunner) HasScript(name string) bool {
	return HasCommand(name)
}

// Run 识别 Python 迁移/转换命令并直接执行 Go 实现
func (WebImportCommandRunner) Run(ctx context.Context, command []string, workingDir string) (int, string, string, error) {
	if len(command) == 0 {
		return -1, "", "", errors.New("命令为空")
	}
	scriptName, scriptArgs, probe, err := resolveWebImportCommand(command)
	if err != nil {
		return -1, "", "", err
	}
	if probe {
		// Go 转换器不依赖 scipy/pyarrow，预检直接视为通过
		return 0, "OK", "", nil
	}
	if !HasCommand(scriptName) {
		return -1, "", "", fmt.Errorf("WebUI 不支持非原生脚本: %s", scriptName)
	}
	_ = workingDir // 命令参数均为绝对路径，进程内执行不切换全局工作目录
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code, ok := RunCommandWithIO(ctx, scriptName, scriptArgs, &stdout, &stderr)
	if !ok {
		return -1, "", "", fmt.Errorf("WebUI 不支持非原生脚本: %s", scriptName)
	}
	return code, stdout.String(), stderr.String(), nil
}

// resolveWebImportCommand 从 Python 命令行或裸脚本命令中提取脚本名与参数
func resolveWebImportCommand(command []string) (string, []string, bool, error) {
	first := strings.TrimSpace(command[0])
	if isPythonExecutable(first) {
		if len(command) >= 3 && command[1] == "-c" {
			if isConvertPreflightProbe(command[2]) {
				return "", nil, true, nil
			}
			return "", nil, false, errors.New("WebUI 不再执行 Python 内联代码预检")
		}
		if len(command) < 2 {
			return "", nil, false, errors.New("Python 命令缺少脚本路径")
		}
		return command[1], command[2:], false, nil
	}
	if strings.HasSuffix(strings.ToLower(first), ".py") {
		return first, command[1:], false, nil
	}
	if HasCommand(first) {
		return first, command[1:], false, nil
	}
	return "", nil, false, fmt.Errorf("无法识别的 WebUI 外部命令: %s", strings.Join(command, " "))
}

// isPythonExecutable 判断可执行名是否为 Python 解释器
func isPythonExecutable(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	base = strings.TrimSuffix(base, ".exe")
	return base == "python" || base == "python3" || base == "py"
}

// isConvertPreflightProbe 识别 convert 流程的 scipy/pyarrow 探测代码
func isConvertPreflightProbe(code string) bool {
	return strings.Contains(code, "scipy") && strings.Contains(code, "pyarrow")
}
