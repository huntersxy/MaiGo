package webuiapi

import (
	"strings"

	appversion "MaiGo/internal/common/version"
)

// buildVersion 统一解析 WebUI 对外版本号
// 优先使用调用方注入的 Deps.BuildVersion，缺失或为 dev 时回退 internal/version.String
// internal/version.String 只认构建注入且非 dev 的版本，否则回退 Python 1.3.19 基线
func (d Deps) buildVersion() string {
	if value := strings.TrimSpace(d.BuildVersion); !appversion.IsUnknown(value) {
		return value
	}
	return appversion.String()
}
