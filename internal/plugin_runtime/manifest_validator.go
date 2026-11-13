package pluginruntime

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// ManifestValidator 是插件 Manifest v2 的严格校验器
type ManifestValidator struct {
	ProjectRoot string
	HostVersion string
	SDKVersion  string
	PythonBin   string
	// ForcePluginCompatibility 跳过 Host / SDK 版本范围校验，仅记录告警
	ForcePluginCompatibility bool
	// SkipPythonPackageDependencies 关闭 Python 包依赖与运行环境校验
	SkipPythonPackageDependencies bool
	// InstalledVersions 用于测试或宿主已有环境快照；非 nil 时不再启动 Python 查询
	InstalledVersions map[string]string
	// InstalledVersionsLoader 可覆盖默认的 Python 环境查询
	InstalledVersionsLoader func(ctx context.Context) (map[string]string, error)

	hostResolved       bool
	sdkResolved        bool
	installedResolved  bool
	installedVersions  map[string]string
	hostRequirements   map[string]pythonRequirement
	requirementsLoaded bool
}

// NewManifestValidator 构造默认校验器
func NewManifestValidator(projectRoot string) *ManifestValidator {
	if strings.TrimSpace(projectRoot) == "" {
		projectRoot = ResolveInstanceRoot()
	}
	pythonBin := strings.TrimSpace(os.Getenv(EnvPluginPython))
	if pythonBin == "" {
		pythonBin = defaultPythonBin(projectRoot)
	}
	return &ManifestValidator{ProjectRoot: projectRoot, PythonBin: pythonBin}
}

// ParseManifest 校验原始 Manifest 字典，skipRuntimeValidation 用于解析禁用插件
func (validator *ManifestValidator) ParseManifest(raw map[string]any, source string, skipRuntimeValidation bool) ManifestValidation {
	result := ManifestValidation{Source: source, Errors: []string{}, Warnings: []string{}}
	manifest, structuralErrors := parsePluginManifest(raw)
	if len(structuralErrors) > 0 {
		result.Errors = append(result.Errors, structuralErrors...)
		return result
	}
	if !skipRuntimeValidation {
		validator.validateRuntimeCompatibility(manifest, &result.Errors, &result.Warnings)
	}
	if len(result.Errors) > 0 {
		return result
	}
	if strings.TrimSpace(result.Source) == "" {
		result.Source = manifest.ID
	}
	result.Manifest = manifest
	return result
}

// ValidateManifest 是 ParseManifest 的布尔包装
func (validator *ManifestValidator) ValidateManifest(raw map[string]any, source string) bool {
	return validator.ParseManifest(raw, source, false).OK()
}

// GetUnsatisfiedPluginDependencies 返回未满足的插件依赖描述
func (validator *ManifestValidator) GetUnsatisfiedPluginDependencies(manifest *PluginManifest, available map[string]string) []string {
	return GetUnsatisfiedPluginDependencies(manifest, available)
}

// GetUnsatisfiedPluginDependencies 对齐 get_unsatisfied_plugin_dependencies
func GetUnsatisfiedPluginDependencies(manifest *PluginManifest, available map[string]string) []string {
	if manifest == nil {
		return []string{}
	}
	unsatisfied := []string{}
	for _, dependency := range manifest.PluginDependencies {
		version := strings.TrimSpace(available[dependency.ID])
		if version == "" {
			unsatisfied = append(unsatisfied, dependency.ID+" (未找到依赖插件)")
			continue
		}
		if !manifestVersionMatchesSpecifier(version, dependency.VersionSpec) {
			unsatisfied = append(unsatisfied, dependency.ID+" (需要 "+dependency.VersionSpec+"，当前 "+version+")")
		}
	}
	return unsatisfied
}

// IsPluginDependencySatisfied 判断单个插件依赖是否被指定版本满足
func IsPluginDependencySatisfied(dependency PluginManifestDependency, pluginVersion string) bool {
	return manifestVersionMatchesSpecifier(strings.TrimSpace(pluginVersion), dependency.VersionSpec)
}

func (validator *ManifestValidator) validateRuntimeCompatibility(manifest *PluginManifest, errors *[]string, warnings *[]string) {
	hostVersion := validator.resolveHostVersion()
	sdkVersion := validator.resolveSDKVersion()
	if validator.ForcePluginCompatibility {
		*warnings = append(*warnings, "已开启强制插件兼容，跳过版本校验（插件声明 Host "+
			manifest.HostApplication.MinVersion+" - "+manifest.HostApplication.MaxVersion+
			"、SDK "+manifest.SDK.MinVersion+" - "+manifest.SDK.MaxVersion+
			"，当前 Host "+hostVersion+" / SDK "+sdkVersion+"）")
	} else {
		if ok, message := isManifestVersionInRange(hostVersion, manifest.HostApplication.MinVersion, manifest.HostApplication.MaxVersion); !ok {
			if isSameMajorMinorHigher(hostVersion, manifest.HostApplication.MaxVersion) {
				*warnings = append(*warnings, "当前版本 "+hostVersion+" 以兼容模式加载插件（插件声明的 Host 最高支持版本为 "+
					normalizeManifestVersion(manifest.HostApplication.MaxVersion)+"）")
			} else {
				*errors = append(*errors, "Host 版本不兼容: "+message+" (当前 Host: "+hostVersion+")")
			}
		}
		if ok, message := isManifestVersionInRange(sdkVersion, manifest.SDK.MinVersion, manifest.SDK.MaxVersion); !ok {
			*warnings = append(*warnings, "SDK 版本超出插件声明范围，已放行: "+message+" (当前 SDK: "+sdkVersion+")")
		}
	}
	if validator.SkipPythonPackageDependencies {
		return
	}
	validator.validatePythonPackageDependencies(manifest, errors, warnings)
}

func (validator *ManifestValidator) validatePythonPackageDependencies(manifest *PluginManifest, errors *[]string, warnings *[]string) {
	if len(manifest.PythonPackageDependencies) == 0 {
		return
	}
	installed := validator.resolveInstalledVersions()
	hostRequirements := validator.resolveHostRequirements()
	for _, dependency := range manifest.PythonPackageDependencies {
		spec, valid := ParsePEP440SpecifierSet(dependency.VersionSpec)
		if !valid {
			*errors = append(*errors, "Python 包依赖 "+dependency.PackageName+" 的版本约束无效: "+dependency.VersionSpec)
			continue
		}
		normalizedName := CanonicalizePythonPackageName(dependency.PackageName)
		installedVersion, installedOK := installed[normalizedName]
		if installedOK && strings.TrimSpace(installedVersion) != "" {
			if !manifestVersionMatchesSpecifier(installedVersion, dependency.VersionSpec) {
				*warnings = append(*warnings, "Python 包依赖版本不匹配: "+dependency.PackageName+
					" 需要 "+dependency.VersionSpec+"，当前运行环境为 "+installedVersion+"，沿用已安装版本并尝试加载")
			}
			continue
		}
		hostRequirement, exists := hostRequirements[normalizedName]
		if !exists {
			continue
		}
		if RequirementsMayOverlap(hostRequirement.SpecSet, spec) {
			continue
		}
		hostSpecifier := strings.TrimSpace(hostRequirement.Specifier)
		if hostSpecifier == "" {
			hostSpecifier = "任意版本"
		}
		*errors = append(*errors, "Python 包依赖冲突: "+dependency.PackageName+" 需要 "+dependency.VersionSpec+
			"，主程序依赖约束为 "+hostSpecifier)
	}
}

func (validator *ManifestValidator) resolveHostVersion() string {
	if strings.TrimSpace(validator.HostVersion) != "" {
		return strings.TrimSpace(validator.HostVersion)
	}
	if validator.hostResolved {
		return ""
	}
	validator.hostResolved = true
	validator.HostVersion = detectManifestHostVersion(validator.projectRoot())
	return validator.HostVersion
}

func (validator *ManifestValidator) resolveSDKVersion() string {
	if strings.TrimSpace(validator.SDKVersion) != "" {
		return strings.TrimSpace(validator.SDKVersion)
	}
	if validator.sdkResolved {
		return ""
	}
	validator.sdkResolved = true
	validator.SDKVersion = validator.pipeline().localSDKVersion()
	return validator.SDKVersion
}

func (validator *ManifestValidator) resolveInstalledVersions() map[string]string {
	if validator.installedResolved {
		return validator.installedVersions
	}
	validator.installedResolved = true
	installed := map[string]string{}
	if validator.InstalledVersions != nil {
		for name, version := range validator.InstalledVersions {
			installed[CanonicalizePythonPackageName(name)] = version
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var loaded map[string]string
		var err error
		if validator.InstalledVersionsLoader != nil {
			loaded, err = validator.InstalledVersionsLoader(ctx)
		} else {
			loaded, err = validator.pipeline().installedVersionMap(ctx)
		}
		if err == nil {
			for name, version := range loaded {
				installed[CanonicalizePythonPackageName(name)] = version
			}
		}
	}
	if sdkVersion := validator.pipeline().localSDKVersion(); sdkVersion != "" {
		installed[CanonicalizePythonPackageName("maibot-plugin-sdk")] = sdkVersion
	}
	validator.installedVersions = installed
	return installed
}

func (validator *ManifestValidator) resolveHostRequirements() map[string]pythonRequirement {
	if validator.requirementsLoaded {
		return validator.hostRequirements
	}
	validator.requirementsLoaded = true
	validator.hostRequirements = validator.pipeline().loadHostDependencyRequirements()
	return validator.hostRequirements
}

func (validator *ManifestValidator) pipeline() *PluginDependencyPipeline {
	return &PluginDependencyPipeline{
		ProjectRoot: validator.projectRoot(),
		PythonBin:   strings.TrimSpace(validator.PythonBin),
	}
}

func (validator *ManifestValidator) projectRoot() string {
	root := strings.TrimSpace(validator.ProjectRoot)
	if root != "" {
		return root
	}
	return ResolveInstanceRoot()
}

func detectManifestHostVersion(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return ""
	}
	var payload struct {
		Project struct {
			Version string `toml:"version"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	version := strings.TrimSpace(payload.Project.Version)
	if !isValidManifestProjectVersion(version) {
		return ""
	}
	return version
}

// sortedManifestPluginIDs 返回稳定排序的依赖映射键
func sortedManifestPluginIDs(dependencies map[string][]string) []string {
	ids := make([]string, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
