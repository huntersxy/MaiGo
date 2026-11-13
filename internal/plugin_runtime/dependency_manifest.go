package pluginruntime

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// PythonPackageDependency 记录单个插件对某个 Python 包的依赖声明
type PythonPackageDependency struct {
	Type        string `json:"type"`
	PackageName string `json:"name"`
	VersionSpec string `json:"version_spec"`
}

// PluginDependencyManifest 是依赖流水线需要的插件 Manifest 子集
type PluginDependencyManifest struct {
	ID           string
	Path         string
	Dependencies []PythonPackageDependency
}

// CollectPluginDependencyManifests 扫描插件根目录并收集可解析的 Manifest
func CollectPluginDependencyManifests(pluginDirs []string) map[string]PluginDependencyManifest {
	manifests := make(map[string]PluginDependencyManifest)
	for _, pluginDir := range pluginDirs {
		root := strings.TrimSpace(pluginDir)
		if root == "" {
			continue
		}
		resolved, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			zap.L().Warn("读取插件目录失败", zap.String("path", resolved), zap.Error(err))
			continue
		}
		sort.Slice(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		for _, entry := range entries {
			pluginPath := filepath.Join(resolved, entry.Name())
			if !entry.IsDir() || isReservedPluginDependencyDirectory(entry.Name()) {
				continue
			}
			if isPluginDisabledByConfig(pluginPath) {
				continue
			}
			manifest, err := loadPluginDependencyManifest(pluginPath)
			if err != nil {
				zap.L().Warn("插件 Manifest 解析失败，依赖分析跳过该插件",
					zap.String("path", pluginPath), zap.Error(err))
				continue
			}
			manifests[manifest.ID] = manifest
		}
	}
	return manifests
}

func isReservedPluginDependencyDirectory(name string) bool {
	lower := strings.ToLower(name)
	return lower == "data" || lower == "__pycache__" || strings.HasPrefix(lower, ".")
}

func isPluginDisabledByConfig(pluginPath string) bool {
	raw, err := os.ReadFile(filepath.Join(pluginPath, "config.toml"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			zap.L().Error("读取插件配置失败", zap.String("path", pluginPath), zap.Error(err))
		}
		return false
	}
	var config struct {
		Plugin map[string]any `toml:"plugin"`
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		zap.L().Error("解析插件配置失败", zap.String("path", pluginPath), zap.Error(err))
		return false
	}
	enabled, exists := config.Plugin["enabled"]
	if !exists {
		return false
	}
	value, ok := pydanticBoolValue(enabled)
	if !ok {
		zap.L().Error("插件配置 plugin.enabled 无效", zap.String("path", pluginPath))
		return false
	}
	return !value
}

func pydanticBoolValue(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case int64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case float64:
		return typed != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "true", "1", "yes", "on", "y", "t":
			return true, true
		case "false", "0", "no", "off", "n", "f", "":
			return false, true
		}
	}
	return false, false
}

func loadPluginDependencyManifest(pluginPath string) (PluginDependencyManifest, error) {
	entrypoint := filepath.Join(pluginPath, "plugin.py")
	if info, err := os.Stat(entrypoint); err != nil || !info.Mode().IsRegular() {
		return PluginDependencyManifest{}, errors.New("缺少 plugin.py")
	}
	raw, err := os.ReadFile(filepath.Join(pluginPath, "_manifest.json"))
	if err != nil {
		return PluginDependencyManifest{}, err
	}
	var payload struct {
		ManifestVersion int    `json:"manifest_version"`
		ID              string `json:"id"`
		Dependencies    []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			VersionSpec string `json:"version_spec"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return PluginDependencyManifest{}, err
	}
	if payload.ManifestVersion != 2 {
		return PluginDependencyManifest{}, errors.New("manifest_version 必须为 2")
	}
	pluginID := strings.TrimSpace(payload.ID)
	if !isSafePythonPluginID(pluginID) {
		return PluginDependencyManifest{}, errors.New("插件 ID 不合法")
	}
	manifest := PluginDependencyManifest{ID: pluginID, Path: pluginPath}
	for _, dependency := range payload.Dependencies {
		if dependency.Type != "python_package" {
			continue
		}
		name := strings.TrimSpace(dependency.Name)
		if !pep440PackageNamePattern.MatchString(name) {
			return PluginDependencyManifest{}, errors.New("Python 包名不合法: " + name)
		}
		versionSpec := strings.TrimSpace(dependency.VersionSpec)
		if versionSpec == "" {
			return PluginDependencyManifest{}, errors.New("Python 包版本约束不能为空: " + name)
		}
		manifest.Dependencies = append(manifest.Dependencies, PythonPackageDependency{
			Type:        dependency.Type,
			PackageName: name,
			VersionSpec: versionSpec,
		})
	}
	return manifest, nil
}
