package pluginruntime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// DependencyCommandResult 是依赖安装与 Python 环境查询命令的结果
type DependencyCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// DependencyCommandRunner 执行依赖安装或环境查询命令
type DependencyCommandRunner func(ctx context.Context, executable string, args []string, dir string) (DependencyCommandResult, error)

func (p *PluginDependencyPipeline) runDependencyCommand(ctx context.Context, executable string, args []string, dir string) (DependencyCommandResult, error) {
	if p.CommandRunner != nil {
		return p.CommandRunner(ctx, executable, args, dir)
	}
	return defaultDependencyCommandRunner(ctx, executable, args, dir)
}

func (p *PluginDependencyPipeline) installRequirements(ctx context.Context, requirements []CombinedPackageRequirement) (bool, string) {
	requirementTexts := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		requirementTexts = append(requirementTexts, requirement.RequirementText)
	}
	if len(requirementTexts) == 0 {
		return true, ""
	}

	zap.L().Info("开始自动安装插件 Python 依赖", zap.Strings("requirements", requirementTexts))
	indexSettings, err := p.LoadPackageIndexSettings()
	if err != nil {
		return false, "读取依赖安装配置或当前环境版本失败，已取消自动安装: " + err.Error()
	}
	installedConstraints, err := p.loadInstalledPackageConstraints(ctx)
	if err != nil {
		return false, "读取依赖安装配置或当前环境版本失败，已取消自动安装: " + err.Error()
	}

	indexURLs := indexSettings.IndexURLs
	if len(indexURLs) == 0 {
		// 与 Python 一致：没有配置索引时只尝试一次安装工具默认源
		indexURLs = []string{""}
	}
	tempDir, err := os.MkdirTemp("", "maibot-plugin-deps-")
	if err != nil {
		return false, err.Error()
	}
	defer os.RemoveAll(tempDir)

	constraints := append([]string{}, indexSettings.Constraints...)
	constraints = append(constraints, installedConstraints...)
	constraintPath, err := writeDependencyConstraintFile(tempDir, constraints)
	if err != nil {
		return false, err.Error()
	}

	installErrors := make([]string, 0, len(indexURLs))
	for attemptIndex, indexURL := range indexURLs {
		sourceName := indexURL
		if sourceName == "" {
			sourceName = "默认源"
		}
		executable, args := p.buildInstallCommand(requirementTexts, indexURL, constraintPath)
		result, runErr := p.runDependencyCommand(ctx, executable, args, p.ProjectRoot)
		if runErr != nil {
			if result.ExitCode < 0 {
				// 启动失败等异常与 Python subprocess.run 抛异常一致，立即返回
				return false, runErr.Error()
			}
		}
		if runErr == nil && result.ExitCode == 0 {
			zap.L().Info("插件 Python 依赖自动安装完成", zap.String("source", sourceName))
			return true, ""
		}

		output := summarizeDependencyInstallError(result.Stdout, result.Stderr)
		if output == "" {
			output = fmt.Sprintf("命令执行失败，退出码 %d", result.ExitCode)
		}
		installErrors = append(installErrors, "["+sourceName+"] "+output)
		if attemptIndex+1 < len(indexURLs) {
			zap.L().Warn("从当前索引安装插件 Python 依赖失败，改用下一个源重试",
				zap.String("source", sourceName), zap.String("error", output))
		}
	}
	return false, strings.Join(installErrors, "；")
}

func (p *PluginDependencyPipeline) buildInstallCommand(
	requirementTexts []string,
	indexURL string,
	constraintPath string,
) (string, []string) {
	var executable string
	var args []string
	if uvPath := p.resolveUVBinary(); uvPath != "" {
		executable = uvPath
		args = []string{"pip", "install", "--python", p.resolvePythonBin()}
		if indexURL != "" {
			// 与 Python 一致：--no-config 才能只查命令行指定的这一个源
			args = append(args, "--no-config", "--index-url", indexURL)
		}
	} else {
		executable = p.resolvePythonBin()
		args = []string{"-m", "pip", "install"}
		if indexURL != "" {
			args = append(args, "--index-url", indexURL)
		}
	}
	if constraintPath != "" {
		args = append(args, "--constraint", constraintPath)
	}
	args = append(args, requirementTexts...)
	return executable, args
}

func (p *PluginDependencyPipeline) resolvePythonBin() string {
	if pythonBin := strings.TrimSpace(p.PythonBin); pythonBin != "" {
		return pythonBin
	}
	return defaultPythonBin(p.ProjectRoot)
}

func (p *PluginDependencyPipeline) resolveUVBinary() string {
	if uvBinary := strings.TrimSpace(os.Getenv("MAIBOT_UV_BINARY")); uvBinary != "" {
		return uvBinary
	}
	uvPath, err := exec.LookPath("uv")
	if err != nil {
		return ""
	}
	return uvPath
}

func writeDependencyConstraintFile(directory string, constraints []string) (string, error) {
	if len(constraints) == 0 {
		return "", nil
	}
	constraintPath := filepath.Join(directory, "constraints.txt")
	content := strings.Join(constraints, "\n") + "\n"
	if err := os.WriteFile(constraintPath, []byte(content), 0o600); err != nil {
		return "", err
	}
	return constraintPath, nil
}

func summarizeDependencyInstallError(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(stderr); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(stdout); trimmed != "" {
		parts = append(parts, trimmed)
	}
	merged := strings.TrimSpace(strings.Join(parts, "\n"))
	if merged == "" {
		return ""
	}
	lines := make([]string, 0)
	for _, line := range strings.Split(merged, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return strings.Join(lines, " | ")
}
