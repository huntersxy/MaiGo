package pluginmarket

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"MaiGo/internal/common/version"

	"go.uber.org/zap"
)

const (
	defaultMarketBaseURL = "http://hyybuth.xyz:10059"
	catalogTTL           = 30 * time.Minute
	marketTTL            = 5 * time.Minute
	requestTimeout       = 8 * time.Second
)

// marketBaseURL 对齐 MARKET_BASE_URL：环境变量覆盖并去掉尾部斜杠
func marketBaseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("MAIBOT_PLUGIN_MARKET_BASE_URL")), "/"); value != "" {
		return value
	}
	return defaultMarketBaseURL
}

// DefaultMaibotVersion 对齐 MMC_VERSION，用于市场兼容查询参数
// Python src/config/config.py:76 的 MMC_VERSION 来自 pyproject.toml:7 的 project.version
// 未显式覆盖时回退构建版本，开发构建回退 1.3.19，不再使用无意义的 dev
func DefaultMaibotVersion() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_VERSION")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("MAIBOT_MMC_VERSION")); value != "" {
		return value
	}
	return version.String()
}

// InformationFile 对齐 _fetch_information_file 的返回结构
type InformationFile struct {
	Success    bool
	Data       any
	MirrorUsed string
	Attempts   int
	URL        string
	Cache      CacheInfo
}

// marketResponse 对齐 request_market 的返回结构
type marketResponse struct {
	Data      map[string]any
	Cache     CacheInfo
	SourceURL string
}

// selectSource 选择 Git 源；测试可用前缀覆盖把请求指向本地服务
func (s *Service) selectSource(sourceID, customPrefix string) (GitSource, error) {
	source, err := SelectGitSource(sourceID, customPrefix)
	if err != nil {
		return GitSource{}, err
	}
	if s.sourcePrefixOverride != "" && sourceID != "custom" {
		source.ClonePrefix = s.sourcePrefixOverride
		source.RawPrefix = s.sourcePrefixOverride
	}
	return source, nil
}

// downloadSource 对齐 sources.download_source：下载源与信息源分开配置
func (s *Service) downloadSource() (GitSource, error) {
	settings, err := LoadSourceSettings(s.instanceDir)
	if err != nil {
		return GitSource{}, err
	}
	customPrefix := ""
	if settings.CustomDownloadPrefix != nil {
		customPrefix = *settings.CustomDownloadPrefix
	}
	return s.selectSource(settings.DownloadSource, customPrefix)
}

// informationSource 对齐 load_source_settings().information_source
func (s *Service) informationSource() (string, error) {
	settings, err := LoadSourceSettings(s.instanceDir)
	if err != nil {
		return "", err
	}
	return settings.InformationSource, nil
}

// sourceByInformationSource 把 official 信息源映射到 GitHub 原始文件源
func (s *Service) sourceByInformationSource(source string) (GitSource, error) {
	if source == "official" {
		source = "github"
	}
	return s.selectSource(source, "")
}

// fetchInformationFile 对齐 sources.fetch_information_file
func (s *Service) fetchInformationFile(ctx context.Context, filePath, source string, forceRefresh bool) (InformationFile, error) {
	selected := strings.TrimSpace(source)
	if selected == "" {
		value, err := s.informationSource()
		if err != nil {
			return InformationFile{}, err
		}
		selected = value
	}
	gitSource, err := s.selectSource(selected, "")
	if err != nil {
		return InformationFile{}, err
	}
	targetURL, err := gitSource.RawURL("Mai-with-u", "plugin-repo", "main", filePath)
	if err != nil {
		return InformationFile{}, err
	}
	loader := func() (any, error) {
		return gitSource.ReadText(ctx, targetURL, requestTimeout)
	}
	ttl := catalogTTL
	if filePath == "plugin_versions.json" {
		ttl = marketTTL
	}
	data, info, err := s.cache.Get(targetURL, loader, ttl, forceRefresh)
	if err != nil {
		return InformationFile{}, err
	}
	zap.L().Debug("插件市场索引已加载",
		zap.String("source", selected), zap.Bool("cache_hit", info.Hit), zap.String("url", targetURL))
	return InformationFile{Success: true, Data: data, MirrorUsed: selected, Attempts: 1, URL: targetURL, Cache: info}, nil
}

// requestMarket 对齐 sources.request_market：市场接口响应合并 cache 与 source_url
func (s *Service) requestMarket(ctx context.Context, path string, params url.Values, forceRefresh bool) (marketResponse, error) {
	targetURL := fmt.Sprintf("%s/api/v2/market/%s?%s", marketBaseURL(), path, params.Encode())
	loader := func() (any, error) {
		payload, err := GitSource{}.ReadJSON(ctx, targetURL, requestTimeout)
		if err != nil {
			return nil, err
		}
		return payload, nil
	}
	started := time.Now()
	data, info, err := s.cache.Get(targetURL, loader, marketTTL, forceRefresh)
	if err != nil {
		zap.L().Warn("插件市场请求失败",
			zap.String("url", targetURL), zap.Duration("elapsed", time.Since(started)), zap.Error(err))
		return marketResponse{}, err
	}
	payload, ok := data.(map[string]any)
	if !ok {
		return marketResponse{}, fmt.Errorf("插件市场响应不是对象")
	}
	zap.L().Debug("插件市场请求完成",
		zap.String("url", targetURL), zap.Bool("cache_hit", info.Hit), zap.Duration("elapsed", time.Since(started)))
	return marketResponse{Data: payload, Cache: info, SourceURL: targetURL}, nil
}

// RequestMarketDetail 对齐 sources.request_market_detail
func (s *Service) requestMarketDetail(ctx context.Context, pluginID, maibotVersion string, forceRefresh bool) (marketResponse, error) {
	params := url.Values{}
	params.Set("maibot_version", maibotVersion)
	return s.requestMarket(ctx, "plugins/"+quoteSegment(pluginID), params, forceRefresh)
}
