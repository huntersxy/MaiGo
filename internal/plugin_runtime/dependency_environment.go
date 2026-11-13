package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

const pythonInstalledPackagesScript = `import importlib.metadata as metadata
import json
result = {}
for distribution in metadata.distributions():
    name = distribution.metadata["Name"]
    if not name:
        raise RuntimeError("已安装包缺少 Name 元数据")
    result[name] = distribution.version
print(json.dumps(result, ensure_ascii=False))
`

// loadInstalledPythonPackages 查询插件 Runner 所在 Python 环境已安装的包版本
func (p *PluginDependencyPipeline) loadInstalledPythonPackages(ctx context.Context) (map[string]string, error) {
	pythonBin := strings.TrimSpace(p.PythonBin)
	if pythonBin == "" {
		pythonBin = defaultPythonBin(p.ProjectRoot)
	}
	result, err := p.runDependencyCommand(ctx, pythonBin, []string{"-c", pythonInstalledPackagesScript}, p.ProjectRoot)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("查询 Python 环境失败（退出码 %d）: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	if strings.TrimSpace(result.Stdout) == "" {
		return nil, fmt.Errorf("Python 环境查询没有输出: %s", strings.TrimSpace(result.Stderr))
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(result.Stdout), &raw); err != nil {
		return nil, fmt.Errorf("解析 Python 环境包版本失败: %w", err)
	}
	installed := make(map[string]string, len(raw))
	for name, version := range raw {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("已安装包缺少 Name 元数据")
		}
		installed[CanonicalizePythonPackageName(name)] = version
	}
	if sdkVersion := p.localSDKVersion(); sdkVersion != "" {
		installed[CanonicalizePythonPackageName("maibot-plugin-sdk")] = sdkVersion
	}
	return installed, nil
}

func (p *PluginDependencyPipeline) installedVersionMap(ctx context.Context) (map[string]string, error) {
	if p.InstalledVersions != nil {
		installed := make(map[string]string, len(p.InstalledVersions))
		for name, version := range p.InstalledVersions {
			installed[CanonicalizePythonPackageName(name)] = version
		}
		if sdkVersion := p.localSDKVersion(); sdkVersion != "" {
			installed[CanonicalizePythonPackageName("maibot-plugin-sdk")] = sdkVersion
		}
		return installed, nil
	}
	if p.InstalledVersionsLoader != nil {
		loaded, err := p.InstalledVersionsLoader(ctx)
		if err != nil {
			return nil, err
		}
		installed := make(map[string]string, len(loaded))
		for name, version := range loaded {
			installed[CanonicalizePythonPackageName(name)] = version
		}
		if sdkVersion := p.localSDKVersion(); sdkVersion != "" {
			installed[CanonicalizePythonPackageName("maibot-plugin-sdk")] = sdkVersion
		}
		return installed, nil
	}
	return p.loadInstalledPythonPackages(ctx)
}

// localSDKVersion 读取本地插件 SDK 的项目版本，对齐 read_local_sdk_version
func (p *PluginDependencyPipeline) localSDKVersion() string {
	path := strings.TrimSpace(os.Getenv(EnvLocalPluginSDKPath))
	if path == "" && p.ProjectRoot != "" {
		path = filepath.Join(p.ProjectRoot, "packages", "maibot-plugin-sdk")
	}
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	pyprojectPath := path
	if info.IsDir() {
		pyprojectPath = filepath.Join(path, "pyproject.toml")
	}
	raw, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return ""
	}
	var payload struct {
		Project struct {
			Version string `toml:"version"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Project.Version)
}

// loadInstalledPackageConstraints 快照当前解释器的已安装版本，同时约束直接与间接依赖
func (p *PluginDependencyPipeline) loadInstalledPackageConstraints(ctx context.Context) ([]string, error) {
	installed, err := p.installedVersionMap(ctx)
	if err != nil {
		return nil, err
	}
	constraints := make([]string, 0, len(installed))
	for name, version := range installed {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("已安装包缺少 Name 元数据")
		}
		constraints = append(constraints, name+"=="+version)
	}
	sortStrings(constraints)
	return constraints, nil
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func defaultDependencyCommandRunner(ctx context.Context, executable string, args []string, dir string) (DependencyCommandResult, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = dir
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := DependencyCommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		result.ExitCode = 0
	} else {
		result.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result, err
}
