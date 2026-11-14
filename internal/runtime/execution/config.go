package execution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	webuiutils "maibot-go/internal/webui/utils"

	"github.com/pelletier/go-toml/v2"
)

// EnvExecutionMode 环境变量名，可覆盖配置文件中的执行模式
const EnvExecutionMode = "MAIBOT_EXECUTION_MODE"

// RuntimeTomlPath 返回运行时配置文件的完整路径
func RuntimeTomlPath(instanceDir string) string {
	return filepath.Join(filepath.Clean(instanceDir), "config", runtimeConfigFileName)
}

// LoadProfile 从 config/runtime.toml 加载执行配置。
// 环境变量 MAIBOT_EXECUTION_MODE 优先级高于文件。
// 文件不存在时返回默认兼容模式；模式值非法时 panic。
func LoadProfile(instanceDir string) Profile {
	profile := DefaultProfile()
	path := RuntimeTomlPath(instanceDir)

	raw, err := os.ReadFile(filepath.Clean(path))
	if err == nil {
		var doc struct {
			Execution Profile `toml:"execution"`
		}
		if parseErr := toml.Unmarshal(raw, &doc); parseErr == nil {
			profile = doc.Execution
		}
	}

	// 环境变量覆盖
	if envMode := strings.TrimSpace(os.Getenv(EnvExecutionMode)); envMode != "" {
		profile.Mode = Mode(strings.ToLower(envMode))
	}

	// 未设置模式时回退到兼容模式
	if profile.Mode == "" {
		profile.Mode = ModeCompatibility
	}

	// 校验模式合法性
	if !IsValidMode(profile.Mode) {
		panic(fmt.Sprintf(
			"无效的执行模式 %q，合法值: %s, %s",
			profile.Mode, ModeCompatibility, ModeNative,
		))
	}

	return profile
}

// SaveProfile 将执行配置写入 config/runtime.toml，保留注释与其他节
func SaveProfile(instanceDir string, profile Profile) error {
	path := RuntimeTomlPath(instanceDir)

	// 读取现有文档或创建空文档
	doc, err := webuiutils.ReadTOMLDocument(path)
	if err != nil {
		doc = make(map[string]any)
	}

	// 更新 execution 节
	doc["execution"] = map[string]any{
		"mode":             string(profile.Mode),
		"worker_pool_size": int64(profile.WorkerPoolSize),
	}

	return webuiutils.WriteTOMLDocument(path, doc)
}
