package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	webuiutils "maibot-go/internal/webui/utils"

	"github.com/pelletier/go-toml/v2"
)

// EnvExecutionMode 环境变量名，可覆盖配置文件中的执行模式
const EnvExecutionMode = "MAIBOT_EXECUTION_MODE"

// MaxWorkerPoolSize 工作池大小的上限，避免误输入导致启动时创建过多 goroutine。
const MaxWorkerPoolSize = 1024

// 配置错误分类，供启动装配区分“必须拒绝启动”和“可回退并让页面修复”。
var (
	ErrInvalidMode           = errors.New("execution: invalid mode")
	ErrInvalidWorkerPoolSize = errors.New("execution: invalid worker_pool_size")
	ErrConfigRead            = errors.New("execution: runtime config read error")
	ErrConfigParse           = errors.New("execution: runtime config parse error")
)

// runtimeSaveMu 串行化 config/runtime.toml 的读-改-写，避免并发保存互相覆盖。
var runtimeSaveMu sync.Mutex

// RuntimeTomlPath 返回运行时配置文件的完整路径
func RuntimeTomlPath(instanceDir string) string {
	return filepath.Join(filepath.Clean(instanceDir), "config", runtimeConfigFileName)
}

// LoadProfile 从 config/runtime.toml 加载执行配置。
// 环境变量 MAIBOT_EXECUTION_MODE 优先级高于文件。
// 文件不存在时返回默认兼容模式；文件损坏或模式非法时返回错误。
func LoadProfile(instanceDir string) (Profile, error) {
	profile := DefaultProfile()
	path := RuntimeTomlPath(instanceDir)

	raw, err := os.ReadFile(filepath.Clean(path))
	switch {
	case err == nil:
		var doc struct {
			Execution Profile `toml:"execution"`
		}
		if parseErr := toml.Unmarshal(raw, &doc); parseErr != nil {
			return Profile{}, fmt.Errorf("%w: 解析 %s 失败: %v", ErrConfigParse, path, parseErr)
		}
		profile = doc.Execution
	case errors.Is(err, os.ErrNotExist):
		// 首次启动没有 runtime.toml，沿用默认值。
	default:
		return Profile{}, fmt.Errorf("%w: 读取 %s 失败: %v", ErrConfigRead, path, err)
	}

	// 环境变量覆盖
	if envMode := strings.TrimSpace(os.Getenv(EnvExecutionMode)); envMode != "" {
		profile.Mode = Mode(strings.ToLower(envMode))
	}

	// 未设置模式时回退到兼容模式
	if profile.Mode == "" {
		profile.Mode = ModeCompatibility
	}

	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}

	return profile, nil
}

// ValidateProfile 校验执行配置的取值是否可持久化、可启动。
func ValidateProfile(profile Profile) error {
	if !IsValidMode(profile.Mode) {
		return fmt.Errorf(
			"%w: 无效的执行模式 %q，合法值: %s, %s",
			ErrInvalidMode, profile.Mode, ModeCompatibility, ModeNative,
		)
	}
	if profile.WorkerPoolSize < 0 {
		return fmt.Errorf("%w: worker_pool_size 不能为负数: %d", ErrInvalidWorkerPoolSize, profile.WorkerPoolSize)
	}
	if profile.WorkerPoolSize > MaxWorkerPoolSize {
		return fmt.Errorf("%w: worker_pool_size 不能超过 %d: %d", ErrInvalidWorkerPoolSize, MaxWorkerPoolSize, profile.WorkerPoolSize)
	}
	return nil
}

// SaveProfile 将执行配置写入 config/runtime.toml，保留注释与其他节
func SaveProfile(instanceDir string, profile Profile) error {
	if err := ValidateProfile(profile); err != nil {
		return err
	}

	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()

	path := RuntimeTomlPath(instanceDir)
	doc, err := readRuntimeDocument(path)
	if err != nil {
		return err
	}

	doc["execution"] = executionSection(profile)
	return webuiutils.WriteTOMLDocument(path, doc)
}

// SaveAll 将执行配置与功能开关同时原子写入 config/runtime.toml
func SaveAll(instanceDir string, profile Profile, toggles FeatureToggles) error {
	if err := ValidateProfile(profile); err != nil {
		return err
	}

	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()

	path := RuntimeTomlPath(instanceDir)
	doc, err := readRuntimeDocument(path)
	if err != nil {
		return err
	}

	doc["execution"] = executionSection(profile)
	doc["features"] = toggles.toMap()

	return webuiutils.WriteTOMLDocument(path, doc)
}

// ResetAll 将执行配置与功能开关重置为默认值。
//
// 与 SaveAll 不同，原文件解析失败时它会重建文档，保证运维能从损坏的配置中恢复。
// 重置是显式操作，允许丢弃无法解析的旧内容。
func ResetAll(instanceDir string) error {
	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()

	path := RuntimeTomlPath(instanceDir)
	doc, err := webuiutils.ReadTOMLDocument(path)
	if err != nil {
		doc = make(map[string]any)
	}

	doc["execution"] = executionSection(DefaultProfile())
	doc["features"] = DefaultFeatureToggles().toMap()
	return webuiutils.WriteTOMLDocument(path, doc)
}

// readRuntimeDocument 读取现有配置；文件不存在时返回空文档，其他读取/解析错误向上抛出。
//
// 不能把解析失败当成“文件不存在”，否则一次保存就会覆盖用户手写的配置和注释。
func readRuntimeDocument(path string) (map[string]any, error) {
	doc, err := webuiutils.ReadTOMLDocument(path)
	if err == nil {
		return doc, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), nil
	}
	return nil, fmt.Errorf("%w: 读取 %s 失败: %v", ErrConfigRead, path, err)
}

// executionSection 将 Profile 转成 TOML 的 [execution] 节。
func executionSection(profile Profile) map[string]any {
	return map[string]any{
		"mode":             string(profile.Mode),
		"worker_pool_size": int64(profile.WorkerPoolSize),
	}
}
