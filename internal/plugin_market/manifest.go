package pluginmarket

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bytedance/sonic"
)

// 对齐 manifest_validator 的正则约束
var (
	pluginIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+$`)
	packageNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	httpURLPattern       = regexp.MustCompile(`^https?://.+$`)
	iconNamePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	hexColorPattern      = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	localIconSuffixes    = map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}, ".svg": {}, ".webp": {}}
	supportedManifestSet = map[int]struct{}{2: {}}
)

var manifestTopLevelKeys = []string{
	"manifest_version", "version", "name", "description", "author", "license", "urls",
	"host_application", "sdk", "dependencies", "llm_providers", "capabilities", "i18n",
	"id", "plugin_type", "type", "display", "changelog",
}

// PluginDependency 对齐 manifest_validator.PluginDependencyDefinition
type PluginDependency struct {
	ID          string
	VersionSpec string
}

// PluginManifest 对齐 manifest_validator.PluginManifest 的字段子集
type PluginManifest struct {
	ID                     string
	Name                   string
	Version                string
	Description            string
	ManifestVersion        int
	PluginDependencies     []PluginDependency
	PythonPackageDeps      []PluginDependency
	HostApplicationVersion [2]string
	SDKVersion             [2]string
	Capabilities           []string
}

type manifestValidation struct {
	manifest *PluginManifest
	errors   []string
	warnings []string
}

// ValidateManifest 对齐 ManifestValidator.parse_manifest 的错误输出
func ValidateManifest(raw map[string]any, hostVersion string) []string {
	return validateManifestDetailed(raw, hostVersion).errors
}

// ParseManifest 解析并校验 manifest，失败返回 nil 与错误列表
func ParseManifest(raw map[string]any, hostVersion string) (*PluginManifest, []string) {
	result := validateManifestDetailed(raw, hostVersion)
	return result.manifest, result.errors
}

// LoadManifestFromPluginPath 对齐 load_from_plugin_path：要求 _manifest.json 与 plugin.py
func LoadManifestFromPluginPath(pluginPath, hostVersion string, requireEntrypoint bool) (*PluginManifest, []string, error) {
	manifestPath := filepath.Join(pluginPath, "_manifest.json")
	info, err := os.Stat(manifestPath)
	if err != nil || info.IsDir() {
		return nil, []string{"缺少 _manifest.json"}, nil
	}
	if requireEntrypoint {
		entrypoint := filepath.Join(pluginPath, "plugin.py")
		if stat, statErr := os.Stat(entrypoint); statErr != nil || stat.IsDir() {
			return nil, []string{"缺少 plugin.py"}, nil
		}
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, []string{"manifest 解析失败: " + err.Error()}, nil
	}
	var payload any
	if err := sonic.UnmarshalString(string(raw), &payload); err != nil {
		return nil, []string{"manifest 解析失败: " + err.Error()}, nil
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, []string{"manifest 顶层必须为 JSON 对象"}, nil
	}
	result := validateManifestDetailed(object, hostVersion)
	return result.manifest, result.errors, nil
}

func validateManifestDetailed(raw map[string]any, hostVersion string) manifestValidation {
	result := manifestValidation{errors: []string{}}
	manifestVersion := safeInt(raw["manifest_version"], 0)
	if _, ok := supportedManifestSet[manifestVersion]; !ok {
		result.errors = append(result.errors, fmt.Sprintf(
			"Manifest 版本不兼容: manifest_version=%v，仅支持 2", raw["manifest_version"]))
		return result
	}
	if err := rejectUnknownKeys(raw, manifestTopLevelKeys, "PluginManifest"); err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	manifest := &PluginManifest{ManifestVersion: manifestVersion}
	if err := parseManifestFields(raw, manifest); err != nil {
		result.errors = append(result.errors, err.Error())
		return result
	}
	if hostVersion = strings.TrimSpace(hostVersion); hostVersion == "" {
		hostVersion = DefaultMaibotVersion()
	}
	ok, message := isInVersionRange(hostVersion, manifest.HostApplicationVersion[0], manifest.HostApplicationVersion[1])
	if !ok {
		if isSameMajorMinorHigher(hostVersion, manifest.HostApplicationVersion[1]) {
			result.warnings = append(result.warnings, fmt.Sprintf(
				"当前版本 %s 以兼容模式加载插件（插件声明的 Host 最高支持版本为 %s）",
				hostVersion, normalizeVersion(manifest.HostApplicationVersion[1])))
		} else {
			result.errors = append(result.errors, fmt.Sprintf("Host 版本不兼容: %s (当前 Host: %s)", message, hostVersion))
		}
	}
	if len(result.errors) > 0 {
		return manifestValidation{errors: result.errors, warnings: result.warnings}
	}
	result.manifest = manifest
	return result
}

func parseManifestFields(raw map[string]any, manifest *PluginManifest) error {
	id, err := requireNonEmptyString(raw["id"], "id")
	if err != nil {
		return err
	}
	if !pluginIDPattern.MatchString(id) {
		return fmt.Errorf("id 必须使用字母/数字/下划线，并以点号或横线分隔，例如 github.author.plugin")
	}
	name, err := requireNonEmptyString(raw["name"], "name")
	if err != nil {
		return err
	}
	description, err := requireNonEmptyString(raw["description"], "description")
	if err != nil {
		return err
	}
	if _, err := requireNonEmptyString(raw["license"], "license"); err != nil {
		return err
	}
	version, err := requireNonEmptyString(raw["version"], "version")
	if err != nil {
		return err
	}
	if !isValidSemver(version) {
		return fmt.Errorf("version 必须为严格三段式版本号，例如 1.0.0")
	}
	author, ok := anyMap(raw["author"])
	if !ok {
		return fmt.Errorf("author 必须为对象")
	}
	if _, err := requireNonEmptyString(author["name"], "author.name"); err != nil {
		return err
	}
	authorURL, err := requireNonEmptyString(author["url"], "author.url")
	if err != nil {
		return err
	}
	if !httpURLPattern.MatchString(authorURL) {
		return fmt.Errorf("author.url 必须为 http:// 或 https:// 开头的 URL")
	}
	if err := validateManifestURLs(raw["urls"]); err != nil {
		return err
	}
	hostMin, hostMax, err := parseVersionRange(raw["host_application"], "host_application")
	if err != nil {
		return err
	}
	sdkMin, sdkMax, err := parseVersionRange(raw["sdk"], "sdk")
	if err != nil {
		return err
	}
	dependencies, pythonDeps, err := parseManifestDependencies(raw["dependencies"], id)
	if err != nil {
		return err
	}
	capabilities, err := parseCapabilities(raw["capabilities"])
	if err != nil {
		return err
	}
	if err := validateManifestI18n(raw["i18n"]); err != nil {
		return err
	}
	if raw["plugin_type"] != nil {
		if _, ok := raw["plugin_type"].(string); !ok {
			return fmt.Errorf("plugin_type 必须为字符串")
		}
	}
	if err := validateManifestDisplay(raw["display"]); err != nil {
		return err
	}
	if err := validateManifestChangelog(raw["changelog"]); err != nil {
		return err
	}
	if err := validateLLMProviders(raw["llm_providers"]); err != nil {
		return err
	}

	manifest.ID = id
	manifest.Name = name
	manifest.Version = version
	manifest.Description = description
	manifest.PluginDependencies = dependencies
	manifest.PythonPackageDeps = pythonDeps
	manifest.HostApplicationVersion = [2]string{hostMin, hostMax}
	manifest.SDKVersion = [2]string{sdkMin, sdkMax}
	manifest.Capabilities = capabilities
	return nil
}

func requireNonEmptyString(value any, field string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须为字符串", field)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("%s 不能为空", field)
	}
	return text, nil
}

func validateManifestURLs(value any) error {
	urls, ok := anyMap(value)
	if !ok {
		return fmt.Errorf("urls 必须为对象")
	}
	repository, err := requireNonEmptyString(urls["repository"], "urls.repository")
	if err != nil {
		return err
	}
	if !httpURLPattern.MatchString(repository) {
		return fmt.Errorf("urls.repository 必须为 http:// 或 https:// 开头的 URL")
	}
	for _, key := range []string{"homepage", "documentation", "issues"} {
		if urls[key] == nil {
			continue
		}
		text, ok := urls[key].(string)
		if !ok {
			return fmt.Errorf("urls.%s 必须为字符串", key)
		}
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("urls.%s 不能为空字符串", key)
		}
		if !httpURLPattern.MatchString(strings.TrimSpace(text)) {
			return fmt.Errorf("urls.%s 必须为 http:// 或 https:// 开头的 URL", key)
		}
	}
	return nil
}

func parseVersionRange(value any, field string) (string, string, error) {
	rangeMap, ok := anyMap(value)
	if !ok {
		return "", "", fmt.Errorf("%s 必须为对象", field)
	}
	minVersion, err := requireNonEmptyString(rangeMap["min_version"], field+".min_version")
	if err != nil {
		return "", "", err
	}
	maxVersion, err := requireNonEmptyString(rangeMap["max_version"], field+".max_version")
	if err != nil {
		return "", "", err
	}
	if !isValidSemver(minVersion) {
		return "", "", fmt.Errorf("%s.min_version 必须为严格三段式版本号，例如 1.0.0", field)
	}
	if !isValidSemver(maxVersion) {
		return "", "", fmt.Errorf("%s.max_version 必须为严格三段式版本号", field)
	}
	if compareVersions(minVersion, maxVersion) > 0 {
		return "", "", fmt.Errorf("%s.min_version 不能大于 max_version", field)
	}
	return minVersion, maxVersion, nil
}

func parseManifestDependencies(value any, manifestID string) ([]PluginDependency, []PluginDependency, error) {
	if value == nil {
		return []PluginDependency{}, []PluginDependency{}, nil
	}
	list, ok := anyList(value)
	if !ok {
		return nil, nil, fmt.Errorf("dependencies 必须为列表")
	}
	pluginDeps := []PluginDependency{}
	pythonDeps := []PluginDependency{}
	seenPlugin := map[string]struct{}{}
	seenPackage := map[string]struct{}{}
	for _, item := range list {
		dependency, ok := anyMap(item)
		if !ok {
			return nil, nil, fmt.Errorf("dependencies 中存在非对象条目")
		}
		depType := safeString(dependency["type"])
		versionSpec, err := requireNonEmptyString(dependency["version_spec"], "dependencies.version_spec")
		if err != nil {
			return nil, nil, err
		}
		if _, err := parseVersionSpecifiers(versionSpec); err != nil {
			return nil, nil, fmt.Errorf("dependencies.version_spec 无效: %s", versionSpec)
		}
		switch depType {
		case "plugin":
			depID, err := requireNonEmptyString(dependency["id"], "dependencies.id")
			if err != nil {
				return nil, nil, err
			}
			if !pluginIDPattern.MatchString(depID) {
				return nil, nil, fmt.Errorf("dependencies.id 必须使用字母/数字/下划线，并以点号或横线分隔")
			}
			if depID == manifestID {
				return nil, nil, fmt.Errorf("dependencies 中的插件依赖不能依赖自身")
			}
			if _, ok := seenPlugin[depID]; ok {
				return nil, nil, fmt.Errorf("存在重复的插件依赖声明: %s", depID)
			}
			seenPlugin[depID] = struct{}{}
			pluginDeps = append(pluginDeps, PluginDependency{ID: depID, VersionSpec: versionSpec})
		case "python_package":
			name, err := requireNonEmptyString(dependency["name"], "dependencies.name")
			if err != nil {
				return nil, nil, err
			}
			if !packageNamePattern.MatchString(name) {
				return nil, nil, fmt.Errorf("dependencies.name 只能包含字母、数字、点号、下划线和横线")
			}
			key := canonicalPackageName(name)
			if _, ok := seenPackage[key]; ok {
				return nil, nil, fmt.Errorf("存在重复的 Python 包依赖声明: %s", name)
			}
			seenPackage[key] = struct{}{}
			pythonDeps = append(pythonDeps, PluginDependency{ID: name, VersionSpec: versionSpec})
		default:
			return nil, nil, fmt.Errorf("dependencies.type 必须为 plugin 或 python_package")
		}
	}
	return pluginDeps, pythonDeps, nil
}

func canonicalPackageName(name string) string {
	replacer := strings.NewReplacer("_", "-", ".", "-")
	return strings.ToLower(replacer.Replace(name))
}

func parseCapabilities(value any) ([]string, error) {
	if value == nil {
		return nil, fmt.Errorf("capabilities 必须为列表")
	}
	list, ok := anyList(value)
	if !ok {
		return nil, fmt.Errorf("capabilities 必须为列表")
	}
	out := make([]string, 0, len(list))
	seen := map[string]struct{}{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("capabilities 中存在非字符串条目")
		}
		capability := strings.TrimSpace(text)
		if capability == "" {
			return nil, fmt.Errorf("capabilities 中存在空能力名")
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out, nil
}

func validateManifestI18n(value any) error {
	i18n, ok := anyMap(value)
	if !ok {
		return fmt.Errorf("i18n 必须为对象")
	}
	if _, err := requireNonEmptyString(i18n["default_locale"], "i18n.default_locale"); err != nil {
		return err
	}
	if i18n["locales_path"] != nil {
		if _, err := requireNonEmptyString(i18n["locales_path"], "i18n.locales_path"); err != nil {
			return err
		}
	}
	locales := []string{}
	if i18n["supported_locales"] != nil {
		list, ok := anyList(i18n["supported_locales"])
		if !ok {
			return fmt.Errorf("i18n.supported_locales 必须为列表")
		}
		for _, item := range list {
			locale := strings.TrimSpace(safeString(item))
			if locale == "" {
				return fmt.Errorf("语言列表中存在空值")
			}
			duplicated := false
			for _, existing := range locales {
				if existing == locale {
					duplicated = true
					break
				}
			}
			if !duplicated {
				locales = append(locales, locale)
			}
		}
	}
	if len(locales) > 0 {
		defaultLocale := strings.TrimSpace(safeString(i18n["default_locale"]))
		found := false
		for _, locale := range locales {
			if locale == defaultLocale {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("default_locale 必须包含在 supported_locales 中")
		}
	}
	return nil
}

func validateManifestDisplay(value any) error {
	if value == nil {
		return nil
	}
	display, ok := anyMap(value)
	if !ok {
		return fmt.Errorf("display 必须为对象")
	}
	iconRaw, ok := anyMap(display["icon"])
	if !ok {
		if display["icon"] == nil {
			return nil
		}
		return fmt.Errorf("display.icon 必须为对象")
	}
	iconType := safeString(iconRaw["type"])
	iconValue, err := requireNonEmptyString(iconRaw["value"], "display.icon.value")
	if err != nil {
		return err
	}
	if iconRaw["fallback"] != nil {
		fallback := strings.TrimSpace(safeString(iconRaw["fallback"]))
		if fallback == "" || !iconNamePattern.MatchString(fallback) {
			return fmt.Errorf("display.icon.fallback 只能包含字母、数字、下划线和横线")
		}
	}
	if iconRaw["background"] != nil {
		background := strings.TrimSpace(safeString(iconRaw["background"]))
		if !hexColorPattern.MatchString(background) {
			return fmt.Errorf("display.icon.background 必须为 #RRGGBB 格式")
		}
	}
	switch iconType {
	case "lucide":
		if !iconNamePattern.MatchString(iconValue) {
			return fmt.Errorf("lucide 图标名只能包含字母、数字、下划线和横线")
		}
	case "local":
		if err := validateLocalIconPath(iconValue); err != nil {
			return err
		}
	case "emoji":
	default:
		return fmt.Errorf("display.icon.type 必须为 lucide、emoji 或 local")
	}
	return nil
}

func validateLocalIconPath(iconPath string) error {
	if strings.HasPrefix(iconPath, "/") || strings.HasPrefix(iconPath, "\\") {
		return fmt.Errorf("local 图标路径包含非法字符")
	}
	if strings.Contains(iconPath, "\x00") {
		return fmt.Errorf("local 图标路径包含非法字符")
	}
	for _, part := range strings.FieldsFunc(iconPath, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("local 图标路径必须是插件目录内的相对路径")
		}
	}
	suffix := strings.ToLower(filepath.Ext(iconPath))
	if _, ok := localIconSuffixes[suffix]; !ok {
		return fmt.Errorf("local 图标仅支持 jpg、jpeg、png、svg、webp")
	}
	return nil
}

func validateManifestChangelog(value any) error {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("changelog 必须为字符串")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("changelog 不能为空字符串")
	}
	if httpURLPattern.MatchString(text) {
		return nil
	}
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "\\") || strings.Contains(text, "\x00") {
		return fmt.Errorf("changelog 路径包含非法字符")
	}
	for _, part := range strings.Split(strings.ReplaceAll(text, "\\", "/"), "/") {
		if part == ".." {
			return fmt.Errorf("changelog 必须为插件目录内的相对路径或 http(s) URL")
		}
	}
	if !strings.HasSuffix(strings.ToLower(text), ".md") {
		return fmt.Errorf("插件内更新日志路径必须指向 Markdown 文件")
	}
	return nil
}

func validateLLMProviders(value any) error {
	if value == nil {
		return nil
	}
	list, ok := anyList(value)
	if !ok {
		return fmt.Errorf("llm_providers 必须为列表")
	}
	seen := map[string]struct{}{}
	for _, item := range list {
		provider, ok := anyMap(item)
		if !ok {
			return fmt.Errorf("llm_providers 中存在非对象条目")
		}
		clientType, err := requireNonEmptyString(provider["client_type"], "llm_providers.client_type")
		if err != nil {
			return err
		}
		if _, ok := seen[clientType]; ok {
			return fmt.Errorf("存在重复的 LLM Provider 声明: %s", clientType)
		}
		seen[clientType] = struct{}{}
	}
	return nil
}

// GetUnsatisfiedPluginDependencies 对齐 get_unsatisfied_plugin_dependencies
func GetUnsatisfiedPluginDependencies(manifest *PluginManifest, available map[string]string) []string {
	unsatisfied := []string{}
	for _, dependency := range manifest.PluginDependencies {
		version, ok := available[dependency.ID]
		if !ok || version == "" {
			unsatisfied = append(unsatisfied, dependency.ID+" (未找到依赖插件)")
			continue
		}
		if !IsPluginDependencySatisfied(dependency, version) {
			unsatisfied = append(unsatisfied, fmt.Sprintf("%s (需要 %s，当前 %s)", dependency.ID, dependency.VersionSpec, version))
		}
	}
	return unsatisfied
}

// IsPluginDependencySatisfied 对齐 is_plugin_dependency_satisfied
func IsPluginDependencySatisfied(dependency PluginDependency, pluginVersion string) bool {
	specifiers, err := parseVersionSpecifiers(dependency.VersionSpec)
	if err != nil {
		return false
	}
	return versionMatchesSpecifiers(pluginVersion, specifiers)
}

func manifestVersionEquals(raw map[string]any, expected int) bool {
	switch value := raw["manifest_version"].(type) {
	case int:
		return value == expected
	case int64:
		return int(value) == expected
	case float64:
		return value == float64(expected)
	}
	return false
}
