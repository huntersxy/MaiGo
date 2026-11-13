package dashboardstatic

import (
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

const (
	envDashboardDir = "MAIBOT_DASHBOARD_DIR"
	// envDashboardPackageDir 指向已安装 maibot_dashboard 包目录，对齐 Python 的包内 dist
	envDashboardPackageDir = "MAIBOT_DASHBOARD_PACKAGE_DIR"
	// envPythonPrefix 指定 Python 安装前缀，用于定位 site-packages
	envPythonPrefix = "MAIBOT_PYTHON_PREFIX"
	// dashboardPackageName 对齐 Python _DASHBOARD_PACKAGE_NAME
	dashboardPackageName = "maibot_dashboard"
)

// ResolveRoot 返回可用的 dashboard dist 目录；不存在则 ("", false)
func ResolveRoot(instanceDir string) (string, bool) {
	if explicit := strings.TrimSpace(os.Getenv(envDashboardDir)); explicit != "" {
		if validDashboardRoot(explicit) {
			return filepath.Clean(explicit), true
		}
		zap.L().Warn("MAIBOT_DASHBOARD_DIR 无效，需要包含 index.html 的构建目录",
			zap.String("path", explicit))
		return "", false
	}

	for _, candidate := range discoveryCandidates(instanceDir) {
		if validDashboardRoot(candidate) {
			return filepath.Clean(candidate), true
		}
	}
	return "", false
}

func discoveryCandidates(instanceDir string) []string {
	var out []string
	if root := strings.TrimSpace(instanceDir); root != "" {
		out = append(out, filepath.Join(root, "dashboard", "dist"))
	}
	wd, err := os.Getwd()
	if err == nil {
		// 与 MaiBot 源码树并列的开发布局：bot/MaiBot/dashboard/dist
		out = append(out, filepath.Join(wd, "..", "MaiBot", "dashboard", "dist"))
		out = append(out, filepath.Join(wd, "MaiBot", "dashboard", "dist"))
	}
	// 对齐 Python webui/app.py:299-313：本地 dist 不存在时回退到已安装的 maibot_dashboard 包
	out = append(out, installedDashboardCandidates()...)
	return out
}

// installedDashboardCandidates 返回已安装 maibot_dashboard 包的 dist 目录候选
// 对齐 Python _resolve_static_path 的 import_module("maibot_dashboard").get_dist_path()
func installedDashboardCandidates() []string {
	var out []string
	// 显式覆盖：MAIBOT_DASHBOARD_PACKAGE_DIR 指向包目录
	if explicit := strings.TrimSpace(os.Getenv(envDashboardPackageDir)); explicit != "" {
		out = append(out, filepath.Join(explicit, "dist"))
	}
	// 常见 site-packages 布局：<prefix>/lib/pythonX.Y/site-packages/maibot_dashboard/dist
	for _, prefix := range pythonPrefixCandidates() {
		matches, err := filepath.Glob(filepath.Join(prefix, "lib", "python*", "site-packages", dashboardPackageName, "dist"))
		if err != nil {
			continue
		}
		out = append(out, matches...)
		// Windows 布局：<prefix>/Lib/site-packages/...
		out = append(out, filepath.Join(prefix, "Lib", "site-packages", dashboardPackageName, "dist"))
	}
	return out
}

// pythonPrefixCandidates 返回可能的 Python 安装前缀
func pythonPrefixCandidates() []string {
	candidates := make([]string, 0, 4)
	if explicit := strings.TrimSpace(os.Getenv(envPythonPrefix)); explicit != "" {
		candidates = append(candidates, explicit)
	}
	// 从 PATH 中的 python 可执行文件反推前缀
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		candidates = append(candidates, filepath.Dir(dir))
	}
	return candidates
}

func validDashboardRoot(dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !info.IsDir()
}
