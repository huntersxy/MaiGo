package pluginmarket

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Service 插件市场服务，对齐 plugin_market.service 的只读查询与发布版本解析
type Service struct {
	mu          sync.RWMutex
	instanceDir string
	cache       *MarketCache
	guard       urlGuardOptions
	// enforcePublicOutbound 为动态配置读取器，非空时覆盖 guard 的静态开关
	enforcePublicOutbound func() bool
	// sourcePrefixOverride 仅用于测试：把 Git 源前缀指向本地服务
	sourcePrefixOverride string
	// cloneHook 仅用于测试：替换真实 Git 下载，覆盖安装链的清理与校验分支
	cloneHook       cloneRepositoryHook
	runtime         RuntimeCoordinator
	progress        func(ProgressUpdate)
	gitMaxRetries   int
	feedbackBaseURL string
	operations      *pluginOperationGuard
}

// NewService 创建市场服务，默认启用公网出站校验
func NewService() *Service {
	return &Service{
		cache:           NewMarketCache(256),
		guard:           urlGuardOptions{RequirePublicNetwork: true, AllowFakeIP: true},
		gitMaxRetries:   3,
		feedbackBaseURL: defaultFeedbackBaseURL,
		operations:      newPluginOperationGuard(),
	}
}

// SetInstanceDir 指定实例目录；留空时按 MAIBOT_INSTANCE_DIR 与工作目录解析
func (s *Service) SetInstanceDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instanceDir = dir
}

// SetEnforcePublicOutbound 关闭或开启出站公网校验，对齐 webui.enforce_public_outbound_url
func (s *Service) SetEnforcePublicOutbound(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.guard.RequirePublicNetwork = enabled
}

// SetEnforcePublicOutboundProvider 注入动态配置读取器，每次请求按当前配置校验
func (s *Service) SetEnforcePublicOutboundProvider(provider func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enforcePublicOutbound = provider
}

// guardForRequest 返回当前请求应使用的出站校验开关
func (s *Service) guardForRequest() urlGuardOptions {
	s.mu.RLock()
	options := s.guard
	provider := s.enforcePublicOutbound
	s.mu.RUnlock()
	if provider != nil {
		options.RequirePublicNetwork = provider()
	}
	return options
}

// SetRuntime 注入插件运行时协调器，用于安装后的加载与重载
func (s *Service) SetRuntime(coordinator RuntimeCoordinator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = coordinator
}

// SetProgressReporter 注入安装进度回调，对齐 update_progress 广播
func (s *Service) SetProgressReporter(reporter func(ProgressUpdate)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = reporter
}

// setSourcePrefixOverride 仅测试使用：覆盖 Git 源前缀
func (s *Service) setSourcePrefixOverride(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourcePrefixOverride = prefix
}

func (s *Service) instanceDirectory() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.instanceDir
}

func (s *Service) runtimeCoordinator() RuntimeCoordinator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtime
}

func (s *Service) reportProgress(update ProgressUpdate) {
	s.mu.RLock()
	reporter := s.progress
	s.mu.RUnlock()
	if reporter != nil {
		reporter(update)
	}
}

// List 对齐 get_catalog
func (s *Service) List(ctx context.Context, compatibleOnly bool, forceRefresh bool) (CatalogResponse, error) {
	snapshot, err := s.loadSnapshot(ctx, forceRefresh)
	if err != nil {
		return CatalogResponse{}, err
	}
	plugins := pluginsFromSnapshot(snapshot)
	if compatibleOnly {
		plugins = filterCompatible(plugins)
	}
	return CatalogResponse{Success: true, Metadata: snapshot.Meta, Plugins: plugins}, nil
}

// Search 对齐 search
func (s *Service) Search(ctx context.Context, request SearchRequest) (SearchResponse, error) {
	if err := request.Validate(); err != nil {
		return SearchResponse{}, err
	}
	request.Normalize()
	snapshot, err := s.loadSnapshot(ctx, request.ForceRefresh)
	if err != nil {
		return SearchResponse{}, err
	}
	plugins := pluginsFromSnapshot(snapshot)
	if request.IncludeStats || request.SortBy != SortName {
		summary, _, _, summaryErr := s.statisticsSnapshot(ctx, request.ForceRefresh)
		if summaryErr != nil {
			return SearchResponse{}, summaryErr
		}
		// 统计只按规范化后的 manifest.id 对应；没有记录时保留空值。
		plugins = attachStatistics(plugins, summary)
	}
	candidates := plugins
	if request.CompatibleOnly {
		candidates = filterCompatible(candidates)
	}
	if request.SortBy != SortName {
		candidates, err = sortStatPlugins(candidates, request.SortBy)
		if err != nil {
			return SearchResponse{}, err
		}
	} else {
		sort.SliceStable(candidates, func(i, j int) bool {
			left, right := strings.ToLower(candidates[i].Name), strings.ToLower(candidates[j].Name)
			if left != right {
				return left < right
			}
			return candidates[i].ID < candidates[j].ID
		})
	}
	filtered := filterPlugins(candidates, request.Query, request.Category)
	start := (request.Page - 1) * request.Limit
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + request.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	pages := 0
	if request.Limit > 0 {
		pages = (len(filtered) + request.Limit - 1) / request.Limit
	}
	drop := []string{"releases"}
	if !request.IncludeManifest {
		drop = append(drop, "manifest")
	}
	if !request.IncludeStats && request.SortBy == SortName {
		drop = append(drop, "stats")
	}
	pageItems := make([]map[string]any, 0, end-start)
	for _, plugin := range filtered[start:end] {
		pageItems = append(pageItems, pluginToMap(plugin, drop...))
	}
	return SearchResponse{
		Success:             true,
		Metadata:            snapshot.Meta,
		Query:               request.Query,
		Category:            request.Category,
		SortBy:              request.SortBy,
		Total:               len(plugins),
		Matched:             len(filtered),
		Page:                request.Page,
		Limit:               request.Limit,
		Pagination:          SearchPagination{TotalPages: pages, HasNext: request.Page < pages, HasPrevious: request.Page > 1 && pages > 0},
		AvailableCategories: collectCategories(plugins),
		Plugins:             pageItems,
	}, nil
}

// collectCategories 对齐 sorted({category ...})
func collectCategories(plugins []PluginCatalogItem) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, plugin := range plugins {
		for _, category := range plugin.Categories {
			if _, ok := seen[category]; ok {
				continue
			}
			seen[category] = struct{}{}
			out = append(out, category)
		}
	}
	sort.Strings(out)
	return out
}

// FindPlugin 对齐 _resolve：按 ID、名称或仓库地址唯一定位插件
func (s *Service) FindPlugin(ctx context.Context, reference string, forceRefresh bool) (PluginCatalogItem, error) {
	snapshot, err := s.loadSnapshot(ctx, forceRefresh)
	if err != nil {
		return PluginCatalogItem{}, err
	}
	return s.resolvePlugin(reference, snapshot.Plugins)
}

func (s *Service) resolvePlugin(reference string, plugins []PluginCatalogItem) (PluginCatalogItem, error) {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		return PluginCatalogItem{}, fmt.Errorf("plugin_id 不能为空")
	}
	folded := strings.ToLower(trimmed)
	matches := make([]PluginCatalogItem, 0, 1)
	for _, plugin := range plugins {
		if strings.ToLower(plugin.ID) == folded ||
			strings.ToLower(plugin.MarketplaceID) == folded ||
			strings.ToLower(plugin.Name) == folded ||
			strings.ToLower(plugin.RepositoryURL) == folded {
			matches = append(matches, plugin)
		}
	}
	if len(matches) == 0 {
		matches = filterPlugins(plugins, trimmed, "")
	}
	if len(matches) != 1 {
		candidates := make([]string, 0, 5)
		for _, plugin := range matches {
			if len(candidates) == 5 {
				break
			}
			candidates = append(candidates, plugin.ID)
		}
		status := 404
		if len(matches) > 0 {
			status = 409
		}
		return PluginCatalogItem{}, &MarketError{
			StatusCode: status,
			Message:    "未找到插件或匹配多个插件，请指定准确 ID。候选：" + strings.Join(candidates, ", "),
		}
	}
	return matches[0], nil
}

// Detail 对齐 get_detail
func (s *Service) Detail(ctx context.Context, pluginID string, forceRefresh bool) (DetailResponse, error) {
	snapshot, err := s.loadSnapshot(ctx, forceRefresh)
	if err != nil {
		return DetailResponse{}, err
	}
	plugin, err := s.resolvePlugin(pluginID, snapshot.Plugins)
	if err != nil {
		return DetailResponse{}, err
	}
	metadata := copyAnyMap(snapshot.Meta)
	source := catalogSource(metadata)
	pluginMap := normalizedCatalogMap(plugin, true)
	index, err := s.loadReleaseIndexWithSource(ctx, source, forceRefresh)
	if err != nil {
		return DetailResponse{}, err
	}
	var entry *PluginReleaseEntry
	for i := range index.Plugins {
		if index.Plugins[i].ID == plugin.MarketplaceID {
			entry = &index.Plugins[i]
			break
		}
	}
	if entry == nil {
		return DetailResponse{}, fmt.Errorf("版本索引中没有插件 %s", plugin.MarketplaceID)
	}
	releases := releaseDescription(entry, plugin.Manifest)
	return DetailResponse{Success: true, Metadata: metadata, Plugin: pluginMap, Releases: releases}, nil
}

// Stats 对齐 get_stats：读取独立缓存的官方统计
func (s *Service) Stats(ctx context.Context, pluginID string, forceRefresh bool) (StatsResponse, error) {
	stats, info, targetURL, err := s.statisticsPlugin(ctx, pluginID, forceRefresh)
	if err != nil {
		return StatsResponse{}, err
	}
	return StatsResponse{
		Success: true,
		Metadata: map[string]any{
			"source": "official", "source_url": targetURL, "cache": info,
		},
		Stats: stats,
	}, nil
}

// LoadReleaseIndex 对齐 load_release_index
func (s *Service) LoadReleaseIndex(ctx context.Context, forceRefresh bool) (*PluginReleaseIndex, error) {
	source, err := s.informationSource()
	if err != nil {
		return nil, err
	}
	return s.loadReleaseIndexWithSource(ctx, source, forceRefresh)
}

// LoadPluginReleaseEntry 对齐 load_plugin_release_entry
func (s *Service) LoadPluginReleaseEntry(ctx context.Context, pluginID string, forceRefresh bool) (*PluginReleaseEntry, error) {
	source, err := s.informationSource()
	if err != nil {
		return nil, err
	}
	index, err := s.loadReleaseIndexWithSource(ctx, source, forceRefresh)
	if err != nil {
		return nil, err
	}
	folded := strings.ToLower(pluginID)
	for i := range index.Plugins {
		entry := &index.Plugins[i]
		if strings.ToLower(entry.ID) == folded {
			return entry, nil
		}
		if entry.ManifestID != nil && strings.ToLower(*entry.ManifestID) == folded {
			return entry, nil
		}
	}
	return nil, nil
}

// ResolveRelease 对齐 service.resolve_release：安装前强制刷新远端发布状态
func (s *Service) ResolveRelease(ctx context.Context, pluginID, version string) (*PluginReleaseEntry, *PluginRelease, error) {
	entry, err := s.LoadPluginReleaseEntry(ctx, pluginID, true)
	if err != nil {
		return nil, nil, err
	}
	if entry == nil {
		return nil, nil, &MarketError{StatusCode: 404, Message: "插件尚未收录到版本索引，请先同步插件中心"}
	}
	if entry.SyncError != nil {
		return nil, nil, &MarketError{StatusCode: 409, Message: "插件版本同步失败：" + *entry.SyncError}
	}
	if entry.Mode == "branch" {
		if version != "latest" {
			return nil, nil, &MarketError{StatusCode: 400, Message: "该插件尚未发布 Release，只支持分支安装"}
		}
		return entry, nil, nil
	}
	if len(entry.Versions) == 0 {
		return nil, nil, &MarketError{StatusCode: 400, Message: "暂无可安装版本"}
	}
	description := describeEntry(entry)
	target := version
	if version == "latest" {
		if description.RecommendedVersion == nil {
			return nil, nil, &MarketError{StatusCode: 400, Message: "没有可安装的兼容稳定版本"}
		}
		target = *description.RecommendedVersion
	}
	release := findReleaseVersion(entry, target)
	if release == nil {
		message := "发布版本不存在"
		if version == "latest" {
			message = "没有可安装的兼容稳定版本"
		}
		return nil, nil, &MarketError{StatusCode: 400, Message: message}
	}
	if reasons := releaseCompatibility(*release); len(reasons) > 0 {
		return nil, nil, &MarketError{StatusCode: 400, Message: strings.Join(reasons, "；")}
	}
	return entry, release, nil
}

// normalizedCatalogMap 输出 dashboard 口径的插件字典，可选是否带 manifest
func normalizedCatalogMap(plugin PluginCatalogItem, includeManifest bool) map[string]any {
	dependencies := plugin.Dependencies
	if dependencies == nil {
		dependencies = []any{}
	}
	out := map[string]any{
		"id":                plugin.ID,
		"marketplace_id":    plugin.MarketplaceID,
		"manifest_version":  plugin.ManifestVersion,
		"name":              plugin.Name,
		"version":           plugin.Version,
		"description":       plugin.Description,
		"author":            plugin.Author,
		"license":           plugin.License,
		"host_application":  plugin.HostApplication,
		"sdk":               plugin.SDK,
		"repository_url":    plugin.RepositoryURL,
		"homepage_url":      plugin.HomepageURL,
		"documentation_url": plugin.DocumentationURL,
		"issues_url":        plugin.IssuesURL,
		"keywords":          plugin.Keywords,
		"categories":        plugin.Categories,
		"capabilities":      plugin.Capabilities,
		"dependencies":      dependencies,
		"default_locale":    plugin.DefaultLocale,
		"locales_path":      plugin.LocalesPath,
		"source":            plugin.Source,
		"stats":             plugin.Stats,
	}
	if includeManifest {
		out["manifest"] = plugin.Manifest
	}
	return out
}

// pluginToMap 把插件条目转成字典并删除指定字段，对齐 Python 的字典裁剪
func pluginToMap(plugin PluginCatalogItem, dropFields ...string) map[string]any {
	out := normalizedCatalogMap(plugin, plugin.Manifest != nil)
	dropped := map[string]struct{}{}
	for _, field := range dropFields {
		dropped[field] = struct{}{}
	}
	if _, ok := dropped["manifest"]; ok {
		delete(out, "manifest")
	}
	if _, ok := dropped["stats"]; ok {
		delete(out, "stats")
	} else if plugin.Stats == nil {
		out["stats"] = nil
	} else {
		out["stats"] = plugin.Stats
	}
	if _, ok := dropped["releases"]; !ok {
		out["releases"] = plugin.Releases
	}
	if _, ok := dropped["assets"]; !ok {
		if plugin.Assets == nil {
			out["assets"] = nil
		} else {
			out["assets"] = plugin.Assets
		}
	}
	if _, ok := dropped["changelog"]; !ok {
		out["changelog"] = plugin.Changelog
	}
	if _, ok := dropped["published_at"]; !ok {
		out["published_at"] = plugin.PublishedAt
	}
	if _, ok := dropped["updated_at"]; !ok {
		out["updated_at"] = plugin.UpdatedAt
	}
	return out
}

// setFeedbackBaseURL 仅测试使用：覆盖评论服务地址
func (s *Service) setFeedbackBaseURL(baseURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedbackBaseURL = baseURL
}
