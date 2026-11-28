package version

import (
	"runtime/debug"
	"strings"
)

const (
	// Baseline 对齐 ../MaiBot/pyproject.toml 的 project.version
	Baseline = "1.4.8"
	// RequiredWebUIVersion 对齐 MaiBot 1.4.8 的 maibot-dashboard==1.10.8
	RequiredWebUIVersion = "1.10.8"
)

// Build 是构建时注入的应用版本，开发构建使用 dev
// Dockerfile:35 通过 -X MaiGo/internal/common/version.Build=<VERSION> 注入
var Build = "dev"

// revision 由发布构建注入；本地 Git 构建可读取 Go 自带的 VCS 元数据。
var revision string

// Revision 返回构建来源的 Git 提交号，无构建元数据时返回空字符串。
func Revision() string {
	if revision != "" {
		return revision
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return ""
}

// IsUnknown 判断版本是否缺失或为开发占位值 dev
func IsUnknown(raw string) bool {
	value := strings.TrimSpace(raw)
	return value == "" || strings.EqualFold(value, "dev")
}

// Resolve 优先使用构建版本，开发构建使用 MaiBot 基线版本。
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
