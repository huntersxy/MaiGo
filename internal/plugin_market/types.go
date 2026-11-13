package pluginmarket

import "fmt"

// MarketSort 对齐 plugin_market.service.MarketSort
type MarketSort string

const (
	SortName              MarketSort = "name"
	SortDownloads         MarketSort = "downloads"
	SortLikes             MarketSort = "likes"
	SortRating            MarketSort = "rating"
	SortDownloadsGrowth7  MarketSort = "downloads_growth_7d"
	SortLikesGrowth7      MarketSort = "likes_growth_7d"
	SortDownloadsGrowth30 MarketSort = "downloads_growth_30d"
	SortLikesGrowth30     MarketSort = "likes_growth_30d"
)

// SearchRequest 对齐 service.MarketSearch
type SearchRequest struct {
	Query           string
	Category        string
	Page            int
	Limit           int
	SortBy          MarketSort
	CompatibleOnly  bool
	IncludeManifest bool
	IncludeStats    bool
	ForceRefresh    bool
}

// Validate 对齐 MarketSearch 的 pydantic 约束：越界直接拒绝，不静默截断
//
// Go 的零值同时表示「未提供」与「显式 0」，因此 0 交给 Normalize 补默认（Python 的
// Field(default=1/20) 同理），负数与超上限才是非法输入。
func (r *SearchRequest) Validate() error {
	if len([]rune(r.Query)) > 500 {
		return fmt.Errorf("query 长度不能超过 500")
	}
	if len([]rune(r.Category)) > 100 {
		return fmt.Errorf("category 长度不能超过 100")
	}
	if r.Page < 0 {
		return fmt.Errorf("page 必须大于等于 1")
	}
	if r.Limit < 0 || r.Limit > 50 {
		return fmt.Errorf("limit 必须在 1 到 50 之间")
	}
	return nil
}

// Normalize 补齐缺省值；越界值由 Validate 拒绝，这里只处理空值
func (r *SearchRequest) Normalize() {
	if r.Page < 1 {
		r.Page = 1
	}
	if r.Limit < 1 {
		r.Limit = 20
	}
	if r.SortBy == "" {
		r.SortBy = SortName
	}
}

// MarketFeatures 信息源能力标记
type MarketFeatures struct {
	Stats    bool `json:"stats"`
	Releases bool `json:"releases"`
}

// CatalogSnapshot 市场快照，metadata 保持 Python 的键集合与空值
type CatalogSnapshot struct {
	Meta    map[string]any
	Plugins []PluginCatalogItem
}

// Source 读取快照信息源
func (s *CatalogSnapshot) Source() string {
	if s == nil {
		return ""
	}
	return safeString(s.Meta["source"])
}

// Features 读取信息源能力
func (s *CatalogSnapshot) Features() MarketFeatures {
	if s == nil {
		return MarketFeatures{}
	}
	// 元数据既可能来自本地构造（MarketFeatures 结构），也可能来自缓存反序列化（map）
	switch raw := s.Meta["features"].(type) {
	case MarketFeatures:
		return raw
	case *MarketFeatures:
		if raw != nil {
			return *raw
		}
	case map[string]any:
		return MarketFeatures{
			Stats:    truthy(raw["stats"]),
			Releases: truthy(raw["releases"]),
		}
	}
	return MarketFeatures{}
}

func truthy(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

// CatalogResponse 对齐 get_catalog 的返回结构
type CatalogResponse struct {
	Success  bool
	Metadata map[string]any
	Plugins  []PluginCatalogItem
}

// Payload 输出 WebUI JSON 结构
func (r CatalogResponse) Payload() map[string]any {
	out := map[string]any{"success": r.Success}
	for key, value := range r.Metadata {
		out[key] = value
	}
	plugins := r.Plugins
	if plugins == nil {
		plugins = []PluginCatalogItem{}
	}
	out["plugins"] = plugins
	return out
}

// SearchPagination 分页信息
type SearchPagination struct {
	TotalPages  int  `json:"total_pages"`
	HasNext     bool `json:"has_next"`
	HasPrevious bool `json:"has_previous"`
}

// SearchResponse 对齐 search 的返回结构
type SearchResponse struct {
	Success             bool
	Metadata            map[string]any
	Query               string
	Category            string
	SortBy              MarketSort
	Total               int
	Matched             int
	Page                int
	Limit               int
	Pagination          SearchPagination
	AvailableCategories []string
	Plugins             []map[string]any
}

// Payload 输出 WebUI JSON 结构
func (r SearchResponse) Payload() map[string]any {
	out := map[string]any{"success": r.Success}
	for key, value := range r.Metadata {
		out[key] = value
	}
	categories := r.AvailableCategories
	if categories == nil {
		categories = []string{}
	}
	plugins := r.Plugins
	if plugins == nil {
		plugins = []map[string]any{}
	}
	out["query"] = r.Query
	out["category"] = r.Category
	out["sort_by"] = string(r.SortBy)
	out["total"] = r.Total
	out["matched"] = r.Matched
	out["page"] = r.Page
	out["limit"] = r.Limit
	out["pagination"] = r.Pagination
	out["available_categories"] = categories
	out["plugins"] = plugins
	return out
}

// DetailResponse 对齐 get_detail 的返回结构
type DetailResponse struct {
	Success  bool
	Metadata map[string]any
	Plugin   map[string]any
	Releases map[string]any
}

// Payload 输出 WebUI JSON 结构
func (r DetailResponse) Payload() map[string]any {
	out := map[string]any{"success": r.Success}
	for key, value := range r.Metadata {
		out[key] = value
	}
	out["plugin"] = r.Plugin
	out["releases"] = r.Releases
	return out
}

// StatsResponse 对齐 get_stats 的返回结构
type StatsResponse struct {
	Success  bool
	Metadata map[string]any
	Stats    map[string]any
}

// Payload 输出 WebUI JSON 结构
func (r StatsResponse) Payload() map[string]any {
	out := map[string]any{"success": r.Success}
	for key, value := range r.Metadata {
		out[key] = value
	}
	if r.Stats == nil {
		out["stats"] = nil
	} else {
		out["stats"] = r.Stats
	}
	return out
}

// DocumentResponse 对齐 get_readme / get_changelog 的返回结构
type DocumentResponse struct {
	Success    bool
	Plugin     map[string]string
	Content    string
	Chars      int
	Truncated  bool
	Source     string
	SourceType string
	Cache      *CacheInfo
}

// Payload 输出 WebUI JSON 结构
func (r DocumentResponse) Payload() map[string]any {
	out := map[string]any{
		"success":     r.Success,
		"plugin":      r.Plugin,
		"content":     r.Content,
		"chars":       r.Chars,
		"truncated":   r.Truncated,
		"source":      r.Source,
		"source_type": r.SourceType,
	}
	if r.Cache != nil {
		out["cache"] = *r.Cache
	}
	return out
}

// InstallResult 对齐 release_install._install_release 的返回结构
type InstallResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	PluginID   string   `json:"plugin_id"`
	PluginName string   `json:"plugin_name"`
	Version    string   `json:"version"`
	OldVersion *string  `json:"old_version"`
	NewVersion string   `json:"new_version"`
	Commit     string   `json:"commit"`
	UpdateMode string   `json:"update_mode"`
	BackupPath *string  `json:"backup_path"`
	Warnings   []string `json:"warnings"`
}

// Payload 输出 WebUI JSON 结构
func (r InstallResult) Payload() map[string]any {
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"success":     r.Success,
		"message":     r.Message,
		"plugin_id":   r.PluginID,
		"plugin_name": r.PluginName,
		"version":     r.Version,
		"old_version": r.OldVersion,
		"new_version": r.NewVersion,
		"commit":      r.Commit,
		"update_mode": r.UpdateMode,
		"backup_path": r.BackupPath,
		"warnings":    warnings,
	}
}
