package host

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultRunnerModule 与 MaiBot supervisor 的 python -m 入口一致
const DefaultRunnerModule = "src.plugin_runtime.runner.runner_main"

// SpawnConfig 从环境变量解析的 Runner 拉起配置
type SpawnConfig struct {
	Enabled     bool
	PythonBin   string
	ScriptPath  string
	ProjectRoot string
}

// envDefaultOn 未设置时返回 true，显式设为 0/false/no/off 才关闭
func envDefaultOn(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// LoadSpawnConfigFromEnv 读取 MAIBOT_PLUGIN_SPAWN 与 Python/脚本路径默认值
// Runner 默认拉起：拉起失败只降级为宿主内置组件并告警，不阻断启动。
// 需要纯宿主模式时显式设 MAIBOT_PLUGIN_SPAWN=0。
func LoadSpawnConfigFromEnv() SpawnConfig {
	root := resolvePluginProjectRoot("")
	cfg := SpawnConfig{
		Enabled:     envDefaultOn(EnvPluginSpawn),
		PythonBin:   strings.TrimSpace(os.Getenv(EnvPluginPython)),
		ScriptPath:  strings.TrimSpace(os.Getenv(EnvPluginRunnerScript)),
		ProjectRoot: root,
	}
	if cfg.PythonBin == "" {
		cfg.PythonBin = DefaultPythonBin(root)
	}
	if cfg.ScriptPath == "" {
		cfg.ScriptPath = DefaultRunnerModule
	}
	// 项目根可能来自自动探测；写回环境变量，供配置解析与依赖同步复用同一口径
	if cfg.ProjectRoot != "" && strings.TrimSpace(os.Getenv(EnvPluginProjectRoot)) == "" {
		_ = os.Setenv(EnvPluginProjectRoot, cfg.ProjectRoot)
	}
	return cfg
}

// resolvePluginProjectRoot 解析 MaiBot Python 项目根（需含 src/plugin_runtime）
func resolvePluginProjectRoot(override string) string {
	if override = strings.TrimSpace(override); override != "" {
		if isMaiBotProjectRoot(override) {
			return override
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvPluginProjectRoot)); v != "" && isMaiBotProjectRoot(v) {
		return v
	}
	if cwd, err := os.Getwd(); err == nil {
		for _, rel := range []string{"../MaiBot", "../../MaiBot", "MaiBot"} {
			candidate := filepath.Clean(filepath.Join(cwd, rel))
			if isMaiBotProjectRoot(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func isMaiBotProjectRoot(dir string) bool {
	if dir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, "src", "plugin_runtime", "runner", "runner_main.py"))
	return err == nil && !st.IsDir()
}

// DefaultPythonBin 返回默认的 Python 解释器
func DefaultPythonBin(projectRoot string) string {
	if projectRoot != "" {
		var venvPython string
		if runtime.GOOS == "windows" {
			venvPython = filepath.Join(projectRoot, ".venv", "Scripts", "python.exe")
		} else {
			venvPython = filepath.Join(projectRoot, ".venv", "bin", "python")
		}
		if st, err := os.Stat(venvPython); err == nil && !st.IsDir() {
			return venvPython
		}
	}
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
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
