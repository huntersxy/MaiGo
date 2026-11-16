package pluginmarket

import (
	"context"
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
// 未显式覆盖时回退构建版本，开发构建回退 1.4.0，不再使用无意义的 dev
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
