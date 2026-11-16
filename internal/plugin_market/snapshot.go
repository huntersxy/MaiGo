package pluginmarket

import (
	"context"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// orderedRecords 保留插件顺序，对齐 Python dict 的插入顺序
type orderedRecords struct {
	order  []string
	values map[string]map[string]any
}

func newOrderedRecords() *orderedRecords {
	return &orderedRecords{values: map[string]map[string]any{}}
}

func (r *orderedRecords) set(key string, value map[string]any) {
	if _, ok := r.values[key]; !ok {
		r.order = append(r.order, key)
	}
	r.values[key] = value
}

func (r *orderedRecords) get(key string) (map[string]any, bool) {
	value, ok := r.values[key]
	return value, ok
}

func (r *orderedRecords) list() []map[string]any {
	out := make([]map[string]any, 0, len(r.order))
	for _, key := range r.order {
		out = append(out, r.values[key])
	}
	return out
}

// loadSnapshot 对齐 service._snapshot：所有信息源都从 release index 与清单文件读取
func (s *Service) loadSnapshot(ctx context.Context, forceRefresh bool) (*CatalogSnapshot, error) {
	source, err := s.informationSource()
	if err != nil {
		return nil, err
	}
	var (
		details InformationFile
		index   *PluginReleaseIndex
		detErr  error
		idxErr  error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		details, detErr = s.fetchInformationFile(ctx, "plugin_details.json", source, forceRefresh)
	}()
	go func() {
		defer wg.Done()
		index, idxErr = s.loadReleaseIndexWithSource(ctx, source, forceRefresh)
	}()
	wg.Wait()
	if detErr != nil {
		return nil, detErr
	}
	if idxErr != nil {
		return nil, idxErr
	}
	rawList, ok := details.Data.(string)
	if !ok {
		return nil, &MarketError{StatusCode: 502, Message: "插件市场列表响应格式不是列表"}
	}
	var parsed any
	if err := sonic.UnmarshalString(rawList, &parsed); err != nil {
		return nil, err
	}
	list, ok := anyList(parsed)
	if !ok {
		return nil, &MarketError{StatusCode: 502, Message: "插件市场列表响应格式不是列表"}
	}
	items := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		item, ok := anyMap(raw)
		if !ok {
			zap.L().Warn("跳过插件市场列表中的非对象条目")
			continue
		}
		items = append(items, item)
	}
	plugins, err := s.buildCatalog(ctx, items, index)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{
		"source":     source,
		"source_url": details.URL,
		"cache":      details.Cache,
		"features":   MarketFeatures{Stats: true, Releases: true},
	}
	return &CatalogSnapshot{Meta: meta, Plugins: plugins}, nil
}

// loadReleaseIndexWithSource 对齐 _git_releases
func (s *Service) loadReleaseIndexWithSource(ctx context.Context, source string, forceRefresh bool) (*PluginReleaseIndex, error) {
	response, err := s.fetchInformationFile(ctx, "plugin_versions.json", source, forceRefresh)
	if err != nil {
		return nil, err
	}
	content, ok := response.Data.(string)
	if !ok {
		return nil, &MarketError{StatusCode: 502, Message: "版本索引响应格式无效"}
	}
	return ParseReleaseIndex(content)
}

// hasUsableManifest 对齐 _catalog 中 record["manifest"] 与 manifest_version 的判断
func hasUsableManifest(record map[string]any) bool {
	manifest, ok := anyMap(record["manifest"])
	if !ok || len(manifest) == 0 {
		return false
	}
	return safeInt(manifest["manifest_version"], 1) >= 2
}

// buildCatalog 对齐 service._catalog：合并索引、规范化条目并解析图标
func (s *Service) buildCatalog(ctx context.Context, items []map[string]any, index *PluginReleaseIndex) ([]PluginCatalogItem, error) {
	iconSource, err := s.downloadSource()
	if err != nil {
		return nil, err
	}
	records := newOrderedRecords()
	for _, item := range items {
		records.set(safeString(item["id"]), item)
	}
	entries := map[string]*PluginReleaseEntry{}
	if index != nil {
		for i := range index.Plugins {
			entries[index.Plugins[i].ID] = &index.Plugins[i]
		}
		// Tag 中的有效清单也必须保留，不能因分支清单失效而丢失发布版本入口
		for i := range index.Plugins {
			entry := &index.Plugins[i]
			record, exists := records.get(entry.ID)
			if exists && hasUsableManifest(record) {
				continue
			}
			description := describeEntry(entry)
			var release *PluginRelease
			if description.RecommendedVersion != nil {
				release = findReleaseVersion(entry, *description.RecommendedVersion)
			}
			if release == nil && len(entry.Versions) > 0 {
				release = maxReleaseVersion(entry.Versions)
			}
			if release == nil {
				continue
			}
			base := map[string]any{}
			if exists {
				base = record
			}
			merged := mergeAnyMap(base, "id", entry.ID)
			merged["manifest"] = release.Manifest
			records.set(entry.ID, merged)
		}
		items = records.list()
	}

	plugins := make([]PluginCatalogItem, 0, len(items))
	for _, item := range items {
		entry := entries[safeString(item["id"])]
		raw := item
		if entry != nil {
			description := describeEntry(entry)
			if description.RecommendedVersion != nil {
				if release := findReleaseVersion(entry, *description.RecommendedVersion); release != nil {
					raw = mergeAnyMap(raw, "manifest", release.Manifest)
				}
			}
		}
		plugin, ok := normalizeCatalogItem(raw, true)
		if !ok {
			continue
		}
		// 统计由官方统计服务单独读取，见 Search()
		plugin.Stats = nil
		if entry == nil {
			// manifest.repository_url 与 urls.repository 双地址回退，并兼容清单条目自带地址
			repositoryURL := plugin.RepositoryURL
			if repositoryURL == "" {
				repositoryURL = safeString(item["repository_url"])
			}
			if !pluginRepositoryPattern.MatchString(repositoryURL) {
				zap.L().Warn("跳过无法构造回退发布信息的插件市场条目",
					zap.String("plugin_id", plugin.ID), zap.String("repository_url", repositoryURL))
				continue
			}
			entry = &PluginReleaseEntry{
				ID:            plugin.MarketplaceID,
				ManifestID:    &plugin.ID,
				RepositoryURL: repositoryURL,
				Mode:          "releases",
				Versions:      []PluginRelease{},
				SyncError:     stringPointer("插件尚未收录到版本索引，请先同步插件中心"),
			}
		}
		plugin.Releases = releaseDescription(entry, plugin.Manifest)
		icon := ""
		if assets, ok := anyMap(item["assets"]); ok {
			icon = safeString(assets["icon_64"])
		}
		if icon != "" {
			resolved, err := s.iconURL(icon, iconSource)
			if err != nil {
				return nil, err
			}
			plugin.Assets = map[string]string{"icon_64": resolved}
		}
		plugin.Changelog = optionalString(item["changelog"])
		plugin.PublishedAt = optionalString(item["published_at"])
		plugin.UpdatedAt = optionalString(item["updated_at"])
		plugins = append(plugins, plugin)
	}
	return plugins, nil
}

func stringPointer(value string) *string {
	return &value
}

func optionalString(value any) *string {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

// isCompatible 对齐 _is_compatible：分支安装看兼容标记，发布安装看推荐版本
func isCompatible(plugin PluginCatalogItem) bool {
	mode := safeString(plugin.Releases["mode"])
	if mode == "branch" {
		compatible, ok := plugin.Releases["branch_compatible"].(bool)
		return ok && compatible
	}
	return safeString(plugin.Releases["recommended_version"]) != ""
}

// filterCompatible 过滤不兼容插件
func filterCompatible(plugins []PluginCatalogItem) []PluginCatalogItem {
	out := make([]PluginCatalogItem, 0, len(plugins))
	for _, plugin := range plugins {
		if isCompatible(plugin) {
			out = append(out, plugin)
		}
	}
	return out
}

// pluginsFromSnapshot 提取插件列表
func pluginsFromSnapshot(snapshot *CatalogSnapshot) []PluginCatalogItem {
	if snapshot == nil {
		return []PluginCatalogItem{}
	}
	return snapshot.Plugins
}

// catalogSource 读取 metadata 中的信息源
func catalogSource(metadata map[string]any) string {
	return strings.ToLower(safeString(metadata["source"]))
}
