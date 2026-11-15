package memorix

// A_memorix 宿主配置读写：对齐 host_service.py 的配置缓存、raw TOML、校验、
// 备份/原子替换、稳定重载与失败恢复、热重载回调
// 差异：Python 用 tomlkit 保留注释与键顺序；Go 用 go-toml 整文件 map 往返，会丢失注释与顺序

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"MaiGo/internal/config/maibot"

	"github.com/bytedance/sonic"
	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// GetConfigPath 对齐 get_config_path
func (s *AMemorixHostService) GetConfigPath() string {
	if s.opts.ConfigStore != nil {
		if path := strings.TrimSpace(s.opts.ConfigStore.BotConfigPath()); path != "" {
			return path
		}
	}
	return filepath.Join(RepoRoot(), "config", "bot_config.toml")
}

// GetSchemaPath 对齐 get_schema_path
func (s *AMemorixHostService) GetSchemaPath() string {
	return s.schemaPath()
}

// GetConfigSchema 对齐 get_config_schema；schema 缺失时返回内置空结构
func (s *AMemorixHostService) GetConfigSchema() (map[string]any, error) {
	path := s.GetSchemaPath()
	data, err := s.fileOps.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultAMemorixSchema(), nil
		}
		return nil, err
	}
	var schema map[string]any
	if err := sonic.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("解析 A_Memorix 配置 schema 失败: %w", err)
	}
	return schema, nil
}

func defaultAMemorixSchema() map[string]any {
	return map[string]any{
		"plugin_id": "a_memorix",
		"plugin_info": map[string]any{
			"name":        "A_Memorix",
			"version":     "",
			"description": "A_Memorix 配置结构",
			"author":      "A_Dawn",
		},
		"sections": map[string]any{},
		"layout":   map[string]any{"type": "auto", "tabs": []any{}},
	}
}

// GetConfig 对齐 get_config，返回运行时配置字典副本
func (s *AMemorixHostService) GetConfig() map[string]any {
	return s.readConfig()
}

// IsEnabled 对齐 is_enabled
func (s *AMemorixHostService) IsEnabled() bool {
	return isEnabledConfig(s.readConfig())
}

// GetRuntimeDataDir 对齐 get_runtime_data_dir
func (s *AMemorixHostService) GetRuntimeDataDir() string {
	config := s.readConfig()
	dataDir := "./data"
	if storage := payloadMap(config, "storage"); storage != nil {
		if raw, ok := storage["data_dir"]; ok {
			if value := strings.TrimSpace(anyToString(raw)); value != "" {
				dataDir = value
			}
		}
	}
	return ResolveRepoPath(dataDir, DefaultDataDir())
}

// GetRawConfigWithMeta 对齐 get_raw_config_with_meta
func (s *AMemorixHostService) GetRawConfigWithMeta() map[string]any {
	config := s.GetConfig()
	encoded, err := toml.Marshal(map[string]any{"a_memorix": config})
	if err != nil {
		zap.L().Warn("A_Memorix 生成 raw 配置失败", zap.Error(err))
	}
	exists := false
	if info, statErr := s.fileOps.Stat(s.GetConfigPath()); statErr == nil && !info.IsDir() {
		exists = true
	}
	defaultConfig := s.configModelToRuntimeDict(s.buildDefaultConfig())
	return map[string]any{
		"config":        string(encoded),
		"exists":        exists,
		"using_default": reflect.DeepEqual(config, defaultConfig),
	}
}

// GetRawConfig 对齐 get_raw_config
func (s *AMemorixHostService) GetRawConfig() string {
	return anyToString(s.GetRawConfigWithMeta()["config"])
}

// UpdateRawConfig 对齐 update_raw_config：整体替换 [a_memorix]
func (s *AMemorixHostService) UpdateRawConfig(ctx context.Context, raw string) (map[string]any, error) {
	var document map[string]any
	if err := toml.Unmarshal([]byte(raw), &document); err != nil {
		return nil, fmt.Errorf("解析 A_Memorix 配置失败: %w", err)
	}
	if document == nil {
		document = map[string]any{}
	}
	payload := anyToMap(document["a_memorix"])
	if payload == nil {
		payload = normalizeConfigMap(document)
	}
	path, backup, err := s.writeConfigToBotConfig(ctx, payload, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":     true,
		"message":     "配置已保存",
		"backup_path": backup,
		"config_path": path,
	}, nil
}

// UpdateConfig 对齐 update_config：按字段合并 [a_memorix]
func (s *AMemorixHostService) UpdateConfig(ctx context.Context, config map[string]any) (map[string]any, error) {
	path, backup, err := s.writeConfigToBotConfig(ctx, config, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"success":     true,
		"message":     "配置已保存",
		"backup_path": backup,
		"config_path": path,
	}, nil
}

// RegisterConfigReloadCallback 对齐 register_config_reload_callback
func (s *AMemorixHostService) RegisterConfigReloadCallback() {
	s.reloadCallbackMu.Lock()
	defer s.reloadCallbackMu.Unlock()
	if s.reloadCallbackRegistered || s.opts.ConfigStore == nil {
		return
	}
	s.opts.ConfigStore.RegisterReloadCallback(func(scopes []string) {
		_ = s.OnConfigReload(scopes)
	})
	s.reloadCallbackRegistered = true
}

// OnConfigReload 对齐 on_config_reload
func (s *AMemorixHostService) OnConfigReload(scopes []string) error {
	normalized := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if value := trimLower(scope); value != "" {
			normalized[value] = struct{}{}
		}
	}
	if len(normalized) > 0 {
		// bot 控制 A_Memorix 自身配置；model 控制原生摘要使用的模型与任务表，
		// 两者都需要重建内核，否则模型热重载后摘要仍走启动快照
		_, botChanged := normalized["bot"]
		_, modelChanged := normalized["model"]
		if !botChanged && !modelChanged {
			return nil
		}
	}
	if s.suppressReloadCallback.Load() {
		return nil
	}
	return s.Reload(context.Background())
}

func (s *AMemorixHostService) clearConfigCache() {
	s.configMu.Lock()
	s.configCache = nil
	s.configMu.Unlock()
}

func (s *AMemorixHostService) buildDefaultConfig() maibot.AMemorixConfig {
	if s.opts.DefaultConfig != nil {
		return s.opts.DefaultConfig()
	}
	return defaultAMemorixConfig()
}

// readConfig 对齐 _read_config：缓存、读失败回退默认值
func (s *AMemorixHostService) readConfig() map[string]any {
	s.configMu.RLock()
	if s.configCache != nil {
		cached := deepCloneMap(s.configCache)
		s.configMu.RUnlock()
		return cached
	}
	s.configMu.RUnlock()

	defaults := s.configModelToRuntimeDict(s.buildDefaultConfig())
	runtime := defaults
	if s.opts.ConfigStore != nil {
		snapshot, err := s.opts.ConfigStore.AMemorixSnapshot()
		if err != nil {
			zap.L().Warn("读取 A_Memorix 主配置失败，使用默认值", zap.Error(err))
		} else if snapshot.Raw != nil {
			runtime = mergeConfigMaps(defaults, normalizeConfigMap(snapshot.Raw))
		}
	} else {
		zap.L().Warn("A_Memorix 配置存储未注入，使用默认值")
	}

	s.configMu.Lock()
	s.configCache = runtime
	s.configMu.Unlock()
	return deepCloneMap(runtime)
}

// configModelToRuntimeDict 对齐 _config_model_to_runtime_dict
// 说明：生成的结构体没有 pydantic 默认值，这里显式补齐 host_service 直接读取的三个字段
func (s *AMemorixHostService) configModelToRuntimeDict(model maibot.AMemorixConfig) map[string]any {
	out := map[string]any{}
	if encoded, err := toml.Marshal(model); err == nil && len(encoded) > 0 {
		if err := toml.Unmarshal(encoded, &out); err != nil {
			out = map[string]any{}
		}
	}
	out = normalizeConfigMap(out)
	if stripped, ok := stripInternalConfigFields(out).(map[string]any); ok {
		out = stripped
	}

	plugin := anyToMap(out["plugin"])
	if plugin == nil {
		plugin = map[string]any{}
	}
	plugin["enabled"] = model.Plugin.Enabled
	out["plugin"] = plugin

	storage := anyToMap(out["storage"])
	if storage == nil {
		storage = map[string]any{}
	}
	storage["data_dir"] = model.Storage.DataDir
	out["storage"] = storage

	out["global_memory_sharing_enabled"] = model.GlobalMemorySharingEnabled
	if web := anyToMap(out["web"]); web != nil {
		if value, ok := web["import_config"]; ok {
			if _, exists := web["import"]; !exists {
				web["import"] = value
			}
			delete(web, "import_config")
		}
	}
	return out
}

// runtimeDictToBotConfigDict 对齐 _runtime_dict_to_bot_config_dict
func (s *AMemorixHostService) runtimeDictToBotConfigDict(config map[string]any) map[string]any {
	normalized := normalizeConfigMap(config)
	if stripped, ok := stripInternalConfigFields(normalized).(map[string]any); ok {
		normalized = stripped
	}
	if web := anyToMap(normalized["web"]); web != nil {
		if value, ok := web["import_config"]; ok {
			if _, exists := web["import"]; !exists {
				web["import"] = value
			}
			delete(web, "import_config")
		}
	}
	return normalized
}

// validateBotConfigDict 对齐 _validate_bot_config_dict
// pydantic extra=forbid 用 toml.Decoder.DisallowUnknownFields 近似；不做取值范围校验
func (s *AMemorixHostService) validateBotConfigDict(config map[string]any) error {
	if config == nil {
		return errors.New("A_Memorix 配置必须是对象")
	}
	normalized := s.runtimeDictToBotConfigDict(config)
	encoded, err := toml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("A_Memorix 配置校验失败: %w", err)
	}
	var model maibot.AMemorixConfig
	decoder := toml.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&model); err != nil {
		return fmt.Errorf("A_Memorix 配置校验失败: %w", err)
	}
	return nil
}

// writeConfigToBotConfig 对齐 _write_config_to_bot_config
func (s *AMemorixHostService) writeConfigToBotConfig(ctx context.Context, config map[string]any, replace bool) (string, string, error) {
	if config == nil {
		return "", "", errors.New("A_Memorix 配置必须是对象")
	}
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	path := s.GetConfigPath()
	if err := s.fileOps.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", err
	}
	original, err := s.readFileContent(path)
	if err != nil {
		return "", "", err
	}

	document := map[string]any{}
	if len(bytes.TrimSpace(original)) > 0 {
		if err := toml.Unmarshal(original, &document); err != nil {
			return "", "", fmt.Errorf("解析 bot_config.toml 失败: %w", err)
		}
		document = normalizeConfigMap(document)
	}

	payload := s.runtimeDictToBotConfigDict(config)
	if replace {
		document["a_memorix"] = payload
	} else if current := anyToMap(document["a_memorix"]); current != nil {
		updateTOMLDoc(current, payload)
	} else {
		document["a_memorix"] = payload
	}

	candidateAny := toBuiltinData(document["a_memorix"])
	candidate, ok := candidateAny.(map[string]any)
	if !ok {
		return "", "", errors.New("A_Memorix 配置必须是对象")
	}
	if err := s.validateBotConfigDict(candidate); err != nil {
		return "", "", err
	}
	candidateContent, err := toml.Marshal(document)
	if err != nil {
		return "", "", err
	}

	backup, err := s.backupConfigFile(path)
	if err != nil {
		return "", "", err
	}
	if err := s.replaceConfigFile(path, candidateContent); err != nil {
		return "", "", err
	}

	persistedContent, err := s.reloadStableConfig(ctx, path)
	if err != nil {
		restored, recoverErr := s.recoverAfterFailedReload(ctx, path, original, candidateContent)
		if recoverErr != nil {
			return "", "", recoverErr
		}
		if restored {
			return "", "", errors.New("A_Memorix 配置重载失败，已恢复写入前的配置")
		}
		return "", "", errors.New("A_Memorix 配置重载失败，检测到其他写入，已保留并重新加载当前配置")
	}

	persisted := s.readAMemorixConfig(persistedContent)
	matched := false
	if replace {
		matched = reflect.DeepEqual(persisted, payload) || configValuesEqual(persisted, payload)
	} else if persistedMap, ok := persisted.(map[string]any); ok {
		matched = s.containsConfigPatch(persistedMap, payload)
	}
	if !matched {
		return "", "", errors.New("A_Memorix 配置在保存期间被其他写入覆盖，已保留并重新加载当前配置")
	}
	return path, backup, nil
}

func (s *AMemorixHostService) reloadStableConfig(ctx context.Context, path string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := s.readFileContent(path)
		if err != nil {
			return nil, err
		}
		loaded, err := s.reloadConfigManager(ctx)
		if err != nil {
			return nil, fmt.Errorf("配置管理器未能加载配置: %w", err)
		}
		if !loaded {
			return nil, errors.New("配置管理器未能加载配置")
		}
		after, err := s.readFileContent(path)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(after, before) {
			continue
		}
		if err := s.Reload(ctx); err != nil {
			return nil, err
		}
		again, err := s.readFileContent(path)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(again, after) {
			return again, nil
		}
	}
	return nil, errors.New("配置在重载期间持续发生变化")
}

func (s *AMemorixHostService) reloadConfigManager(ctx context.Context) (bool, error) {
	if s.opts.ConfigStore == nil {
		return false, errors.New("配置存储未注入")
	}
	s.suppressReloadCallback.Store(true)
	defer s.suppressReloadCallback.Store(false)
	if err := s.opts.ConfigStore.Reload(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *AMemorixHostService) recoverAfterFailedReload(ctx context.Context, path string, original, candidate []byte) (bool, error) {
	current, err := s.readFileContent(path)
	if err != nil {
		return false, err
	}
	restored := bytes.Equal(current, candidate)
	if restored {
		if original == nil {
			if removeErr := s.fileOps.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return restored, removeErr
			}
		} else if replaceErr := s.replaceConfigFile(path, original); replaceErr != nil {
			return restored, replaceErr
		}
	}
	if _, err := s.reloadStableConfig(ctx, path); err != nil {
		if restored {
			return restored, errors.New("已恢复原配置，但配置管理器或 A_Memorix 运行时仍无法重载")
		}
		return restored, errors.New("检测到其他配置写入，已保留当前文件，但无法完成运行时重载")
	}
	return restored, nil
}

func (s *AMemorixHostService) containsConfigPatch(actual, expected map[string]any) bool {
	for key, expectedValue := range expected {
		actualValue, ok := actual[key]
		if !ok {
			return false
		}
		expectedMap, expectedIsMap := expectedValue.(map[string]any)
		if expectedIsMap {
			actualMap, actualIsMap := actualValue.(map[string]any)
			if !actualIsMap || !s.containsConfigPatch(actualMap, expectedMap) {
				return false
			}
			continue
		}
		if !configValuesEqual(actualValue, expectedValue) {
			return false
		}
	}
	return true
}

func (s *AMemorixHostService) readFileContent(path string) ([]byte, error) {
	data, err := s.fileOps.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func (s *AMemorixHostService) readAMemorixConfig(content []byte) any {
	if content == nil {
		return nil
	}
	var document map[string]any
	if err := toml.Unmarshal(content, &document); err != nil {
		return nil
	}
	value, ok := document["a_memorix"]
	if !ok {
		return map[string]any{}
	}
	converted := toBuiltinData(value)
	if mapped, ok := converted.(map[string]any); ok {
		return mapped
	}
	return nil
}

func (s *AMemorixHostService) backupConfigFile(path string) (string, error) {
	info, err := s.fileOps.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	data, err := s.fileOps.ReadFile(path)
	if err != nil {
		return "", err
	}
	// 对齐 Python `%Y%m%d%H%M%S%f`：20 位无分隔点的时间戳（含微秒），写失败清理残留
	now := time.Now()
	backupPath := fmt.Sprintf("%s.backup.%s%06d.%s", path, now.Format("20060102150405"), now.Nanosecond()/1000, newUUIDHex())
	if err := s.fileOps.WriteFileExclusive(backupPath, data, 0o600); err != nil {
		_ = s.fileOps.Remove(backupPath)
		return "", err
	}
	if err := s.fileOps.Chmod(backupPath, info.Mode().Perm()); err != nil {
		_ = s.fileOps.Remove(backupPath)
		return "", err
	}
	return backupPath, nil
}

func (s *AMemorixHostService) replaceConfigFile(path string, content []byte) error {
	mode := os.FileMode(0o600)
	if info, err := s.fileOps.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tempPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%s.tmp", filepath.Base(path), newUUIDHex()))
	if err := s.fileOps.WriteFileExclusive(tempPath, content, 0o600); err != nil {
		return err
	}
	defer func() {
		if removeErr := s.fileOps.Remove(tempPath); removeErr != nil && !os.IsNotExist(removeErr) {
			zap.L().Warn("A_Memorix 清理临时配置文件失败", zap.Error(removeErr))
		}
	}()
	if err := s.fileOps.Chmod(tempPath, mode); err != nil {
		return err
	}
	return s.fileOps.ReplaceFile(tempPath, path)
}
