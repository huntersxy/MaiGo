package pluginruntime

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	manifestHTTPURLPattern    = regexp.MustCompile(`^https?://.+$`)
	manifestIconNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	manifestHexColorPattern   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	manifestLocalIconSuffixes = map[string]struct{}{".jpg": {}, ".jpeg": {}, ".png": {}, ".svg": {}, ".webp": {}}
	manifestSupportedVersions = map[int]struct{}{2: {}}
)

var manifestTopLevelKeys = map[string]struct{}{
	"manifest_version": {}, "version": {}, "name": {}, "description": {}, "author": {},
	"license": {}, "urls": {}, "host_application": {}, "sdk": {}, "dependencies": {},
	"llm_providers": {}, "capabilities": {}, "i18n": {}, "id": {}, "plugin_type": {},
	"type": {}, "display": {}, "changelog": {},
}

var manifestAuthorKeys = map[string]struct{}{"name": {}, "url": {}}
var manifestURLKeys = map[string]struct{}{"repository": {}, "homepage": {}, "documentation": {}, "issues": {}}
var manifestRangeKeys = map[string]struct{}{"min_version": {}, "max_version": {}}
var manifestI18nKeys = map[string]struct{}{"default_locale": {}, "locales_path": {}, "supported_locales": {}}
var manifestPluginDependencyKeys = map[string]struct{}{"type": {}, "id": {}, "version_spec": {}}
var manifestPythonDependencyKeys = map[string]struct{}{"type": {}, "name": {}, "version_spec": {}}
var manifestLLMProviderKeys = map[string]struct{}{"client_type": {}, "name": {}, "description": {}, "version": {}}
var manifestDisplayKeys = map[string]struct{}{"icon": {}}
var manifestDisplayIconKeys = map[string]struct{}{"type": {}, "value": {}, "fallback": {}, "background": {}}

type manifestErrors struct {
	items []string
}

func (errors *manifestErrors) add(location, message string) {
	errors.items = append(errors.items, "字段 "+location+" 校验失败: "+message)
}

func (errors *manifestErrors) missing(location string) {
	errors.items = append(errors.items, "缺少必需字段: "+location)
}

func (errors *manifestErrors) extra(location string) {
	errors.items = append(errors.items, "存在未声明字段: "+location)
}

func (errors *manifestErrors) typeError(location string) {
	errors.items = append(errors.items, "字段 "+location+" 的类型不正确")
}

func (errors *manifestErrors) literal(location string, values ...string) {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	errors.items = append(errors.items, "字段 "+location+" 的值不合法，必须为 ["+strings.Join(quoted, ", ")+"]")
}

func (errors *manifestErrors) empty() bool { return len(errors.items) == 0 }

func manifestRepr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return "'" + typed + "'"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

func rejectManifestKeys(raw map[string]any, allowed map[string]struct{}, prefix string, errors *manifestErrors) {
	if len(raw) == 0 {
		return
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; ok {
			continue
		}
		location := key
		if prefix != "" {
			location = prefix + "." + key
		}
		errors.extra(location)
	}
}

func manifestObject(raw map[string]any, key, location string, required bool, errors *manifestErrors) (map[string]any, bool) {
	value, exists := raw[key]
	if !exists || value == nil {
		if required {
			errors.missing(location)
		}
		return nil, false
	}
	object, ok := value.(map[string]any)
	if !ok {
		errors.typeError(location)
		return nil, false
	}
	return object, true
}

func manifestString(raw map[string]any, key, location string, required bool, errors *manifestErrors) (string, bool) {
	value, exists := raw[key]
	if !exists || value == nil {
		if required {
			errors.missing(location)
		}
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		errors.typeError(location)
		return "", false
	}
	text = strings.TrimSpace(text)
	if required && text == "" {
		errors.add(location, "不能为空")
		return "", false
	}
	return text, true
}

func manifestOptionalString(raw map[string]any, key, location string, errors *manifestErrors) (string, bool) {
	value, exists := raw[key]
	if !exists || value == nil {
		return "", true
	}
	text, ok := value.(string)
	if !ok {
		errors.typeError(location)
		return "", false
	}
	return strings.TrimSpace(text), true
}

func manifestList(raw map[string]any, key, location string, required bool, errors *manifestErrors) ([]any, bool) {
	value, exists := raw[key]
	if !exists || value == nil {
		if required {
			errors.missing(location)
		}
		return nil, false
	}
	list, ok := value.([]any)
	if !ok {
		errors.typeError(location)
		return nil, false
	}
	return list, true
}

func parsePluginManifest(raw map[string]any) (*PluginManifest, []string) {
	errors := &manifestErrors{}
	versionValue, exists := raw["manifest_version"]
	if !exists {
		errors.items = append(errors.items, "Manifest 版本不兼容: manifest_version=None，仅支持 2")
		return nil, errors.items
	}
	versionNumber, versionOK := manifestInt(versionValue)
	if !versionOK {
		errors.items = append(errors.items, "Manifest 版本不兼容: manifest_version="+manifestRepr(versionValue)+"，仅支持 2")
		return nil, errors.items
	}
	if _, supported := manifestSupportedVersions[versionNumber]; !supported {
		errors.items = append(errors.items, "Manifest 版本不兼容: manifest_version="+manifestRepr(versionValue)+"，仅支持 2")
		return nil, errors.items
	}
	rejectManifestKeys(raw, manifestTopLevelKeys, "", errors)

	manifest := &PluginManifest{ManifestVersion: versionNumber, PluginType: "extension"}
	manifest.ID, _ = manifestString(raw, "id", "id", true, errors)
	manifest.Name, _ = manifestString(raw, "name", "name", true, errors)
	manifest.Description, _ = manifestString(raw, "description", "description", true, errors)
	manifest.License, _ = manifestString(raw, "license", "license", true, errors)
	manifest.Version, _ = manifestString(raw, "version", "version", true, errors)
	if manifest.Version != "" && !isValidManifestSemver(manifest.Version) {
		errors.add("version", "必须为严格三段式版本号，例如 1.0.0")
	}
	if manifest.ID != "" && !isSafePythonPluginID(manifest.ID) {
		errors.add("id", "必须使用字母/数字/下划线，并以点号或横线分隔，例如 github.author.plugin")
	}

	if author, ok := manifestObject(raw, "author", "author", true, errors); ok {
		rejectManifestKeys(author, manifestAuthorKeys, "author", errors)
		manifest.Author.Name, _ = manifestString(author, "name", "author.name", true, errors)
		if url, present := manifestString(author, "url", "author.url", true, errors); present {
			manifest.Author.URL = url
			if !manifestHTTPURLPattern.MatchString(url) {
				errors.add("author.url", "必须为 http:// 或 https:// 开头的 URL")
			}
		}
	}

	if urls, ok := manifestObject(raw, "urls", "urls", true, errors); ok {
		rejectManifestKeys(urls, manifestURLKeys, "urls", errors)
		if repository, present := manifestString(urls, "repository", "urls.repository", true, errors); present {
			manifest.URLs.Repository = repository
			if !manifestHTTPURLPattern.MatchString(repository) {
				errors.add("urls.repository", "必须为 http:// 或 https:// 开头的 URL")
			}
		}
		for _, key := range []string{"homepage", "documentation", "issues"} {
			value, present := manifestOptionalString(urls, key, "urls."+key, errors)
			if !present {
				continue
			}
			if value == "" {
				if _, exists := urls[key]; exists {
					errors.add("urls."+key, "不能为空字符串")
				}
				continue
			}
			if !manifestHTTPURLPattern.MatchString(value) {
				errors.add("urls."+key, "必须为 http:// 或 https:// 开头的 URL")
			}
			switch key {
			case "homepage":
				manifest.URLs.Homepage = value
			case "documentation":
				manifest.URLs.Documentation = value
			case "issues":
				manifest.URLs.Issues = value
			}
		}
	}

	manifest.HostApplication = parseManifestVersionRange(raw, "host_application", "host_application", true, errors)
	manifest.SDK = parseManifestVersionRange(raw, "sdk", "sdk", true, errors)

	if value, exists := raw["plugin_type"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			errors.typeError("plugin_type")
		} else {
			manifest.PluginType = strings.TrimSpace(text)
		}
	} else if value, exists := raw["type"]; exists && value != nil {
		text, ok := value.(string)
		if !ok {
			errors.typeError("type")
		} else {
			manifest.PluginType = strings.TrimSpace(text)
		}
	}

	manifest.PluginDependencies, manifest.PythonPackageDependencies = parseManifestDependencies(raw["dependencies"], manifest.ID, errors)
	manifest.Capabilities = parseManifestCapabilities(raw, errors)
	manifest.LLMProviders = parseManifestLLMProviders(raw["llm_providers"], errors)
	manifest.I18n = parseManifestI18n(raw, errors)
	manifest.Display = parseManifestDisplay(raw["display"], errors)
	manifest.Changelog = parseManifestChangelog(raw["changelog"], errors)

	if !errors.empty() {
		return nil, errors.items
	}
	return manifest, nil
}

func manifestInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	}
	return 0, false
}

func parseManifestVersionRange(raw map[string]any, key, location string, required bool, errors *manifestErrors) PluginManifestVersionRange {
	object, ok := manifestObject(raw, key, location, required, errors)
	if !ok {
		return PluginManifestVersionRange{}
	}
	rejectManifestKeys(object, manifestRangeKeys, location, errors)
	result := PluginManifestVersionRange{}
	result.MinVersion, _ = manifestString(object, "min_version", location+".min_version", true, errors)
	result.MaxVersion, _ = manifestString(object, "max_version", location+".max_version", true, errors)
	for field, value := range map[string]string{"min_version": result.MinVersion, "max_version": result.MaxVersion} {
		if value == "" {
			continue
		}
		if !isValidManifestSemver(value) {
			errors.add(location+"."+field, "必须为严格三段式版本号，例如 1.0.0")
		}
	}
	if result.MinVersion != "" && result.MaxVersion != "" && compareManifestVersions(result.MinVersion, result.MaxVersion) > 0 {
		errors.add(location+".min_version", "不能大于 max_version")
	}
	return result
}

func parseManifestDependencies(value any, manifestID string, errors *manifestErrors) ([]PluginManifestDependency, []PythonPackageDependency) {
	pluginDependencies := []PluginManifestDependency{}
	pythonDependencies := []PythonPackageDependency{}
	if value == nil {
		return pluginDependencies, pythonDependencies
	}
	list, ok := value.([]any)
	if !ok {
		errors.typeError("dependencies")
		return pluginDependencies, pythonDependencies
	}
	seenPlugins := map[string]struct{}{}
	seenPackages := map[string]struct{}{}
	for index, item := range list {
		location := fmt.Sprintf("dependencies.%d", index)
		dependency, ok := item.(map[string]any)
		if !ok {
			errors.typeError(location)
			continue
		}
		typeText, present := manifestString(dependency, "type", location+".type", true, errors)
		if !present {
			continue
		}
		switch typeText {
		case "plugin":
			rejectManifestKeys(dependency, manifestPluginDependencyKeys, location, errors)
			id, present := manifestString(dependency, "id", location+".id", true, errors)
			if !present {
				continue
			}
			if !isSafePythonPluginID(id) {
				errors.add(location+".id", "必须使用字母/数字/下划线，并以点号或横线分隔")
			}
			if manifestID != "" && id == manifestID {
				errors.add(location+".id", "dependencies 中的插件依赖不能依赖自身")
			}
			if _, exists := seenPlugins[id]; exists {
				errors.add(location+".id", "存在重复的插件依赖声明: "+id)
			}
			seenPlugins[id] = struct{}{}
			spec, present := manifestString(dependency, "version_spec", location+".version_spec", true, errors)
			if present {
				if _, valid := ParsePEP440SpecifierSet(spec); !valid {
					errors.add(location+".version_spec", "无效的版本约束: "+spec)
					continue
				}
			}
			pluginDependencies = append(pluginDependencies, PluginManifestDependency{ID: id, VersionSpec: spec})
		case "python_package":
			rejectManifestKeys(dependency, manifestPythonDependencyKeys, location, errors)
			name, present := manifestString(dependency, "name", location+".name", true, errors)
			if !present {
				continue
			}
			if !pep440PackageNamePattern.MatchString(name) {
				errors.add(location+".name", "包名只能包含字母、数字、点号、下划线和横线")
			}
			normalized := CanonicalizePythonPackageName(name)
			if _, exists := seenPackages[normalized]; exists {
				errors.add(location+".name", "存在重复的 Python 包依赖声明: "+name)
			}
			seenPackages[normalized] = struct{}{}
			spec, present := manifestString(dependency, "version_spec", location+".version_spec", true, errors)
			if present {
				if _, valid := ParsePEP440SpecifierSet(spec); !valid {
					errors.add(location+".version_spec", "无效的版本约束: "+spec)
					continue
				}
			}
			pythonDependencies = append(pythonDependencies, PythonPackageDependency{Type: "python_package", PackageName: name, VersionSpec: spec})
		default:
			errors.literal(location+".type", "plugin", "python_package")
		}
	}
	return pluginDependencies, pythonDependencies
}

func parseManifestCapabilities(raw map[string]any, errors *manifestErrors) []string {
	value, exists := raw["capabilities"]
	if !exists || value == nil {
		errors.missing("capabilities")
		return []string{}
	}
	list, ok := value.([]any)
	if !ok {
		errors.typeError("capabilities")
		return []string{}
	}
	capabilities := make([]string, 0, len(list))
	seen := map[string]struct{}{}
	for index, item := range list {
		text, ok := item.(string)
		if !ok {
			errors.typeError(fmt.Sprintf("capabilities.%d", index))
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			errors.add("capabilities", "存在空能力名")
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		capabilities = append(capabilities, text)
	}
	return capabilities
}

func parseManifestLLMProviders(value any, errors *manifestErrors) []PluginManifestLLMProvider {
	providers := []PluginManifestLLMProvider{}
	if value == nil {
		return providers
	}
	list, ok := value.([]any)
	if !ok {
		errors.typeError("llm_providers")
		return providers
	}
	seen := map[string]struct{}{}
	for index, item := range list {
		location := fmt.Sprintf("llm_providers.%d", index)
		raw, ok := item.(map[string]any)
		if !ok {
			errors.typeError(location)
			continue
		}
		rejectManifestKeys(raw, manifestLLMProviderKeys, location, errors)
		clientType, present := manifestString(raw, "client_type", location+".client_type", true, errors)
		if !present {
			continue
		}
		if _, exists := seen[clientType]; exists {
			errors.add(location+".client_type", "存在重复的 LLM Provider 声明: "+clientType)
		}
		seen[clientType] = struct{}{}
		provider := PluginManifestLLMProvider{ClientType: clientType, Version: "1.0.0"}
		provider.Name, _ = manifestOptionalString(raw, "name", location+".name", errors)
		provider.Description, _ = manifestOptionalString(raw, "description", location+".description", errors)
		if version, present := manifestOptionalString(raw, "version", location+".version", errors); present && version != "" {
			provider.Version = version
		}
		providers = append(providers, provider)
	}
	return providers
}

func parseManifestI18n(raw map[string]any, errors *manifestErrors) PluginManifestI18n {
	i18n := PluginManifestI18n{SupportedLocales: []string{}}
	object, ok := manifestObject(raw, "i18n", "i18n", true, errors)
	if !ok {
		return i18n
	}
	rejectManifestKeys(object, manifestI18nKeys, "i18n", errors)
	i18n.DefaultLocale, _ = manifestString(object, "default_locale", "i18n.default_locale", true, errors)
	if path, present := manifestOptionalString(object, "locales_path", "i18n.locales_path", errors); present {
		if _, exists := object["locales_path"]; exists && path == "" {
			errors.add("i18n.locales_path", "不能为空字符串")
		}
		i18n.LocalesPath = path
	}
	if value, exists := object["supported_locales"]; exists && value != nil {
		list, ok := value.([]any)
		if !ok {
			errors.typeError("i18n.supported_locales")
		} else {
			for index, item := range list {
				locale, ok := item.(string)
				if !ok {
					errors.typeError(fmt.Sprintf("i18n.supported_locales.%d", index))
					continue
				}
				locale = strings.TrimSpace(locale)
				if locale == "" {
					errors.add("i18n.supported_locales", "语言列表中存在空值")
					continue
				}
				duplicated := false
				for _, existing := range i18n.SupportedLocales {
					if existing == locale {
						duplicated = true
						break
					}
				}
				if !duplicated {
					i18n.SupportedLocales = append(i18n.SupportedLocales, locale)
				}
			}
		}
	}
	if len(i18n.SupportedLocales) > 0 && i18n.DefaultLocale != "" {
		found := false
		for _, locale := range i18n.SupportedLocales {
			if locale == i18n.DefaultLocale {
				found = true
				break
			}
		}
		if !found {
			errors.add("i18n.default_locale", "default_locale 必须包含在 supported_locales 中")
		}
	}
	return i18n
}

func parseManifestDisplay(value any, errors *manifestErrors) *PluginManifestDisplay {
	if value == nil {
		return nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		errors.typeError("display")
		return nil
	}
	rejectManifestKeys(raw, manifestDisplayKeys, "display", errors)
	display := &PluginManifestDisplay{}
	iconValue, exists := raw["icon"]
	if !exists || iconValue == nil {
		return display
	}
	iconRaw, ok := iconValue.(map[string]any)
	if !ok {
		errors.typeError("display.icon")
		return display
	}
	rejectManifestKeys(iconRaw, manifestDisplayIconKeys, "display.icon", errors)
	icon := &PluginManifestDisplayIcon{}
	icon.Type, _ = manifestString(iconRaw, "type", "display.icon.type", true, errors)
	icon.Value, _ = manifestString(iconRaw, "value", "display.icon.value", true, errors)
	icon.Fallback, _ = manifestOptionalString(iconRaw, "fallback", "display.icon.fallback", errors)
	icon.Background, _ = manifestOptionalString(iconRaw, "background", "display.icon.background", errors)

	switch icon.Type {
	case "lucide":
		if icon.Value != "" && !manifestIconNamePattern.MatchString(icon.Value) {
			errors.add("display.icon.value", "lucide 图标名只能包含字母、数字、下划线和横线")
		}
	case "emoji":
	case "local":
		if err := validateManifestLocalIconPath(icon.Value); err != nil && icon.Value != "" {
			errors.add("display.icon.value", err.Error())
		}
	default:
		errors.literal("display.icon.type", "lucide", "emoji", "local")
	}
	if _, exists := iconRaw["fallback"]; exists && icon.Fallback != "" && !manifestIconNamePattern.MatchString(icon.Fallback) {
		errors.add("display.icon.fallback", "fallback 只能包含字母、数字、下划线和横线")
	} else if _, exists := iconRaw["fallback"]; exists && icon.Fallback == "" {
		errors.add("display.icon.fallback", "fallback 不能为空")
	}
	if _, exists := iconRaw["background"]; exists && !manifestHexColorPattern.MatchString(icon.Background) {
		errors.add("display.icon.background", "background 必须为 #RRGGBB 格式")
	}
	display.Icon = icon
	return display
}

func validateManifestLocalIconPath(iconPath string) error {
	if iconPath == "" {
		return fmt.Errorf("图标值不能为空")
	}
	if strings.HasPrefix(iconPath, "/") || strings.HasPrefix(iconPath, "\\") {
		return fmt.Errorf("local 图标路径必须是插件目录内的相对路径")
	}
	if strings.Contains(iconPath, "\x00") {
		return fmt.Errorf("local 图标路径包含非法字符")
	}
	for _, part := range strings.FieldsFunc(iconPath, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("local 图标路径必须是插件目录内的相对路径")
		}
	}
	if filepath.IsAbs(iconPath) {
		return fmt.Errorf("local 图标路径必须是插件目录内的相对路径")
	}
	suffix := strings.ToLower(filepath.Ext(iconPath))
	if _, ok := manifestLocalIconSuffixes[suffix]; !ok {
		return fmt.Errorf("local 图标仅支持 jpg、jpeg、png、svg、webp")
	}
	return nil
}

func parseManifestChangelog(value any, errors *manifestErrors) string {
	if value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		errors.typeError("changelog")
		return ""
	}
	text = strings.TrimSpace(text)
	if text == "" {
		errors.add("changelog", "不能为空字符串")
		return ""
	}
	if manifestHTTPURLPattern.MatchString(text) {
		return text
	}
	if strings.HasPrefix(text, "/") || strings.HasPrefix(text, "\\") || strings.Contains(text, "\x00") {
		errors.add("changelog", "必须为插件目录内的相对路径或 http(s) URL")
		return text
	}
	for _, part := range strings.Split(strings.ReplaceAll(text, "\\", "/"), "/") {
		if part == ".." {
			errors.add("changelog", "必须为插件目录内的相对路径或 http(s) URL")
			return text
		}
	}
	if !strings.HasSuffix(strings.ToLower(text), ".md") {
		errors.add("changelog", "插件内更新日志路径必须指向 Markdown 文件")
	}
	return text
}
