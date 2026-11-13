package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	localSDKPackageName = "maibot-plugin-sdk"
	localSDKImportDir   = "maibot_sdk"
)

// buildPythonPathWithLocalSDK 校验并构造优先包含本地 SDK 的 PYTHONPATH，对齐 local_sdk.build_pythonpath_with_local_sdk
func buildPythonPathWithLocalSDK(env map[string]string) (string, bool, error) {
	raw := strings.TrimSpace(env[EnvLocalPluginSDKPath])
	if raw == "" {
		return "", false, nil
	}
	sdkPath, err := resolveLocalSDKPath(raw)
	if err != nil {
		return "", false, err
	}
	existing := strings.Split(env["PYTHONPATH"], string(os.PathListSeparator))
	entries := make([]string, 0, len(existing)+1)
	entries = append(entries, sdkPath)
	for _, entry := range existing {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if sameLocalSDKPath(entry, sdkPath) {
			continue
		}
		entries = append(entries, entry)
	}
	return strings.Join(entries, string(os.PathListSeparator)), true, nil
}

// resolveLocalSDKPath 展开环境变量与用户目录，并校验本地 SDK 仓库
func resolveLocalSDKPath(raw string) (string, error) {
	expanded := os.ExpandEnv(strings.TrimSpace(raw))
	expanded = expandHomePath(expanded)
	if !filepath.IsAbs(expanded) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("解析本地插件 SDK 相对路径失败: %w", err)
		}
		expanded = filepath.Join(cwd, expanded)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("解析本地插件 SDK 路径失败: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	if err := validateLocalSDKPath(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func expandHomePath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, "~\\") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func validateLocalSDKPath(sdkPath string) error {
	if st, err := os.Stat(sdkPath); err != nil || !st.IsDir() {
		return fmt.Errorf("%s 指向的目录不存在: %s", EnvLocalPluginSDKPath, sdkPath)
	}
	if st, err := os.Stat(filepath.Join(sdkPath, localSDKImportDir)); err != nil || !st.IsDir() {
		return fmt.Errorf("%s 必须指向包含 %s/ 的 SDK 仓库: %s", EnvLocalPluginSDKPath, localSDKImportDir, sdkPath)
	}
	pyprojectPath := filepath.Join(sdkPath, "pyproject.toml")
	raw, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return fmt.Errorf("读取本地 SDK pyproject.toml 失败: %s: %w", pyprojectPath, err)
	}
	var document map[string]any
	if err := toml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("读取本地 SDK pyproject.toml 失败: %s: %w", pyprojectPath, err)
	}
	project, ok := document["project"].(map[string]any)
	if !ok {
		return fmt.Errorf("本地 SDK pyproject.toml 缺少 [project]: %s", pyprojectPath)
	}
	name := strings.TrimSpace(fmt.Sprint(project["name"]))
	if name != localSDKPackageName {
		return fmt.Errorf("%s 指向的项目不是 %s: %s", EnvLocalPluginSDKPath, localSDKPackageName, sdkPath)
	}
	version := strings.TrimSpace(fmt.Sprint(project["version"]))
	if version == "" || version == "<nil>" {
		return fmt.Errorf("%s 指向的 SDK 缺少 project.version: %s", EnvLocalPluginSDKPath, sdkPath)
	}
	return nil
}

func sameLocalSDKPath(entry, sdkPath string) bool {
	candidate := strings.TrimSpace(entry)
	if candidate == "" {
		return false
	}
	if !filepath.IsAbs(candidate) {
		if cwd, err := os.Getwd(); err == nil {
			candidate = filepath.Join(cwd, candidate)
		}
	}
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}
	candidate = filepath.Clean(candidate)
	target := filepath.Clean(sdkPath)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(candidate, target)
	}
	return candidate == target
}
