package execution

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	maibotcfg "MaiGo/internal/config/maibot"
	webuiutils "MaiGo/internal/webui/utils"

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
	ErrConfigChanged         = errors.New("execution: runtime config changed outside the live settings API")
)

// runtimeSaveMu 串行化 config/runtime.toml 的读-改-写，避免并发保存互相覆盖。
var runtimeSaveMu sync.Mutex

// runtimeDocument runtime.toml 的完整解析结果
type runtimeDocument struct {
	Execution Profile        `toml:"execution"`
	Features  FeatureToggles `toml:"features"`
}

// RuntimeTomlPath 返回运行时配置文件的完整路径
func RuntimeTomlPath(instanceDir string) string {
	return filepath.Join(filepath.Clean(instanceDir), "config", runtimeConfigFileName)
}

// LoadRuntimeFile 从 config/runtime.toml 读取执行配置与功能开关。
//
// 不应用 MAIBOT_EXECUTION_MODE；写路径必须使用文件中的原始模式，避免把环境变量
// 临时覆盖的值持久化到磁盘。文件不存在时返回默认值。
func LoadRuntimeFile(instanceDir string) (Profile, FeatureToggles, error) {
	path := RuntimeTomlPath(instanceDir)
	raw, err := readRuntimeSource(path)
	if err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	return parseRuntimeSource(path, raw)
}

func readRuntimeSource(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: 读取 %s 失败: %v", ErrConfigRead, path, err)
	}
	return raw, nil
}

func parseRuntimeSource(path string, raw []byte) (Profile, FeatureToggles, error) {
	var doc runtimeDocument
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return Profile{}, FeatureToggles{}, fmt.Errorf("%w: 解析 %s 失败: %v", ErrConfigParse, path, err)
	}

	profile := doc.Execution
	if profile.Mode == "" {
		profile.Mode = ModeCompatibility
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	return profile, doc.Features, nil
}

// LoadRuntime 读取文件并应用 MAIBOT_EXECUTION_MODE，返回实际生效的配置。
func LoadRuntime(instanceDir string) (Profile, FeatureToggles, error) {
	profile, toggles, err := LoadRuntimeFile(instanceDir)
	if err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	profile, err = ApplyEnvMode(profile)
	if err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	return profile, toggles, nil
}

// ApplyEnvMode 应用 MAIBOT_EXECUTION_MODE 覆盖并校验结果。
func ApplyEnvMode(profile Profile) (Profile, error) {
	return applyEnvMode(profile, strings.TrimSpace(os.Getenv(EnvExecutionMode)))
}

func applyEnvMode(profile Profile, envMode string) (Profile, error) {
	if envMode != "" {
		profile.Mode = Mode(strings.ToLower(envMode))
	}
	if profile.Mode == "" {
		profile.Mode = ModeCompatibility
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// LoadProfile 从 config/runtime.toml 加载执行配置。
// 环境变量 MAIBOT_EXECUTION_MODE 优先级高于文件。
// 文件不存在时返回默认兼容模式；文件损坏或模式非法时返回错误。
func LoadProfile(instanceDir string) (Profile, error) {
	profile, _, err := LoadRuntimeFile(instanceDir)
	if err != nil {
		return Profile{}, err
	}
	return ApplyEnvMode(profile)
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

// UpdateRuntime 在 runtimeSaveMu 内完成读取-修改-写回。
//
// mutate 可以返回错误中止写入；文件中的未知节与未知 feature 键会保留。
func UpdateRuntime(instanceDir string, mutate MutateSettings) (Profile, FeatureToggles, error) {
	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()
	save, err := prepareRuntimeSave(instanceDir, false, mutate)
	if err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	if err := save.write(); err != nil {
		return Profile{}, FeatureToggles{}, err
	}
	return save.profile, save.toggles, nil
}

// SaveProfile 将执行配置写入 config/runtime.toml，保留注释、其他节与未知 feature 键。
func SaveProfile(instanceDir string, next Profile) error {
	_, _, err := UpdateRuntime(instanceDir, func(profile *Profile, _ *FeatureToggles) error {
		*profile = next
		return nil
	})
	return err
}

// SaveAll 将执行配置与功能开关同时写入 config/runtime.toml。
func SaveAll(instanceDir string, next Profile, toggles FeatureToggles) error {
	_, _, err := UpdateRuntime(instanceDir, func(profile *Profile, current *FeatureToggles) error {
		*profile = next
		*current = toggles
		return nil
	})
	return err
}

// ResetAll 将执行配置与功能开关重置为默认值。
//
// 与普通保存不同，原文件解析失败时它会重建文档，保证运维能从损坏的配置中恢复。
// 重置是显式操作，允许丢弃无法解析的旧内容，但仍保留可解析文件里的未知键。
func ResetAll(instanceDir string) error {
	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()
	save, err := prepareRuntimeSave(instanceDir, true, nil)
	if err != nil {
		return err
	}
	return save.write()
}

type runtimeSave struct {
	path    string
	before  []byte
	after   []byte
	profile Profile
	toggles FeatureToggles
}

func prepareRuntimeSave(instanceDir string, reset bool, mutate MutateSettings) (runtimeSave, error) {
	path := RuntimeTomlPath(instanceDir)
	raw, err := readRuntimeSource(path)
	if err != nil {
		return runtimeSave{}, err
	}
	profile, toggles := DefaultProfile(), DefaultFeatureToggles()
	if !reset {
		profile, toggles, err = parseRuntimeSource(path, raw)
		if err != nil {
			return runtimeSave{}, err
		}
	}
	if mutate != nil {
		if err := mutate(&profile, &toggles); err != nil {
			return runtimeSave{}, err
		}
	}
	if err := ValidateProfile(profile); err != nil {
		return runtimeSave{}, err
	}
	doc := make(map[string]any)
	parseErr := toml.Unmarshal(raw, &doc)
	if parseErr != nil {
		if !reset {
			return runtimeSave{}, fmt.Errorf("%w: 解析 %s 失败: %v", ErrConfigParse, path, parseErr)
		}
		doc = make(map[string]any)
	}
	section := runtimeSection(doc, "execution")
	section["mode"] = string(profile.Mode)
	section["worker_pool_size"] = int64(profile.WorkerPoolSize)
	features := runtimeSection(doc, "features")
	for key, value := range toggles.toMap() {
		features[key] = value
	}
	encoded, err := toml.Marshal(doc)
	if err != nil {
		return runtimeSave{}, err
	}
	if parseErr == nil && len(raw) > 0 {
		encoded = maibotcfg.MergeTOMLPreservingComments(raw, encoded)
		encodedProfile, encodedToggles, err := parseRuntimeSource(path, encoded)
		if err != nil {
			return runtimeSave{}, err
		}
		if encodedProfile != profile || encodedToggles != toggles {
			return runtimeSave{}, fmt.Errorf("%w: 写回内容与目标配置不一致", ErrConfigParse)
		}
	}
	return runtimeSave{path: path, before: raw, after: encoded, profile: profile, toggles: toggles}, nil
}

func runtimeSection(doc map[string]any, key string) map[string]any {
	section, _ := doc[key].(map[string]any)
	if section == nil {
		section = make(map[string]any)
		doc[key] = section
	}
	return section
}

func (save runtimeSave) persist() error {
	runtimeSaveMu.Lock()
	defer runtimeSaveMu.Unlock()
	return save.write()
}

func (save runtimeSave) write() error {
	current, err := readRuntimeSource(save.path)
	if err != nil {
		return err
	}
	if (current == nil) != (save.before == nil) || !bytes.Equal(current, save.before) {
		return ErrConfigChanged
	}
	return webuiutils.WriteFileAtomic(save.path, save.after, 0o644)
}
