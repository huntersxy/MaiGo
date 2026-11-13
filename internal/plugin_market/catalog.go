package pluginmarket

import (
	"fmt"
	"golang.org/x/text/cases"
	"net/url"
	"sort"
	"strings"
)

const (
	minPluginManifestVersion = 2
	// pluginStatsSorts 对齐 PLUGIN_STATS_SORTS 的顺序
	pluginStatsSortsDescription = "downloads, likes, rating, downloads_growth_7d, likes_growth_7d, downloads_growth_30d, likes_growth_30d"
)

// PluginAuthor 对齐 dashboard 展示的作者信息
type PluginAuthor struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PluginCatalogItem 对齐 catalog._normalize_plugin_catalog_item 的完整字段
type PluginCatalogItem struct {
	ID               string            `json:"id"`
	MarketplaceID    string            `json:"marketplace_id"`
	ManifestVersion  int               `json:"manifest_version"`
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Description      string            `json:"description"`
	Author           PluginAuthor      `json:"author"`
	License          string            `json:"license"`
	HostApplication  map[string]any    `json:"host_application"`
	SDK              map[string]any    `json:"sdk"`
	RepositoryURL    string            `json:"repository_url"`
	HomepageURL      string            `json:"homepage_url"`
	DocumentationURL string            `json:"documentation_url"`
	IssuesURL        string            `json:"issues_url"`
	Keywords         []string          `json:"keywords"`
	Categories       []string          `json:"categories"`
	Capabilities     []string          `json:"capabilities"`
	Dependencies     []any             `json:"dependencies"`
	DefaultLocale    string            `json:"default_locale"`
	LocalesPath      string            `json:"locales_path"`
	Source           string            `json:"source"`
	Manifest         map[string]any    `json:"manifest,omitempty"`
	Stats            map[string]any    `json:"stats"`
	Releases         map[string]any    `json:"releases"`
	Assets           map[string]string `json:"assets"`
	Changelog        *string           `json:"changelog"`
	PublishedAt      *string           `json:"published_at"`
	UpdatedAt        *string           `json:"updated_at"`
}

// normalizeCatalogItem 对齐 catalog._normalize_plugin_catalog_item
func normalizeCatalogItem(item map[string]any, includeManifest bool) (PluginCatalogItem, bool) {
	manifest, ok := anyMap(item["manifest"])
	if !ok {
		return PluginCatalogItem{}, false
	}
	manifestVersion := safeInt(manifest["manifest_version"], 0)
	if manifestVersion < minPluginManifestVersion {
		return PluginCatalogItem{}, false
	}
	manifestID := safeString(manifest["id"])
	marketplaceID := safeString(item["id"])
	pluginID := manifestID
	if pluginID == "" {
		pluginID = marketplaceID
	}
	name := safeString(manifest["name"])
	version := safeString(manifest["version"])
	if pluginID == "" || name == "" || version == "" {
		return PluginCatalogItem{}, false
	}
	author := map[string]any{}
	if rawAuthor, ok := anyMap(manifest["author"]); ok {
		author = rawAuthor
	}
	hostApplication := map[string]any{}
	if rawHost, ok := anyMap(manifest["host_application"]); ok {
		hostApplication = rawHost
	}
	sdk := map[string]any{}
	if rawSDK, ok := anyMap(manifest["sdk"]); ok {
		sdk = rawSDK
	}
	dependencies := []any{}
	if rawDeps, ok := anyList(manifest["dependencies"]); ok {
		dependencies = rawDeps
	}
	i18n := map[string]any{}
	if rawI18n, ok := anyMap(manifest["i18n"]); ok {
		i18n = rawI18n
	}
	authorName := safeString(author["name"])
	if authorName == "" {
		authorName = "Unknown"
	}
	license := safeString(manifest["license"])
	if license == "" {
		license = "Unknown"
	}
	defaultLocale := safeString(i18n["default_locale"])
	if defaultLocale == "" {
		defaultLocale = "zh-CN"
	}

	out := PluginCatalogItem{
		ID:               pluginID,
		MarketplaceID:    marketplaceID,
		ManifestVersion:  manifestVersion,
		Name:             name,
		Version:          version,
		Description:      safeString(manifest["description"]),
		Author:           PluginAuthor{Name: authorName, URL: safeString(author["url"])},
		License:          license,
		HostApplication:  hostApplication,
		SDK:              sdk,
		RepositoryURL:    manifestRepositoryURL(manifest),
		HomepageURL:      manifestHomepageURL(manifest),
		DocumentationURL: manifestURL(manifest, "documentation"),
		IssuesURL:        manifestURL(manifest, "issues"),
		Keywords:         stringList(manifest["keywords"]),
		Categories:       stringList(manifest["categories"]),
		Capabilities:     stringList(manifest["capabilities"]),
		Dependencies:     dependencies,
		DefaultLocale:    defaultLocale,
		LocalesPath:      safeString(i18n["locales_path"]),
		Source:           "market",
	}
	if includeManifest {
		out.Manifest = manifest
	}
	return out, true
}

// manifestRepositoryURL 对齐 _plugin_manifest_repository_url
func manifestRepositoryURL(manifest map[string]any) string {
	urls, _ := anyMap(manifest["urls"])
	fromURLs := ""
	if urls != nil {
		fromURLs = safeString(urls["repository"])
	}
	if repository := safeString(manifest["repository_url"]); repository != "" {
		return repository
	}
	return fromURLs
}

// manifestHomepageURL 对齐 _plugin_manifest_homepage_url
func manifestHomepageURL(manifest map[string]any) string {
	urls, _ := anyMap(manifest["urls"])
	fromURLs := ""
	if urls != nil {
		fromURLs = safeString(urls["homepage"])
	}
	if homepage := safeString(manifest["homepage_url"]); homepage != "" {
		return homepage
	}
	return fromURLs
}

// manifestURL 对齐 _plugin_manifest_url
func manifestURL(manifest map[string]any, key string) string {
	urls, _ := anyMap(manifest["urls"])
	if urls == nil {
		return ""
	}
	return safeString(urls[key])
}

// pluginSearchText 对齐 _plugin_search_text
func pluginSearchText(plugin PluginCatalogItem) string {
	parts := []string{
		plugin.ID,
		plugin.MarketplaceID,
		plugin.Name,
		plugin.Description,
		plugin.RepositoryURL,
		plugin.HomepageURL,
		plugin.Author.Name,
	}
	parts = append(parts, plugin.Keywords...)
	parts = append(parts, plugin.Categories...)
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			filtered = append(filtered, part)
		}
	}
	return cases.Fold().String(strings.Join(filtered, " "))
}

// filterPlugins 对齐 catalog._filter_plugins：关键词全命中且分类为子串匹配
func filterPlugins(plugins []PluginCatalogItem, query, category string) []PluginCatalogItem {
	terms := []string{}
	for _, term := range strings.Fields(strings.TrimSpace(query)) {
		terms = append(terms, cases.Fold().String(term))
	}
	normalizedCategory := cases.Fold().String(strings.TrimSpace(category))
	filtered := make([]PluginCatalogItem, 0, len(plugins))
	for _, plugin := range plugins {
		if normalizedCategory != "" {
			matched := false
			for _, item := range plugin.Categories {
				if strings.Contains(cases.Fold().String(item), normalizedCategory) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if len(terms) > 0 {
			searchText := pluginSearchText(plugin)
			matched := true
			for _, term := range terms {
				if !strings.Contains(searchText, term) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		filtered = append(filtered, plugin)
	}
	return filtered
}

// isStatSort 判断排序键是否属于统计排序
func isStatSort(sortBy MarketSort) bool {
	switch sortBy {
	case SortDownloads, SortLikes, SortRating, SortDownloadsGrowth7, SortLikesGrowth7, SortDownloadsGrowth30, SortLikesGrowth30:
		return true
	}
	return false
}

// sortStatPlugins 对齐 catalog._sort_stat_plugins：缺失指标不参与排序
func sortStatPlugins(plugins []PluginCatalogItem, sortBy MarketSort) ([]PluginCatalogItem, error) {
	if !isStatSort(sortBy) {
		return nil, fmt.Errorf("sort_by 必须是以下值之一：%s", pluginStatsSortsDescription)
	}
	field := string(sortBy)
	growth := strings.Contains(field, "_growth_")
	if growth {
		field = strings.ReplaceAll(field, "_growth_", "_growth_rank_")
	}
	if growth && len(plugins) > 0 {
		available := false
		for _, plugin := range plugins {
			if _, ok := plugin.Stats[field]; ok {
				available = true
				break
			}
		}
		if !available {
			return nil, fmt.Errorf("统计接口未提供 %s，无法查询该飙升榜", field)
		}
	}
	candidates := make([]PluginCatalogItem, 0, len(plugins))
	for _, plugin := range plugins {
		value, ok := statValue(plugin.Stats, field)
		if !ok {
			continue
		}
		if growth && value <= 0 {
			continue
		}
		candidates = append(candidates, plugin)
	}
	valueOf := func(plugin PluginCatalogItem) float64 {
		value, _ := statValue(plugin.Stats, field)
		return value
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := valueOf(candidates[i]), valueOf(candidates[j])
		if left != right {
			if growth {
				return left < right
			}
			return left > right
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates, nil
}

// statValue 读取统计字段数值，布尔值不参与排序
func statValue(stats map[string]any, field string) (float64, bool) {
	if stats == nil {
		return 0, false
	}
	switch value := stats[field].(type) {
	case bool:
		return 0, false
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	}
	return 0, false
}

// iconURL 对齐 service._icon_url：市场图标跟随下载源
func (s *Service) iconURL(icon string, target GitSource) (string, error) {
	trimmed := strings.TrimSpace(icon)
	parsed, err := parseURL(trimmed)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" && parsed.Host == "" {
		return target.RawURL("Mai-with-u", "plugin-repo", "main", trimmed)
	}
	prefixes := map[string]string{
		"raw.githubusercontent.com": "/Mai-with-u/plugin-repo/",
		"cnb.cool":                  "/Mai-with-u/plugin-repo/-/git/raw/",
	}
	prefix, ok := prefixes[cases.Fold().String(parsed.Hostname())]
	if !ok || !strings.HasPrefix(cases.Fold().String(parsed.Path), cases.Fold().String(prefix)) {
		return trimmed, nil
	}
	rest := parsed.Path[len(prefix):]
	ref, path, found := strings.Cut(rest, "/")
	if ref == "" || !found || path == "" {
		return "", fmt.Errorf("插件市场图标 URL 缺少版本或文件路径：%s", trimmed)
	}
	resolvedRef := "main"
	if target.ID != "cnb" {
		resolvedRef = unescapePath(ref)
	}
	resolved, err := target.RawURL("Mai-with-u", "plugin-repo", resolvedRef, unescapePath(path))
	if err != nil {
		return "", err
	}
	// 对齐 urlsplit(resolved)._replace(query=..., fragment=...) 的行为
	if parsed.RawQuery != "" {
		resolved += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		resolved += "#" + parsed.Fragment
	}
	return resolved, nil
}

// parseURL 统一 URL 解析入口
func parseURL(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

// unescapePath 对齐 urllib.parse.unquote，失败时保留原值
func unescapePath(value string) string {
	unescaped, err := url.PathUnescape(value)
	if err != nil {
		return value
	}
	return unescaped
}
