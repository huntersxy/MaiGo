package pluginruntime

// PluginManifestAuthor 是 Manifest 中的作者信息
type PluginManifestAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PluginManifestURLs 是 Manifest 中声明的相关链接
type PluginManifestURLs struct {
	Repository    string `json:"repository"`
	Homepage      string `json:"homepage"`
	Documentation string `json:"documentation"`
	Issues        string `json:"issues"`
}

// PluginManifestVersionRange 是闭区间版本声明
type PluginManifestVersionRange struct {
	MinVersion string `json:"min_version"`
	MaxVersion string `json:"max_version"`
}

// PluginManifestI18n 是插件国际化配置
type PluginManifestI18n struct {
	DefaultLocale    string   `json:"default_locale"`
	LocalesPath      string   `json:"locales_path,omitempty"`
	SupportedLocales []string `json:"supported_locales"`
}

// PluginManifestDependency 是插件之间的依赖声明
type PluginManifestDependency struct {
	ID          string `json:"id"`
	VersionSpec string `json:"version_spec"`
}

// PluginManifestLLMProvider 是 Manifest 中静态声明的 LLM Provider
type PluginManifestLLMProvider struct {
	ClientType  string `json:"client_type"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
}

// PluginManifestDisplayIcon 是插件展示图标声明
type PluginManifestDisplayIcon struct {
	Type       string `json:"type"`
	Value      string `json:"value"`
	Fallback   string `json:"fallback,omitempty"`
	Background string `json:"background,omitempty"`
}

// PluginManifestDisplay 是插件展示元信息
type PluginManifestDisplay struct {
	Icon *PluginManifestDisplayIcon `json:"icon,omitempty"`
}

// PluginManifest 是 Manifest v2 的强类型模型
type PluginManifest struct {
	ManifestVersion           int                         `json:"manifest_version"`
	Version                   string                      `json:"version"`
	Name                      string                      `json:"name"`
	Description               string                      `json:"description"`
	Author                    PluginManifestAuthor        `json:"author"`
	License                   string                      `json:"license"`
	URLs                      PluginManifestURLs          `json:"urls"`
	HostApplication           PluginManifestVersionRange  `json:"host_application"`
	SDK                       PluginManifestVersionRange  `json:"sdk"`
	PluginDependencies        []PluginManifestDependency  `json:"plugin_dependencies"`
	PythonPackageDependencies []PythonPackageDependency   `json:"python_package_dependencies"`
	LLMProviders              []PluginManifestLLMProvider `json:"llm_providers"`
	Capabilities              []string                    `json:"capabilities"`
	I18n                      PluginManifestI18n          `json:"i18n"`
	ID                        string                      `json:"id"`
	PluginType                string                      `json:"plugin_type"`
	Display                   *PluginManifestDisplay      `json:"display,omitempty"`
	Changelog                 string                      `json:"changelog,omitempty"`
}

// PluginDependencyIDs 返回 Manifest 声明的插件依赖 ID 列表
func (manifest *PluginManifest) PluginDependencyIDs() []string {
	if manifest == nil {
		return nil
	}
	ids := make([]string, 0, len(manifest.PluginDependencies))
	for _, dependency := range manifest.PluginDependencies {
		ids = append(ids, dependency.ID)
	}
	return ids
}

// ManifestValidation 是一次 Manifest 校验的完整结果
type ManifestValidation struct {
	Source   string
	Manifest *PluginManifest
	Errors   []string
	Warnings []string
}

// OK 表示 Manifest 已通过全部校验
func (validation ManifestValidation) OK() bool {
	return validation.Manifest != nil && len(validation.Errors) == 0
}

// ManifestRecord 是目录扫描命中的一个插件 Manifest
type ManifestRecord struct {
	Path       string
	Validation ManifestValidation
}

// Manifest 返回解析成功的 Manifest，失败时返回 nil
func (record ManifestRecord) Manifest() *PluginManifest {
	return record.Validation.Manifest
}
