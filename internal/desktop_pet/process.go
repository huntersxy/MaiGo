package desktoppet

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"MaiGo/internal/plugin_runtime"
)

func lookPathPython(bin string) (string, error) {
	bin = strings.TrimSpace(bin)
	if bin == "" {
		return "", fmt.Errorf("未指定 Python 解释器")
	}
	if filepath.IsAbs(bin) {
		if st, err := os.Stat(bin); err != nil || st.IsDir() {
			return "", fmt.Errorf("Python 解释器不存在: %q", bin)
		}
		return bin, nil
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("找不到 Python 可执行文件 %q: %w", bin, err)
	}
	return path, nil
}

// buildPetCommand 构造桌宠子进程并绑定 stdin/stdout/stderr，调用方负责 Start
// 与 pet_window.py 的管道协议一致：stdout 只走 JSON 事件，stderr 只记日志
func buildPetCommand(spawn SpawnEnv) (*exec.Cmd, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	projectRoot := strings.TrimSpace(spawn.ProjectRoot)
	if projectRoot == "" {
		return nil, nil, nil, nil, fmt.Errorf("未找到 MaiBot Python 项目根，请把 %s 指向含 src/plugin_runtime 的目录", pluginruntime.EnvPluginProjectRoot)
	}
	script := petScriptPath(projectRoot)
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return nil, nil, nil, nil, fmt.Errorf("桌宠脚本不存在: %s", script)
	}

	instanceRoot := strings.TrimSpace(spawn.InstanceRoot)
	if instanceRoot == "" {
		instanceRoot = projectRoot
	}
	dataDir := petDataDir(instanceRoot)
	runtimeDir := petRuntimeDir(instanceRoot)
	for _, dir := range []string{dataDir, runtimeDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("创建桌宠目录失败 %q: %w", dir, err)
		}
	}

	python, err := lookPathPython(spawn.PythonBin)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	cmd := exec.Command(python, script, "--data-dir", dataDir, "--runtime-dir", runtimeDir)
	cmd.Dir = projectRoot
	cmd.Env = os.Environ()
	cmd.SysProcAttr = petSysProcAttr()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("绑定桌宠 stdin 失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("绑定桌宠 stdout 失败: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("绑定桌宠 stderr 失败: %w", err)
	}
	return cmd, stdin, stdout, stderr, nil
}
