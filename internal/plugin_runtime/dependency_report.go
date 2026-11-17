package pluginruntime

// 供宿主汇总所有 Bot 的 Python 依赖声明；只读，不修改环境。

import (
	"context"
	"sort"
	"strings"
)

// PluginRequirement 单个插件的 Python 包依赖及其环境状态
type PluginRequirement struct {
	Requirement      string   `json:"requirement"`
	PackageName      string   `json:"package"`
	VersionSpec      string   `json:"version_spec"`
	Satisfied        bool     `json:"satisfied"`
	InstalledVersion *string  `json:"installed_version"`
	PluginIDs        []string `json:"plugin_ids"`
}

// PluginDependencyReport 一个插件根目录的依赖声明
type PluginDependencyReport struct {
	Requirements []PluginRequirement `json:"requirements"`
	Blocked      map[string]string   `json:"blocked"`
}

// AnalyzePluginRequirements 读取插件声明并对照当前 Python 环境
func AnalyzePluginRequirements(ctx context.Context, projectRoot string, pluginDirs []string) (PluginDependencyReport, error) {
	pipeline := NewPluginDependencyPipeline(projectRoot)
	installed, err := pipeline.installedVersionMap(ctx)
	if err != nil {
		installed = map[string]string{}
	}
	manifests := CollectPluginDependencyManifests(pluginDirs)
	blocked := normalizePluginBlockedReasons(nil)
	for pluginID, reason := range pipeline.detectHostConflicts(manifests, installed) {
		appendPluginBlockReason(blocked, pluginID, reason)
	}
	for pluginID, reason := range pipeline.detectPluginConflicts(manifests, blocked, installed) {
		appendPluginBlockReason(blocked, pluginID, reason)
	}
	byRequirement := map[string]*PluginRequirement{}
	for _, pluginID := range sortedDependencyManifestIDs(manifests) {
		manifest := manifests[pluginID]
		for _, dependency := range manifest.Dependencies {
			text := dependency.PackageName + dependency.VersionSpec
			entry := byRequirement[text]
			if entry == nil {
				entry = &PluginRequirement{
					Requirement: text, PackageName: dependency.PackageName, VersionSpec: dependency.VersionSpec,
				}
				byRequirement[text] = entry
			}
			entry.PluginIDs = append(entry.PluginIDs, manifest.ID)
			installedVersion, exists := findInstalledPackageVersion(installed, dependency.PackageName)
			if !exists || strings.TrimSpace(installedVersion) == "" {
				continue
			}
			version := installedVersion
			entry.InstalledVersion = &version
			specSet, ok := ParsePEP440SpecifierSet(dependency.VersionSpec)
			parsed, versionOK := ParsePEP440Version(installedVersion)
			entry.Satisfied = !ok || !versionOK || specSet.ContainsText(installedVersion, parsed)
		}
	}
	requirements := make([]PluginRequirement, 0, len(byRequirement))
	for _, entry := range byRequirement {
		sort.Strings(entry.PluginIDs)
		entry.PluginIDs = uniqueStrings(entry.PluginIDs)
		requirements = append(requirements, *entry)
	}
	sort.Slice(requirements, func(i, j int) bool {
		return CanonicalizePythonPackageName(requirements[i].PackageName) <
			CanonicalizePythonPackageName(requirements[j].PackageName)
	})
	return PluginDependencyReport{Requirements: requirements, Blocked: blocked}, nil
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	kept := values[:1]
	for _, value := range values[1:] {
		if value != kept[len(kept)-1] {
			kept = append(kept, value)
		}
	}
	return kept
}

// InstallMissingRequirements 解析并安装缺失的插件依赖
func InstallMissingRequirements(ctx context.Context, projectRoot string, pluginDirs []string) (bool, string, error) {
	pipeline := NewPluginDependencyPipeline(projectRoot)
	plan := pipeline.BuildPlan(ctx, pluginDirs, nil)
	if len(plan.BlockedPluginReasons) > 0 {
		ids := make([]string, 0, len(plan.BlockedPluginReasons))
		for pluginID := range plan.BlockedPluginReasons {
			ids = append(ids, pluginID)
		}
		sort.Strings(ids)
		lines := make([]string, 0, len(ids))
		for _, pluginID := range ids {
			lines = append(lines, pluginID+": "+plan.BlockedPluginReasons[pluginID])
		}
		return false, "插件依赖声明冲突：" + strings.Join(lines, "；"), nil
	}
	if len(plan.InstallRequirements) == 0 {
		return false, "", nil
	}
	succeeded, message := pipeline.installRequirements(ctx, plan.InstallRequirements)
	if !succeeded {
		return false, message, nil
	}
	return true, message, nil
}
