package version

import "strings"

const (
	// Baseline 对齐 ../MaiBot/pyproject.toml 的 project.version
	Baseline = "1.4.3"
	// RequiredWebUIVersion 对齐 ../MaiBot/pyproject.toml 的 maibot-dashboard==1.10.3
	RequiredWebUIVersion = "1.10.3"
)

// Build 是构建时注入的应用版本，开发构建使用 dev
// Dockerfile:35 通过 -X MaiGo/internal/common/version.Build=<VERSION> 注入
var Build = "dev"

// IsUnknown 判断版本是否缺失或为开发占位值 dev
func IsUnknown(raw string) bool {
	value := strings.TrimSpace(raw)
	return value == "" || strings.EqualFold(value, "dev")
}

// Resolve 返回对外版本号：构建注入值非空且非 dev 时优先，否则回退 Python 基线 1.4.3
// Python 生产版本来自 ../MaiBot/pyproject.toml:7 的 project.version，由 src/config/config.py:76 读取
func Resolve(raw string) string {
	if IsUnknown(raw) {
		return Baseline
	}
	return strings.TrimSpace(raw)
}

// String 返回当前构建的对外版本号
func String() string {
	return Resolve(Build)
}
