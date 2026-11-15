package pluginruntime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

// PackageDependencyUsage 记录单个插件对某个 Python 包的依赖声明
type PackageDependencyUsage struct {
	PackageName string `json:"package_name"`
	PluginID    string `json:"plugin_id"`
	VersionSpec string `json:"version_spec"`
}

// CombinedPackageRequirement 表示一个已经合并后的 Python 包安装需求
type CombinedPackageRequirement struct {
	PackageName     string   `json:"package_name"`
	PluginIDs       []string `json:"plugin_ids"`
	RequirementText string   `json:"requirement_text"`
	VersionSpec     string   `json:"version_spec"`
}

// PackageIndexSettings 是主程序 pyproject.toml 中与依赖安装相关的 uv 配置
type PackageIndexSettings struct {
	IndexURLs   []string `json:"index_urls"`
	Constraints []string `json:"constraints"`
}

// DependencyPipelinePlan 表示一次依赖分析得到的计划
type DependencyPipelinePlan struct {
	BlockedPluginReasons map[string]string            `json:"blocked_plugin_reasons"`
	InstallRequirements  []CombinedPackageRequirement `json:"install_requirements"`
}

// DependencyPipelineResult 表示一次依赖流水线执行后的结果
type DependencyPipelineResult struct {
	BlockedPluginReasons map[string]string            `json:"blocked_plugin_reasons"`
	EnvironmentChanged   bool                         `json:"environment_changed"`
	InstallRequirements  []CombinedPackageRequirement `json:"install_requirements"`
	// InstallError 记录自动安装失败原因，Python 版本只写日志；Go 侧同时暴露给调用方
	InstallError string `json:"install_error,omitempty"`
}

type pyprojectDependencyConfig struct {
	Project struct {
		Dependencies []string `toml:"dependencies"`
	} `toml:"project"`
	Tool struct {
		UV struct {
			Index []struct {
				URL     string `toml:"url"`
				Default bool   `toml:"default"`
			} `toml:"index"`
			ConstraintDependencies []string `toml:"constraint-dependencies"`
		} `toml:"uv"`
	} `toml:"tool"`
}

// PluginDependencyPipeline 负责插件 Python 依赖分析与补装
type PluginDependencyPipeline struct {
	ProjectRoot string
	PythonBin   string
	// InstalledVersions 用于测试或宿主已有环境快照；非 nil 时不再启动 Python 查询
	InstalledVersions map[string]string
	// InstalledVersionsLoader 可覆盖默认的 Python 环境查询
	InstalledVersionsLoader func(ctx context.Context) (map[string]string, error)
	// CommandRunner 可覆盖依赖安装命令执行，便于测试
	CommandRunner DependencyCommandRunner
}

// NewPluginDependencyPipeline 构造依赖流水线
func NewPluginDependencyPipeline(projectRoot string) *PluginDependencyPipeline {
	if strings.TrimSpace(projectRoot) == "" {
		projectRoot = ResolveInstanceRoot()
	}
	pythonBin := strings.TrimSpace(os.Getenv(EnvPluginPython))
	if pythonBin == "" {
		pythonBin = defaultPythonBin(projectRoot)
	}
	return &PluginDependencyPipeline{ProjectRoot: projectRoot, PythonBin: pythonBin}
}

// Execute 执行完整的依赖分析与自动安装流程
func (p *PluginDependencyPipeline) Execute(ctx context.Context, pluginDirs []string, initialBlockedPluginReasons map[string]string) DependencyPipelineResult {
	plan := p.BuildPlan(ctx, pluginDirs, initialBlockedPluginReasons)
	result := DependencyPipelineResult{
		BlockedPluginReasons: clonePluginReasonMap(plan.BlockedPluginReasons),
		InstallRequirements:  plan.InstallRequirements,
	}
	if len(plan.InstallRequirements) == 0 {
		return result
	}
	succeeded, message := p.installRequirements(ctx, plan.InstallRequirements)
	if succeeded {
		result.EnvironmentChanged = true
		return result
	}
	result.InstallError = message
	affected := make([]string, 0)
	seen := make(map[string]struct{})
	for _, requirement := range plan.InstallRequirements {
		for _, pluginID := range requirement.PluginIDs {
			if _, exists := seen[pluginID]; exists {
				continue
			}
			seen[pluginID] = struct{}{}
			affected = append(affected, pluginID)
		}
	}
	sort.Strings(affected)
	// 安装失败只记录日志，插件仍尝试加载，实际导入异常由 Runner 报告
	zap.L().Error("自动安装 Python 依赖失败，插件仍将尝试加载",
		zap.Strings("plugin_ids", affected), zap.String("error", message))
	return result
}

// BuildPlan 构建依赖分析计划
func (p *PluginDependencyPipeline) BuildPlan(ctx context.Context, pluginDirs []string, initialBlockedPluginReasons map[string]string) DependencyPipelinePlan {
	manifests := CollectPluginDependencyManifests(pluginDirs)
	blocked := normalizePluginBlockedReasons(initialBlockedPluginReasons)

	installed, err := p.installedVersionMap(ctx)
	if err != nil {
		zap.L().Warn("读取 Python 已安装包版本失败，依赖冲突检测将按未安装处理", zap.Error(err))
	}

	for pluginID, reason := range p.detectHostConflicts(manifests, installed) {
		appendPluginBlockReason(blocked, pluginID, reason)
	}
	for pluginID, reason := range p.detectPluginConflicts(manifests, blocked, installed) {
		appendPluginBlockReason(blocked, pluginID, reason)
	}

	installRequirements := p.buildInstallRequirements(manifests, blocked, installed)
	return DependencyPipelinePlan{
		BlockedPluginReasons: blocked,
		InstallRequirements:  installRequirements,
	}
}

func (p *PluginDependencyPipeline) detectHostConflicts(manifests map[string]PluginDependencyManifest, installed map[string]string) map[string]string {
	hostRequirements := p.loadHostDependencyRequirements()
	blocked := make(map[string]string)
	pluginIDs := sortedDependencyManifestIDs(manifests)
	for _, pluginID := range pluginIDs {
		manifest := manifests[pluginID]
		for _, dependency := range manifest.Dependencies {
			specSet, ok := ParsePEP440SpecifierSet(dependency.VersionSpec)
			if !ok {
				appendPluginBlockReason(blocked, manifest.ID,
					"Python 包依赖声明无效: "+dependency.PackageName+dependency.VersionSpec)
				continue
			}
			// 已有依赖直接沿用，不因插件声明的版本范围提前阻止加载
			if installedVersion, exists := findInstalledPackageVersion(installed, dependency.PackageName); exists && installedVersion != "" {
				continue
			}
			hostRequirement, exists := hostRequirements[CanonicalizePythonPackageName(dependency.PackageName)]
			if !exists {
				continue
			}
			if RequirementsMayOverlap(hostRequirement.SpecSet, specSet) {
				continue
			}
			hostSpecifierText := hostRequirement.Specifier
			if strings.TrimSpace(hostSpecifierText) == "" {
				hostSpecifierText = "任意版本"
			}
			appendPluginBlockReason(blocked, manifest.ID,
				"Python 包依赖与主程序冲突: "+dependency.PackageName+" 需要 "+dependency.VersionSpec+
					"，主程序约束为 "+hostSpecifierText)
		}
	}
	return blocked
}

func (p *PluginDependencyPipeline) detectPluginConflicts(
	manifests map[string]PluginDependencyManifest,
	blockedPluginReasons map[string]string,
	installed map[string]string,
) map[string]string {
	conflicts := make(map[string]string)
	usages := collectPackageDependencyUsages(manifests, blockedPluginReasons)
	packageNames := make([]string, 0, len(usages))
	for packageName := range usages {
		packageNames = append(packageNames, packageName)
	}
	sort.Strings(packageNames)
	for _, normalizedName := range packageNames {
		packageUsages := usages[normalizedName]
		displayPackageName := packageUsages[0].PackageName
		if installedVersion, exists := findInstalledPackageVersion(installed, displayPackageName); exists && installedVersion != "" {
			continue
		}
		for index, left := range packageUsages {
			for _, right := range packageUsages[index+1:] {
				leftSpec, leftOK := ParsePEP440SpecifierSet(left.VersionSpec)
				rightSpec, rightOK := ParsePEP440SpecifierSet(right.VersionSpec)
				if !leftOK || !rightOK {
					continue
				}
				if RequirementsMayOverlap(leftSpec, rightSpec) {
					continue
				}
				appendPluginBlockReason(conflicts, left.PluginID,
					"Python 包依赖冲突: 与插件 "+right.PluginID+" 在 "+displayPackageName+" 上的约束不兼容 ("+
						left.VersionSpec+" vs "+right.VersionSpec+")")
				appendPluginBlockReason(conflicts, right.PluginID,
					"Python 包依赖冲突: 与插件 "+left.PluginID+" 在 "+displayPackageName+" 上的约束不兼容 ("+
						right.VersionSpec+" vs "+left.VersionSpec+")")
			}
		}
	}
	return conflicts
}

func (p *PluginDependencyPipeline) buildInstallRequirements(
	manifests map[string]PluginDependencyManifest,
	blockedPluginReasons map[string]string,
	installed map[string]string,
) []CombinedPackageRequirement {
	usages := collectPackageDependencyUsages(manifests, blockedPluginReasons)
	packageNames := make([]string, 0, len(usages))
	for packageName := range usages {
		packageNames = append(packageNames, packageName)
	}
	sort.Strings(packageNames)
	requirements := make([]CombinedPackageRequirement, 0, len(packageNames))
	for _, normalizedName := range packageNames {
		packageUsages := usages[normalizedName]
		pluginIDSet := make(map[string]struct{})
		specTexts := make([]string, 0, len(packageUsages))
		for _, usage := range packageUsages {
			pluginIDSet[usage.PluginID] = struct{}{}
			specTexts = append(specTexts, usage.VersionSpec)
		}
		mergedSpecifierText := mergePythonSpecifierTexts(specTexts)
		packageName := packageUsages[0].PackageName
		requirementText := packageName + mergedSpecifierText
		if installedVersion, exists := findInstalledPackageVersion(installed, packageName); exists && installedVersion != "" {
			continue
		}
		pluginIDs := make([]string, 0, len(pluginIDSet))
		for pluginID := range pluginIDSet {
			pluginIDs = append(pluginIDs, pluginID)
		}
		sort.Strings(pluginIDs)
		requirements = append(requirements, CombinedPackageRequirement{
			PackageName:     packageName,
			PluginIDs:       pluginIDs,
			RequirementText: requirementText,
			VersionSpec:     mergedSpecifierText,
		})
	}
	sort.Slice(requirements, func(i, j int) bool {
		return CanonicalizePythonPackageName(requirements[i].PackageName) <
			CanonicalizePythonPackageName(requirements[j].PackageName)
	})
	return requirements
}

func collectPackageDependencyUsages(
	manifests map[string]PluginDependencyManifest,
	blockedPluginReasons map[string]string,
) map[string][]PackageDependencyUsage {
	usages := make(map[string][]PackageDependencyUsage)
	for _, pluginID := range sortedDependencyManifestIDs(manifests) {
		manifest := manifests[pluginID]
		if _, blocked := blockedPluginReasons[manifest.ID]; blocked {
			continue
		}
		for _, dependency := range manifest.Dependencies {
			normalized := CanonicalizePythonPackageName(dependency.PackageName)
			usages[normalized] = append(usages[normalized], PackageDependencyUsage{
				PackageName: dependency.PackageName,
				PluginID:    manifest.ID,
				VersionSpec: dependency.VersionSpec,
			})
		}
	}
	return usages
}

func sortedDependencyManifestIDs(manifests map[string]PluginDependencyManifest) []string {
	ids := make([]string, 0, len(manifests))
	for pluginID := range manifests {
		ids = append(ids, pluginID)
	}
	sort.Strings(ids)
	return ids
}

func normalizePluginBlockedReasons(input map[string]string) map[string]string {
	normalized := make(map[string]string)
	for pluginID, reason := range input {
		trimmedID := strings.TrimSpace(pluginID)
		trimmedReason := strings.TrimSpace(reason)
		if trimmedID == "" || trimmedReason == "" {
			continue
		}
		normalized[trimmedID] = trimmedReason
	}
	return normalized
}

func appendPluginBlockReason(blocked map[string]string, pluginID, reason string) {
	existing, exists := blocked[pluginID]
	if !exists {
		blocked[pluginID] = reason
		return
	}
	for _, part := range strings.Split(existing, "；") {
		if strings.TrimSpace(part) == reason {
			return
		}
	}
	blocked[pluginID] = existing + "；" + reason
}

func clonePluginReasonMap(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func findInstalledPackageVersion(installed map[string]string, packageName string) (string, bool) {
	if len(installed) == 0 {
		return "", false
	}
	version, exists := installed[CanonicalizePythonPackageName(packageName)]
	return version, exists
}

func (p *PluginDependencyPipeline) loadHostDependencyRequirements() map[string]pythonRequirement {
	requirements := make(map[string]pythonRequirement)
	config, err := p.loadPyprojectDependencyConfig()
	if err != nil {
		return requirements
	}
	for _, rawDependency := range config.Project.Dependencies {
		requirement, ok := parsePythonRequirement(rawDependency)
		if !ok {
			continue
		}
		requirements[requirement.Normalized] = requirement
	}
	return requirements
}

func (p *PluginDependencyPipeline) loadPyprojectDependencyConfig() (pyprojectDependencyConfig, error) {
	var config pyprojectDependencyConfig
	path := filepath.Join(p.ProjectRoot, "pyproject.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		// Go 部署不一定带 pyproject.toml（二进制分发没有 Python 工程文件）：
		// 缺文件时按「无自定义索引」处理，走安装工具默认源，而不是取消安装。
		if errors.Is(err, fs.ErrNotExist) {
			return config, nil
		}
		return config, err
	}
	if err := toml.Unmarshal(raw, &config); err != nil {
		return config, err
	}
	return config, nil
}

// LoadPackageIndexSettings 读取主程序 pyproject.toml 中 [tool.uv] 的索引顺序与约束依赖
func (p *PluginDependencyPipeline) LoadPackageIndexSettings() (PackageIndexSettings, error) {
	config, err := p.loadPyprojectDependencyConfig()
	if err != nil {
		return PackageIndexSettings{}, err
	}
	settings := PackageIndexSettings{
		IndexURLs:   []string{},
		Constraints: []string{},
	}
	for _, index := range config.Tool.UV.Index {
		if !index.Default {
			settings.IndexURLs = append(settings.IndexURLs, index.URL)
		}
	}
	for _, index := range config.Tool.UV.Index {
		if index.Default {
			settings.IndexURLs = append(settings.IndexURLs, index.URL)
		}
	}
	for _, constraint := range config.Tool.UV.ConstraintDependencies {
		settings.Constraints = append(settings.Constraints, constraint)
	}
	// 环境变量优先：部署环境（如国内网络）可直接指定镜像源，无需提供 pyproject.toml
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_PLUGIN_DEP_INDEX_URLS")); raw != "" {
		urls := make([]string, 0, 2)
		for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\n'
		}) {
			if item = strings.TrimSpace(item); item != "" {
				urls = append(urls, item)
			}
		}
		if len(urls) > 0 {
			settings.IndexURLs = urls
		}
	}
	return settings, nil
}
