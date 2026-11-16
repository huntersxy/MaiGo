package search

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Service 组合 Runner 与缓存，提供与 Python _execute_ai_search_request 对齐的执行流程
type Service struct {
	runner Runner
	cache  *Cache
}

// NewService 使用默认缓存构造服务
func NewService(runner Runner) *Service {
	return NewServiceWithCache(runner, NewCache(DefaultCacheTTL, DefaultCacheMaxEntries))
}

// NewServiceWithCache 允许测试注入共享缓存
func NewServiceWithCache(runner Runner, cache *Cache) *Service {
	if cache == nil {
		cache = NewCache(DefaultCacheTTL, DefaultCacheMaxEntries)
	}
	return &Service{runner: runner, cache: cache}
}

// Runner 返回底层执行器
func (s *Service) Runner() Runner {
	if s == nil {
		return nil
	}
	return s.runner
}

// Execute 执行一次带进度回调的 AI 搜索并管理缓存
func (s *Service) Execute(ctx context.Context, request Request, emit Emit) (Response, error) {
	if err := ValidateRequest(request); err != nil {
		return Response{}, err
	}
	if s == nil || s.runner == nil {
		return Response{}, &UnavailableError{Message: "AI 搜索服务未接线：需要可注入的 SearchRunner 或 LLM 路由器"}
	}
	if emit == nil {
		emit = func(ProgressEvent) {}
	}
	cacheable := request.HelpSessionID == nil && !request.AllowPluginConfigWrite
	key := ""
	if cacheable {
		key = Key(request)
		if cached, ok := s.cache.Get(key); ok {
			emit(NewProgressEvent("cache_hit"))
			return cached, nil
		}
	}
	response, err := s.runner.Run(ctx, request, emit)
	if err != nil {
		return Response{}, err
	}
	response = normalizeResponse(response)
	if cacheable && !response.UsedLocalConfig && !response.UsedPluginMarket && response.GroundingError == "" {
		s.cache.Set(key, response)
	}
	return response, nil
}

func normalizeResponse(response Response) Response {
	if response.Suggestions == nil {
		response.Suggestions = []string{}
	}
	if response.Sources == nil {
		response.Sources = []Source{}
	}
	if response.ExpandedTerms == nil {
		response.ExpandedTerms = []string{}
	}
	if response.Results == nil {
		response.Results = []ModelResult{}
	}
	if response.Plugins == nil {
		response.Plugins = []PluginInfo{}
	}
	if response.InstallRequests == nil {
		response.InstallRequests = []InstallRequest{}
	}
	if response.ConfigUpdates == nil {
		response.ConfigUpdates = []ConfigUpdate{}
	}
	if response.Artifacts == nil {
		response.Artifacts = []HelpArtifact{}
	}
	response.Answer = strings.TrimSpace(response.Answer)
	return response
}

func trimQuery(query string) string { return strings.TrimSpace(query) }

// IsValidation 判断是否为请求校验错误
func IsValidation(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

// StatusCode 把搜索错误映射为 HTTP 状态码，对齐 Python 路由的 400/502
func StatusCode(err error) int {
	var validation *ValidationError
	if errors.As(err, &validation) {
		if strings.Contains(validation.Message, "重复 ID") {
			return 400
		}
		return 422
	}
	var unavailable *UnavailableError
	if errors.As(err, &unavailable) {
		return 503
	}
	return 502
}

// Timeout 默认单次搜索硬超时
const Timeout = 120 * time.Second
