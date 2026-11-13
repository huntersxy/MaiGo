package pluginmarket

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
)

var (
	releaseVersionPattern   = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$`)
	releaseCommitPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pluginRepositoryPattern = regexp.MustCompile(`^https://github\.com/[\w.-]+/[\w.-]+/?$`)
)

// PluginRelease 对齐 releases.PluginRelease
type PluginRelease struct {
	Version      string         `json:"version"`
	Tag          string         `json:"tag"`
	Commit       string         `json:"commit"`
	Prerelease   bool           `json:"prerelease"`
	Yanked       bool           `json:"yanked"`
	Manifest     map[string]any `json:"manifest"`
	PublishedAt  *string        `json:"published_at"`
	ReleaseURL   *string        `json:"release_url"`
	ReleaseNotes string         `json:"release_notes"`
}

// PluginReleaseEntry 对齐 releases.PluginReleaseEntry，字段名与 Python 模型一致
type PluginReleaseEntry struct {
	ID               string              `json:"id"`
	ManifestID       *string             `json:"manifest_id"`
	RepositoryURL    string              `json:"repositoryUrl"`
	Mode             string              `json:"mode"`
	Versions         []PluginRelease     `json:"versions"`
	SyncError        *string             `json:"sync_error"`
	RejectedReleases []map[string]string `json:"rejected_releases"`
}

// PluginReleaseIndex 对齐 releases.PluginReleaseIndex
type PluginReleaseIndex struct {
	SchemaVersion int                  `json:"schema_version"`
	Plugins       []PluginReleaseEntry `json:"plugins"`
}

// DescribedRelease 对齐 describe_entry 中的版本项
type DescribedRelease struct {
	Version      string         `json:"version"`
	Tag          string         `json:"tag"`
	Commit       string         `json:"commit"`
	Prerelease   bool           `json:"prerelease"`
	Yanked       bool           `json:"yanked"`
	Manifest     map[string]any `json:"manifest"`
	PublishedAt  *string        `json:"published_at"`
	ReleaseURL   *string        `json:"release_url"`
	ReleaseNotes string         `json:"release_notes"`
	Compatible   bool           `json:"compatible"`
	Reasons      []string       `json:"reasons"`
}

// EntryDescription 对齐 describe_entry 的返回结构
type EntryDescription struct {
	ID                 string              `json:"id"`
	ManifestID         *string             `json:"manifest_id"`
	RepositoryURL      string              `json:"repositoryUrl"`
	Mode               string              `json:"mode"`
	SyncError          *string             `json:"sync_error"`
	RejectedReleases   []map[string]string `json:"rejected_releases"`
	Versions           []DescribedRelease  `json:"versions"`
	RecommendedVersion *string             `json:"recommended_version"`
}

func rejectUnknownKeys(raw map[string]any, allowed []string, model string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range raw {
		if _, ok := allowedSet[key]; !ok {
			return fmt.Errorf("%s 存在未知字段: %s", model, key)
		}
	}
	return nil
}

// ParseReleaseIndex 对齐 parse_release_index：校验 schema_version、别名冲突与条目规则
func ParseReleaseIndex(content string) (*PluginReleaseIndex, error) {
	var raw struct {
		SchemaVersion any              `json:"schema_version"`
		Plugins       []map[string]any `json:"plugins"`
	}
	if err := sonic.UnmarshalString(content, &raw); err != nil {
		return nil, err
	}
	if safeInt(raw.SchemaVersion, -1) != 1 {
		return nil, fmt.Errorf("版本索引 schema_version 必须为 1")
	}
	index := &PluginReleaseIndex{SchemaVersion: 1, Plugins: make([]PluginReleaseEntry, 0, len(raw.Plugins))}
	aliases := map[string]string{}
	for _, item := range raw.Plugins {
		entry, err := parseReleaseEntry(item)
		if err != nil {
			return nil, err
		}
		for _, alias := range releaseAliases(*entry) {
			if entry.SyncError != nil {
				continue
			}
			key := strings.ToLower(alias)
			if existing, ok := aliases[key]; ok && existing != entry.ID {
				return nil, fmt.Errorf("版本索引插件 ID 冲突：%s", alias)
			}
			aliases[key] = entry.ID
		}
		if err := validateReleaseEntry(*entry); err != nil {
			return nil, err
		}
		index.Plugins = append(index.Plugins, *entry)
	}
	return index, nil
}

func releaseAliases(entry PluginReleaseEntry) []string {
	aliases := []string{entry.ID}
	if entry.ManifestID != nil && *entry.ManifestID != "" {
		aliases = append(aliases, *entry.ManifestID)
	}
	return aliases
}

// ParseMarketReleaseEntry 对齐 parse_market_release_entry：可由市场响应或索引条目构造
func ParseMarketReleaseEntry(data map[string]any, pluginID string) (*PluginReleaseEntry, error) {
	raw := map[string]any{
		"id":                data["marketplace_id"],
		"manifest_id":       data["id"],
		"repositoryUrl":     data["repository_url"],
		"mode":              data["install_mode"],
		"versions":          data["versions"],
		"sync_error":        data["sync_error"],
		"rejected_releases": data["rejected_releases"],
	}
	if raw["rejected_releases"] == nil {
		raw["rejected_releases"] = []any{}
	}
	entry, err := parseReleaseEntry(raw)
	if err != nil {
		return nil, err
	}
	if pluginID != "" {
		matched := strings.EqualFold(entry.ID, pluginID)
		if !matched && entry.ManifestID != nil && strings.EqualFold(*entry.ManifestID, pluginID) {
			matched = true
		}
		if !matched {
			return nil, fmt.Errorf("插件中心返回的插件 ID 与请求不一致")
		}
	}
	if err := validateReleaseEntry(*entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func parseReleaseEntry(raw map[string]any) (*PluginReleaseEntry, error) {
	// 对齐 Python PluginReleaseEntry(BaseModel)：未声明字段被忽略而非报错，
	// 这样市场侧新增字段不会让旧版客户端整份索引解析失败
	entry := &PluginReleaseEntry{
		ID:            strings.TrimSpace(safeString(raw["id"])),
		RepositoryURL: strings.TrimSpace(safeString(raw["repositoryUrl"])),
		Mode:          strings.TrimSpace(safeString(raw["mode"])),
		Versions:      []PluginRelease{},
	}
	if entry.ID == "" {
		return nil, fmt.Errorf("PluginReleaseEntry.id 不能为空")
	}
	if raw["manifest_id"] != nil {
		value := safeString(raw["manifest_id"])
		if value != "" {
			entry.ManifestID = &value
		}
	}
	if !pluginRepositoryPattern.MatchString(entry.RepositoryURL) {
		return nil, fmt.Errorf("PluginReleaseEntry.repositoryUrl 必须为 https://github.com/owner/repo 地址")
	}
	if entry.Mode != "releases" && entry.Mode != "branch" {
		return nil, fmt.Errorf("PluginReleaseEntry.mode 必须为 releases 或 branch")
	}
	if raw["sync_error"] != nil {
		value := strings.TrimSpace(safeString(raw["sync_error"]))
		if value != "" {
			entry.SyncError = &value
		}
	}
	versions, ok := anyList(raw["versions"])
	if !ok {
		return nil, fmt.Errorf("PluginReleaseEntry.versions 必须为列表")
	}
	for _, item := range versions {
		versionMap, ok := anyMap(item)
		if !ok {
			return nil, fmt.Errorf("PluginReleaseEntry.versions 中存在非对象条目")
		}
		release, err := parseRelease(versionMap)
		if err != nil {
			return nil, err
		}
		entry.Versions = append(entry.Versions, *release)
	}
	rejected, ok := anyList(raw["rejected_releases"])
	if !ok && raw["rejected_releases"] != nil {
		return nil, fmt.Errorf("PluginReleaseEntry.rejected_releases 必须为列表")
	}
	entry.RejectedReleases = make([]map[string]string, 0, len(rejected))
	for _, item := range rejected {
		rejectedMap, ok := anyMap(item)
		if !ok {
			return nil, fmt.Errorf("PluginReleaseEntry.rejected_releases 中存在非对象条目")
		}
		converted := make(map[string]string, len(rejectedMap))
		for key, value := range rejectedMap {
			converted[key] = safeString(value)
		}
		entry.RejectedReleases = append(entry.RejectedReleases, converted)
	}
	return entry, nil
}

func parseRelease(raw map[string]any) (*PluginRelease, error) {
	// 同上：未声明字段忽略，与 pydantic 默认行为一致
	release := &PluginRelease{
		Version: strings.TrimSpace(safeString(raw["version"])),
		Tag:     strings.TrimSpace(safeString(raw["tag"])),
		Commit:  strings.TrimSpace(safeString(raw["commit"])),
	}
	if !releaseVersionPattern.MatchString(release.Version) {
		return nil, fmt.Errorf("PluginRelease.version 必须为 X.Y.Z 形式")
	}
	if release.Tag == "" {
		return nil, fmt.Errorf("PluginRelease.tag 不能为空")
	}
	if !releaseCommitPattern.MatchString(release.Commit) {
		return nil, fmt.Errorf("PluginRelease.commit 必须为 40 位小写十六进制")
	}
	prerelease, ok := raw["prerelease"].(bool)
	if !ok {
		return nil, fmt.Errorf("PluginRelease.prerelease 必须为布尔值")
	}
	release.Prerelease = prerelease
	if raw["yanked"] != nil {
		yanked, ok := raw["yanked"].(bool)
		if !ok {
			return nil, fmt.Errorf("PluginRelease.yanked 必须为布尔值")
		}
		release.Yanked = yanked
	}
	manifest, ok := anyMap(raw["manifest"])
	if !ok {
		return nil, fmt.Errorf("PluginRelease.manifest 必须为对象")
	}
	release.Manifest = manifest
	if raw["published_at"] != nil {
		value, ok := raw["published_at"].(string)
		if !ok {
			return nil, fmt.Errorf("PluginRelease.published_at 必须为字符串")
		}
		release.PublishedAt = &value
	}
	if raw["release_url"] != nil {
		value, ok := raw["release_url"].(string)
		if !ok {
			return nil, fmt.Errorf("PluginRelease.release_url 必须为字符串")
		}
		release.ReleaseURL = &value
	}
	if raw["release_notes"] != nil {
		value, ok := raw["release_notes"].(string)
		if !ok {
			return nil, fmt.Errorf("PluginRelease.release_notes 必须为字符串")
		}
		release.ReleaseNotes = value
	}
	return release, nil
}

// validateReleaseEntry 对齐 _validate_release_entry
func validateReleaseEntry(entry PluginReleaseEntry) error {
	if entry.Mode == "branch" && (len(entry.Versions) > 0 || len(entry.RejectedReleases) > 0) {
		return fmt.Errorf("版本索引安装模式不匹配：%s", entry.ID)
	}
	seen := map[string]struct{}{}
	for _, release := range entry.Versions {
		if _, ok := seen[release.Version]; ok {
			return fmt.Errorf("版本索引版本重复或 manifest 不匹配：%s", entry.ID)
		}
		if safeString(release.Manifest["version"]) != release.Version {
			return fmt.Errorf("版本索引版本重复或 manifest 不匹配：%s", entry.ID)
		}
		if release.Tag != release.Version && release.Tag != "v"+release.Version {
			return fmt.Errorf("版本索引 Tag 不匹配：%s", entry.ID)
		}
		if manifestVersionEquals(release.Manifest, 2) {
			manifestID := safeString(release.Manifest["id"])
			if entry.ManifestID == nil || manifestID != *entry.ManifestID {
				return fmt.Errorf("版本索引 manifest ID 不匹配：%s", entry.ID)
			}
		}
		seen[release.Version] = struct{}{}
	}
	return nil
}

// manifestCompatibility 对齐 releases.manifest_compatibility：只返回校验错误
func manifestCompatibility(manifest map[string]any) []string {
	return ValidateManifest(manifest, DefaultMaibotVersion())
}

// releaseCompatibility 对齐 release_compatibility：清单错误与撤回标记共同决定可用性
func releaseCompatibility(release PluginRelease) []string {
	reasons := manifestCompatibility(release.Manifest)
	if release.Yanked {
		reasons = append(reasons, "该版本已撤回")
	}
	return reasons
}

// describeEntry 对齐 describe_entry：按版本倒序给出兼容性与推荐版本
func describeEntry(entry *PluginReleaseEntry) EntryDescription {
	versions := make([]PluginRelease, len(entry.Versions))
	copy(versions, entry.Versions)
	sort.SliceStable(versions, func(i, j int) bool {
		return compareReleaseVersions(versions[i].Version, versions[j].Version) > 0
	})
	described := make([]DescribedRelease, 0, len(versions))
	var recommended *string
	for _, release := range versions {
		reasons := releaseCompatibility(release)
		if entry.SyncError != nil {
			reasons = append(reasons, "版本同步失败："+*entry.SyncError)
		}
		if reasons == nil {
			reasons = []string{}
		}
		described = append(described, DescribedRelease{
			Version:      release.Version,
			Tag:          release.Tag,
			Commit:       release.Commit,
			Prerelease:   release.Prerelease,
			Yanked:       release.Yanked,
			Manifest:     release.Manifest,
			PublishedAt:  release.PublishedAt,
			ReleaseURL:   release.ReleaseURL,
			ReleaseNotes: release.ReleaseNotes,
			Compatible:   len(reasons) == 0,
			Reasons:      reasons,
		})
		if recommended == nil && len(reasons) == 0 && !release.Prerelease {
			version := release.Version
			recommended = &version
		}
	}
	return EntryDescription{
		ID:                 entry.ID,
		ManifestID:         entry.ManifestID,
		RepositoryURL:      entry.RepositoryURL,
		Mode:               entry.Mode,
		SyncError:          entry.SyncError,
		RejectedReleases:   entry.RejectedReleases,
		Versions:           described,
		RecommendedVersion: recommended,
	}
}

// releaseDescription 对齐 service._release_description：分支安装追加兼容原因
func releaseDescription(entry *PluginReleaseEntry, manifest map[string]any) map[string]any {
	description := describeEntry(entry)
	payload := entryDescriptionToMap(description)
	if entry.Mode == "branch" {
		reasons := manifestCompatibility(manifest)
		if entry.SyncError != nil {
			reasons = append(reasons, "版本同步失败："+*entry.SyncError)
		}
		if reasons == nil {
			reasons = []string{}
		}
		payload["branch_compatible"] = len(reasons) == 0
		payload["branch_reasons"] = reasons
	}
	return payload
}

// entryDescriptionToMap 把描述结构转回通用字典，便于按 Python 口径输出
func entryDescriptionToMap(description EntryDescription) map[string]any {
	payload := map[string]any{
		"id":                description.ID,
		"manifest_id":       description.ManifestID,
		"repositoryUrl":     description.RepositoryURL,
		"mode":              description.Mode,
		"sync_error":        description.SyncError,
		"rejected_releases": description.RejectedReleases,
		"versions":          description.Versions,
	}
	if description.RecommendedVersion != nil {
		payload["recommended_version"] = *description.RecommendedVersion
	} else {
		payload["recommended_version"] = nil
	}
	return payload
}

// findReleaseVersion 在条目中按版本号定位发布版本
func findReleaseVersion(entry *PluginReleaseEntry, version string) *PluginRelease {
	for i := range entry.Versions {
		if entry.Versions[i].Version == version {
			return &entry.Versions[i]
		}
	}
	return nil
}

// maxReleaseVersion 取最高版本，对齐 _catalog 中的 max(..., key=Version)
func maxReleaseVersion(versions []PluginRelease) *PluginRelease {
	if len(versions) == 0 {
		return nil
	}
	best := &versions[0]
	for i := range versions {
		if compareReleaseVersions(versions[i].Version, best.Version) > 0 {
			best = &versions[i]
		}
	}
	return best
}
