package pluginruntime

import "MaiGo/internal/common/version"

// DetectHostApplicationVersion 读取当前 Host 应用版本号，对齐 detect_host_application_version
//
// Python 从 pyproject.toml 读 project.version；Go 的版本在构建期注入（`-X ...version.Build`），
// 开发构建回退到 Python 基线版本，两者语义等价。
func DetectHostApplicationVersion() string {
	return version.String()
}
