package websearch

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"go.uber.org/zap"
)

const (
	// DefaultPreviewChars 对齐 MaiBot web.preview_chars 默认值
	DefaultPreviewChars = 4000
	// MinPreviewChars / MaxPreviewChars 对齐 pydantic ge=500 le=12000
	MinPreviewChars = 500
	MaxPreviewChars = 12000

	// DefaultWebTimeout 见 adapters_bridge.go（转发自 adapters）
	MaxWebTimeout = 300 * time.Second
)

// ErrNoWebConfig 表示 model_config.toml 没有 [web] 联网配置，调用方应回退 env
var ErrNoWebConfig = errors.New("model_config 未配置 web 联网服务")

// CodeWebServiceConfigInvalid 对齐 protocol errors 的联网配置错误码
const CodeWebServiceConfigInvalid = "E_WEB_SERVICE_CONFIG_INVALID"

// ConfigError 已分配的联网服务缺失或配置不完整
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string { return e.Message }

// ErrorCode 供插件能力映射为 RPC 错误码
func (e *ConfigError) ErrorCode() string { return CodeWebServiceConfigInvalid }

// ServiceConfig 单个联网服务的调用配置快照
type ServiceConfig struct {
	Adapter   string
	APIKey    string
	ServerRef string
}

// RuntimeConfig 运行时联网配置；TOML 快照 + env 覆盖后的扁平结果
type RuntimeConfig struct {
	Enabled bool

	SearchAdapter string
	FetchAdapter  string
	// SearchService / FetchService 记录 TOML 中选中的服务名，便于诊断
	SearchService string
	FetchService  string
	// Services 保留服务定义，调用时校验服务是否存在且配置完整
	Services map[string]ServiceConfig

	PreviewChars int
	Timeout      time.Duration
	ProxyURL     string

	SearchDepth  string
	ExtractDepth string
	Category     string

	DeepSeekAPIKey   string
	DeepSeekBaseURL  string
	TavilyAPIKey     string
	TavilyBaseURL    string
	ExaAPIKey        string
	ExaBaseURL       string
	FirecrawlAPIKey  string
	FirecrawlBaseURL string
	AnySearchAPIKey  string
	AnySearchBaseURL string

	StepSearchMCPServer    string
	StepSearchMCPTool      string
	StepSearchMCPFetchTool string
	StepSearchCategory     string
}

// ConfigProvider 每次调用返回当前配置快照；动态实现用于 TOML 热重载
type ConfigProvider func() (RuntimeConfig, error)

// DefaultRuntimeConfig 返回 env-only 模式的默认值；TOML 无 [web] 时回退到该配置
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Enabled:      true,
		PreviewChars: DefaultPreviewChars,
		Timeout:      DefaultWebTimeout,
		SearchDepth:  "basic",
		ExtractDepth: "basic",
	}
}

// LoadRuntimeFromEnv 从环境变量加载；未配置 API Key 时搜索回退 Bing 抓取
func LoadRuntimeFromEnv() RuntimeConfig {
	cfg := DefaultRuntimeConfig()
	applyEnvOverrides(&cfg)
	finalizeRuntimeConfig(&cfg)
	return cfg
}

func applyEnvAndValidate(cfg RuntimeConfig) (RuntimeConfig, error) {
	applyEnvOverrides(&cfg)
	finalizeRuntimeConfig(&cfg)
	if err := validateRuntimeConfig(cfg); err != nil {
		return RuntimeConfig{}, err
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *RuntimeConfig) {
	if cfg == nil {
		return
	}
	envString := func(name string, target *string) {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			*target = value
		}
	}
	envString("MAIBOT_WEB_SEARCH_ADAPTER", &cfg.SearchAdapter)
	envString("MAIBOT_WEB_FETCH_ADAPTER", &cfg.FetchAdapter)
	envString("MAIBOT_WEB_SEARCH_DEPTH", &cfg.SearchDepth)
	envString("MAIBOT_WEB_EXTRACT_DEPTH", &cfg.ExtractDepth)
	envString("MAIBOT_WEB_CATEGORY", &cfg.Category)
	envString("MAIBOT_WEB_PROXY", &cfg.ProxyURL)

	envString("MAIBOT_DEEPSEEK_API_KEY", &cfg.DeepSeekAPIKey)
	envString("MAIBOT_DEEPSEEK_BASE_URL", &cfg.DeepSeekBaseURL)
	envString("MAIBOT_TAVILY_API_KEY", &cfg.TavilyAPIKey)
	envString("MAIBOT_TAVILY_BASE_URL", &cfg.TavilyBaseURL)
	envString("MAIBOT_EXA_API_KEY", &cfg.ExaAPIKey)
	envString("MAIBOT_EXA_BASE_URL", &cfg.ExaBaseURL)
	envString("MAIBOT_FIRECRAWL_API_KEY", &cfg.FirecrawlAPIKey)
	envString("MAIBOT_FIRECRAWL_BASE_URL", &cfg.FirecrawlBaseURL)
	envString("MAIBOT_ANYSEARCH_API_KEY", &cfg.AnySearchAPIKey)
	envString("MAIBOT_ANYSEARCH_BASE_URL", &cfg.AnySearchBaseURL)

	envString("MAIBOT_STEPSEARCH_MCP_SERVER", &cfg.StepSearchMCPServer)
	envString("MAIBOT_STEPSEARCH_MCP_TOOL", &cfg.StepSearchMCPTool)
	envString("MAIBOT_STEPSEARCH_MCP_FETCH_TOOL", &cfg.StepSearchMCPFetchTool)
	envString("MAIBOT_STEPSEARCH_CATEGORY", &cfg.StepSearchCategory)

	if raw := strings.TrimSpace(os.Getenv("MAIBOT_WEB_SEARCH_ENABLED")); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil {
			cfg.Enabled = enabled
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_WEB_PREVIEW_CHARS")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			cfg.PreviewChars = value
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MAIBOT_WEB_SEARCH_TIMEOUT_SECONDS")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 {
			cfg.Timeout = time.Duration(value) * time.Second
		}
	}
}

func finalizeRuntimeConfig(cfg *RuntimeConfig) {
	if cfg == nil {
		return
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultWebTimeout
	}
	if cfg.PreviewChars <= 0 {
		cfg.PreviewChars = DefaultPreviewChars
	}
	if strings.TrimSpace(cfg.SearchDepth) == "" {
		cfg.SearchDepth = "basic"
	}
	if strings.TrimSpace(cfg.ExtractDepth) == "" {
		cfg.ExtractDepth = "basic"
	}
	cfg.SearchDepth = strings.ToLower(strings.TrimSpace(cfg.SearchDepth))
	cfg.ExtractDepth = strings.ToLower(strings.TrimSpace(cfg.ExtractDepth))
	cfg.Category = strings.TrimSpace(cfg.Category)
	if cfg.SearchAdapter == "" && strings.TrimSpace(cfg.SearchService) == "" {
		switch {
		case strings.TrimSpace(cfg.TavilyAPIKey) != "":
			cfg.SearchAdapter = "tavily"
		case strings.TrimSpace(cfg.DeepSeekAPIKey) != "":
			cfg.SearchAdapter = "deepseek"
		default:
			cfg.SearchAdapter = "bing"
		}
	}
	if cfg.FetchAdapter == "" && strings.TrimSpace(cfg.FetchService) == "" {
		switch {
		case strings.TrimSpace(cfg.TavilyAPIKey) != "":
			cfg.FetchAdapter = "tavily"
		case strings.TrimSpace(cfg.DeepSeekAPIKey) != "":
			cfg.FetchAdapter = "deepseek"
		default:
			cfg.FetchAdapter = "http"
		}
	}
	if strings.TrimSpace(cfg.DeepSeekBaseURL) == "" {
		cfg.DeepSeekBaseURL = "https://api.deepseek.com"
	}
	if strings.TrimSpace(cfg.TavilyBaseURL) == "" {
		cfg.TavilyBaseURL = "https://api.tavily.com"
	}
	if strings.TrimSpace(cfg.ExaBaseURL) == "" {
		cfg.ExaBaseURL = "https://api.exa.ai"
	}
	if strings.TrimSpace(cfg.FirecrawlBaseURL) == "" {
		cfg.FirecrawlBaseURL = "https://api.firecrawl.dev"
	}
	if strings.TrimSpace(cfg.AnySearchBaseURL) == "" {
		cfg.AnySearchBaseURL = "https://api.anysearch.com"
	}
}

func validateRuntimeConfig(cfg RuntimeConfig) error {
	if cfg.PreviewChars < MinPreviewChars || cfg.PreviewChars > MaxPreviewChars {
		return fmt.Errorf("web.preview_chars 必须在 %d 到 %d 之间", MinPreviewChars, MaxPreviewChars)
	}
	if cfg.Timeout <= 0 || cfg.Timeout > MaxWebTimeout {
		return fmt.Errorf("联网服务 timeout 必须大于 0 且不超过 300 秒")
	}
	for label, adapter := range map[string]string{"search": cfg.SearchAdapter, "fetch": cfg.FetchAdapter} {
		switch strings.ToLower(strings.TrimSpace(adapter)) {
		case "deepseek", "tavily", "exa", "firecrawl", "anysearch", "stepsearch", "stepsearch_mcp", "bing", "http", "":
		default:
			return fmt.Errorf("未知%s供应商: %s", label, adapter)
		}
	}
	switch cfg.SearchDepth {
	case "basic", "advanced", "fast", "ultra-fast":
	default:
		return fmt.Errorf("web.search_depth 必须是 basic、advanced、fast 或 ultra-fast")
	}
	switch cfg.ExtractDepth {
	case "basic", "advanced":
	default:
		return fmt.Errorf("web.extract_depth 必须是 basic 或 advanced")
	}
	switch cfg.Category {
	case "", "programming", "research", "gov", "business":
	default:
		return fmt.Errorf("web.category 必须是空、programming、research、gov 或 business")
	}
	for _, raw := range []string{cfg.DeepSeekBaseURL, cfg.TavilyBaseURL, cfg.ExaBaseURL, cfg.FirecrawlBaseURL, cfg.AnySearchBaseURL} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if err := validateBaseURL(raw); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		if err := validateProxyURL(cfg.ProxyURL); err != nil {
			return err
		}
	}
	return nil
}

func validateBaseURL(raw string) error {
	parts, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parts.Scheme != "http" && parts.Scheme != "https") || parts.Hostname() == "" || parts.User != nil {
		return fmt.Errorf("联网服务 base_url 必须是无用户名和密码的 HTTP(S) 地址")
	}
	if parts.RawQuery != "" || parts.Fragment != "" {
		return fmt.Errorf("联网服务 base_url 不能包含查询参数或片段")
	}
	return nil
}

func validateProxyURL(raw string) error {
	parts, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parts.Host == "" {
		return fmt.Errorf("联网服务 proxy 必须是有效的代理地址")
	}
	switch strings.ToLower(parts.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("联网服务 proxy 只支持 http、https、socks5 或 socks5h")
	}
}

// rawModelConfig 只解析 web 节和 web_search 任务分配，避免触发完整 maibot 配置升级
type rawModelConfig struct {
	Web             *rawWebConfig       `toml:"web"`
	ModelTaskConfig *rawModelTaskConfig `toml:"model_task_config"`
}

type rawWebConfig struct {
	PreviewChars *int                  `toml:"preview_chars"`
	Services     map[string]rawService `toml:"services"`
}

type rawModelTaskConfig struct {
	WebSearch *rawWebSearchTask `toml:"web_search"`
}

type rawWebSearchTask struct {
	ModelList []string `toml:"model_list"`
}

type rawService struct {
	Adapter      string   `toml:"adapter"`
	BaseURL      string   `toml:"base_url"`
	APIKey       string   `toml:"api_key"`
	ServerRef    string   `toml:"server_ref"`
	Timeout      *float64 `toml:"timeout"`
	SearchDepth  string   `toml:"search_depth"`
	ExtractDepth string   `toml:"extract_depth"`
	Category     string   `toml:"category"`
	Proxy        string   `toml:"proxy"`
}

// LoadRuntimeFromTOML 读取 model_config.toml 的 [web] 与 web_search 分配并合并 env 覆盖
func LoadRuntimeFromTOML(path string) (RuntimeConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("读取 model_config.toml: %w", err)
	}
	return ParseRuntimeTOML(raw)
}

// ParseRuntimeTOML 解析 model_config.toml 字节；没有 web 配置时返回 ErrNoWebConfig
func ParseRuntimeTOML(raw []byte) (RuntimeConfig, error) {
	var doc rawModelConfig
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return RuntimeConfig{}, fmt.Errorf("解析 model_config.toml: %w", err)
	}
	if doc.Web == nil {
		if doc.ModelTaskConfig == nil || doc.ModelTaskConfig.WebSearch == nil || len(doc.ModelTaskConfig.WebSearch.ModelList) == 0 {
			return RuntimeConfig{}, ErrNoWebConfig
		}
	}
	cfg := DefaultRuntimeConfig()
	cfg.Enabled = false
	services := map[string]rawService{}
	if doc.Web != nil {
		if doc.Web.PreviewChars != nil {
			cfg.PreviewChars = *doc.Web.PreviewChars
		}
		for name, service := range doc.Web.Services {
			if name == "" || strings.TrimSpace(name) != name {
				return RuntimeConfig{}, fmt.Errorf("联网服务名称不能为空或包含首尾空格")
			}
			services[name] = service
		}
	}
	cfg.Services = make(map[string]ServiceConfig, len(services))
	for name, service := range services {
		if err := validateRawService(name, service); err != nil {
			return RuntimeConfig{}, err
		}
		cfg.Services[name] = ServiceConfig{
			Adapter:   normalizedAdapter(service.Adapter),
			APIKey:    strings.TrimSpace(service.APIKey),
			ServerRef: strings.TrimSpace(service.ServerRef),
		}
	}
	selected := ""
	if doc.ModelTaskConfig != nil && doc.ModelTaskConfig.WebSearch != nil {
		list := doc.ModelTaskConfig.WebSearch.ModelList
		if len(list) > 1 {
			return RuntimeConfig{}, fmt.Errorf("web_search.model_list 最多只能选择一个联网服务")
		}
		if len(list) == 1 {
			selected = strings.TrimSpace(list[0])
			if selected == "" {
				return RuntimeConfig{}, fmt.Errorf("web_search.model_list 不能包含空服务名")
			}
		}
	}
	if selected != "" {
		if service, ok := services[selected]; ok {
			applySelectedService(&cfg, selected, service)
		} else {
			// 服务缺失不阻止启动；调用时返回 E_WEB_SERVICE_CONFIG_INVALID
			cfg.Enabled = true
			cfg.SearchService = selected
			cfg.FetchService = selected
		}
		if err := requireWebService(cfg, selected); err != nil {
			zap.L().Warn("联网工具配置有误，调用时将返回 "+CodeWebServiceConfigInvalid,
				zap.Error(err))
		}
	}
	return applyEnvAndValidate(cfg)
}

func validateRawService(name string, service rawService) error {
	adapter := strings.ToLower(strings.TrimSpace(service.Adapter))
	if adapter == "" {
		adapter = "deepseek"
	}
	switch adapter {
	case "deepseek", "tavily", "exa", "firecrawl", "anysearch", "stepsearch", "stepsearch_mcp", "bing", "http":
	default:
		return fmt.Errorf("联网服务 %s 的 adapter 不受支持: %s", name, service.Adapter)
	}
	if (adapter == "stepsearch" || adapter == "stepsearch_mcp") &&
		(strings.TrimSpace(service.APIKey) != "" || strings.TrimSpace(service.BaseURL) != "") {
		return fmt.Errorf("StepSearch MCP 请使用 server_ref，地址和密钥在 MCP 连接中配置")
	}
	if strings.TrimSpace(service.BaseURL) != "" {
		if err := validateBaseURL(service.BaseURL); err != nil {
			return fmt.Errorf("联网服务 %s: %w", name, err)
		}
	}
	if service.Timeout != nil {
		if *service.Timeout <= 0 || *service.Timeout > 300 {
			return fmt.Errorf("联网服务 %s 的 timeout 必须大于 0 且不超过 300 秒", name)
		}
	}
	if depth := strings.ToLower(strings.TrimSpace(service.SearchDepth)); depth != "" {
		switch depth {
		case "basic", "advanced", "fast", "ultra-fast":
		default:
			return fmt.Errorf("联网服务 %s 的 search_depth 必须是 basic、advanced、fast 或 ultra-fast", name)
		}
	}
	if depth := strings.ToLower(strings.TrimSpace(service.ExtractDepth)); depth != "" {
		switch depth {
		case "basic", "advanced":
		default:
			return fmt.Errorf("联网服务 %s 的 extract_depth 必须是 basic 或 advanced", name)
		}
	}
	if category := strings.TrimSpace(service.Category); category != "" {
		switch category {
		case "programming", "research", "gov", "business":
		default:
			return fmt.Errorf("联网服务 %s 的 category 必须是空、programming、research、gov 或 business", name)
		}
	}
	if strings.TrimSpace(service.Proxy) != "" {
		if err := validateProxyURL(service.Proxy); err != nil {
			return fmt.Errorf("联网服务 %s: %w", name, err)
		}
	}
	return nil
}

func normalizedAdapter(raw string) string {
	adapter := strings.ToLower(strings.TrimSpace(raw))
	if adapter == "" {
		return "deepseek"
	}
	if adapter == "stepsearch" {
		return "stepsearch_mcp"
	}
	return adapter
}

// requireWebService 获取指定联网服务并检查调用所需配置
func requireWebService(cfg RuntimeConfig, name string) error {
	service, ok := cfg.Services[strings.TrimSpace(name)]
	if !ok {
		return &ConfigError{Message: fmt.Sprintf("web_search 引用了不存在的联网服务：%s，请修正或取消该功能分配", name)}
	}
	field, value := "api_key", service.APIKey
	if service.Adapter == "stepsearch_mcp" {
		field, value = "server_ref", service.ServerRef
	}
	if strings.TrimSpace(value) == "" {
		return &ConfigError{Message: fmt.Sprintf("联网服务 %s 缺少 %s", name, field)}
	}
	return nil
}

func applySelectedService(cfg *RuntimeConfig, name string, service rawService) {
	adapter := normalizedAdapter(service.Adapter)
	cfg.Enabled = true
	cfg.SearchAdapter = adapter
	cfg.FetchAdapter = adapter
	cfg.SearchService = name
	cfg.FetchService = name
	cfg.Category = strings.TrimSpace(service.Category)
	if depth := strings.ToLower(strings.TrimSpace(service.SearchDepth)); depth != "" {
		cfg.SearchDepth = depth
	}
	if depth := strings.ToLower(strings.TrimSpace(service.ExtractDepth)); depth != "" {
		cfg.ExtractDepth = depth
	}
	if service.Timeout != nil {
		cfg.Timeout = time.Duration(*service.Timeout * float64(time.Second))
	}
	if strings.TrimSpace(service.Proxy) != "" {
		cfg.ProxyURL = strings.TrimSpace(service.Proxy)
	}
	baseURL := strings.TrimSpace(service.BaseURL)
	apiKey := strings.TrimSpace(service.APIKey)
	switch adapter {
	case "deepseek":
		cfg.DeepSeekAPIKey = apiKey
		cfg.DeepSeekBaseURL = baseURL
	case "tavily":
		cfg.TavilyAPIKey = apiKey
		cfg.TavilyBaseURL = baseURL
	case "exa":
		cfg.ExaAPIKey = apiKey
		cfg.ExaBaseURL = baseURL
	case "firecrawl":
		cfg.FirecrawlAPIKey = apiKey
		cfg.FirecrawlBaseURL = baseURL
	case "anysearch":
		cfg.AnySearchAPIKey = apiKey
		cfg.AnySearchBaseURL = baseURL
	case "stepsearch", "stepsearch_mcp":
		cfg.StepSearchMCPServer = strings.TrimSpace(service.ServerRef)
		cfg.StepSearchCategory = strings.TrimSpace(service.Category)
		cfg.SearchAdapter = "stepsearch"
		cfg.FetchAdapter = "stepsearch"
	}
}

// ModelConfigPathFromEnv 返回当前实例的 model_config.toml；MAIBOT_MODEL_CONFIG_PATH 优先
func ModelConfigPathFromEnv() string {
	if path := strings.TrimSpace(os.Getenv("MAIBOT_MODEL_CONFIG_PATH")); path != "" {
		return path
	}
	root := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR"))
	if root == "" {
		return ""
	}
	return filepath.Join(root, "config", "model_config.toml")
}

type configCache struct {
	mu        sync.Mutex
	path      string
	modTime   time.Time
	size      int64
	parsed    RuntimeConfig
	hasParsed bool
	lastErr   error
}

var defaultConfigCache configCache

// CurrentRuntimeConfig 返回当前 TOML/env 合并快照；文件未变化时复用解析结果
func CurrentRuntimeConfig() (RuntimeConfig, error) {
	return defaultConfigCache.load()
}

func (c *configCache) load() (RuntimeConfig, error) {
	path := ModelConfigPathFromEnv()
	if path == "" {
		return applyEnvAndValidate(DefaultRuntimeConfig())
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return applyEnvAndValidate(DefaultRuntimeConfig())
		}
		return RuntimeConfig{}, fmt.Errorf("读取 model_config.toml: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == path && c.hasParsed && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		return applyEnvAndValidate(c.parsed)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("读取 model_config.toml: %w", err)
	}
	parsed, err := ParseRuntimeTOML(raw)
	if err != nil {
		c.lastErr = err
		if c.hasParsed && c.path == path {
			// 对齐 ConfigManager.reload_config：热重载失败保留上一份有效快照
			zap.L().Warn("联网 TOML 配置热重载失败，继续使用上一份快照", zap.Error(err))
			return applyEnvAndValidate(c.parsed)
		}
		if errors.Is(err, ErrNoWebConfig) {
			return applyEnvAndValidate(DefaultRuntimeConfig())
		}
		return RuntimeConfig{}, err
	}
	c.path = path
	c.modTime = info.ModTime()
	c.size = info.Size()
	c.parsed = parsed
	c.hasParsed = true
	c.lastErr = nil
	return applyEnvAndValidate(parsed)
}

// LastConfigError 返回最近一次热重载失败原因；成功时为空
func LastConfigError() error {
	defaultConfigCache.mu.Lock()
	defer defaultConfigCache.mu.Unlock()
	return defaultConfigCache.lastErr
}
