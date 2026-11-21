package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"

	"MaiGo/internal/plugin_runtime/hooks"
	"MaiGo/internal/plugin_runtime/protocol"
	chinesetypo "MaiGo/plugins/built_in/chinese_typo"
)

func (h *Host) builtinTypoConfigPath() string {
	h.mu.RLock()
	root := h.instanceRoot
	h.mu.RUnlock()
	if root == "" {
		root = ResolveInstanceRoot()
	}
	return filepath.Join(root, "config", "builtin_plugins", chinesetypo.Directory, "config.toml")
}

func (h *Host) inspectBuiltinTypo(input map[string]any, provided bool) (InspectPluginConfigResult, chinesetypo.Config, error) {
	_, defaults, err := chinesetypo.ParseConfig(nil)
	if err != nil {
		return InspectPluginConfigResult{}, chinesetypo.Config{}, err
	}
	result := InspectPluginConfigResult{Success: true, Enabled: true, ConfigSchema: chinesetypo.Schema(), DefaultConfig: defaults}
	if !provided {
		raw, readErr := os.ReadFile(h.builtinTypoConfigPath())
		switch {
		case readErr == nil:
			if err := toml.Unmarshal(raw, &input); err != nil {
				result.ConfigError = err.Error()
				return result, chinesetypo.Config{}, nil
			}
		case !errors.Is(readErr, os.ErrNotExist):
			return result, chinesetypo.Config{}, readErr
		}
	}
	settings, normalized, err := chinesetypo.ParseConfig(input)
	if err != nil {
		result.ConfigError = err.Error()
		if plugin, ok := input["plugin"].(map[string]any); ok {
			if flag, ok := plugin["enabled"].(bool); ok {
				result.Enabled = flag
			}
		}
		return result, settings, nil
	}
	result.NormalizedConfig, result.Enabled = normalized, settings.Enabled
	result.Changed = !reflect.DeepEqual(input, normalized)
	return result, settings, nil
}

func (h *Host) applyBuiltinTypo() (string, error) {
	h.pluginFileUpdateMu.Lock()
	defer h.pluginFileUpdateMu.Unlock()
	snapshot, settings, err := h.inspectBuiltinTypo(nil, false)
	if err != nil {
		return "", err
	}
	return h.activateBuiltinTypo(snapshot, settings)
}

func (h *Host) activateBuiltinTypo(snapshot InspectPluginConfigResult, settings chinesetypo.Config) (string, error) {
	const id = chinesetypo.PluginID
	manifest := chinesetypo.Manifest()
	record := PluginRecord{PluginID: id, DisplayName: manifest["name"].(string), PluginVersion: manifest["version"].(string),
		PluginType: "extension", RunnerGroup: RunnerGroupBuiltin, ConfigError: snapshot.ConfigError,
		DefaultConfig: snapshot.DefaultConfig, ConfigSchema: snapshot.ConfigSchema}
	if snapshot.ConfigError != "" {
		h.stopBuiltinTypoLocked()
		h.RegisterPlugin(record)
		h.rememberNativeLoadStatus(id, "failed")
		return "config_error", nil
	}
	if err := writeInitialBuiltinTypoConfig(h.builtinTypoConfigPath(), snapshot.DefaultConfig); err != nil {
		return "", err
	}
	loaded := h.chineseTypo.Load() != nil
	if loaded == snapshot.Enabled && h.appliedConfigMatches(id, snapshot.NormalizedConfig) {
		if loaded {
			return "success", nil
		}
		return "inactive", nil
	}
	h.setPluginTransition(id, snapshot.Enabled)
	defer h.clearPluginTransition(id)
	if !snapshot.Enabled {
		h.stopBuiltinTypoLocked()
		h.rememberAppliedConfig(id, snapshot.NormalizedConfig)
		return "inactive", nil
	}
	runtime := chinesetypo.NewRuntime(settings)
	if !loaded {
		var components []protocol.ComponentDeclaration
		handlers := make(map[string]hooks.HandlerFunc)
		for _, declaration := range chinesetypo.HookDeclarations() {
			components = append(components, protocol.ComponentDeclaration{
				PluginID: id, Name: declaration.Name, ComponentType: ComponentTypeHookHandler,
				Metadata: map[string]any{"hook": declaration.Event, "description": declaration.Description, "timeout_ms": declaration.TimeoutMS},
			})
			handlers[declaration.Name] = func(ctx context.Context, args map[string]any) (hooks.HandlerResult, error) {
				if current := h.chineseTypo.Load(); current != nil {
					return current.Handler(declaration.Name)(ctx, args)
				}
				return hooks.HandlerResult{Success: true, Action: "continue"}, nil
			}
		}
		if _, err := h.applyPluginComponents(record, components, handlers); err != nil {
			return "", err
		}
		h.RegisterPlugin(record)
	}
	h.chineseTypo.Store(runtime)
	h.rememberAppliedConfig(id, snapshot.NormalizedConfig)
	h.rememberNativeLoadStatus(id, "success")
	return "success", nil
}

func (h *Host) stopBuiltinTypoLocked() {
	const id = chinesetypo.PluginID
	h.chineseTypo.Store(nil)
	if h.hasPlugin(id) {
		h.UnregisterPlugin(id)
	}
	h.forgetAppliedConfig(id)
	h.rememberNativeLoadStatus(id, "inactive")
}

func (h *Host) reloadBuiltinTypo() (ReloadPluginResult, error) {
	const id = chinesetypo.PluginID
	status, err := h.applyBuiltinTypo()
	result := ReloadPluginResult{RequestedPluginID: id, Success: err == nil}
	switch status {
	case "success":
		result.ReloadedPlugins = []string{id}
	case "inactive":
		result.InactivePlugins = []string{id}
		result.ExplicitlyDisabledPlugins = []string{id}
	case "config_error":
		result.Success = false
		result.FailedPlugins = map[string]string{id: h.PluginConfigError(id)}
	}
	return result, err
}

func writeInitialBuiltinTypoConfig(path string, config map[string]any) error {
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".chinese-typo-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	if err := os.Link(file.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("创建麦麦小工具配置失败: %w", err)
	}
	return nil
}
