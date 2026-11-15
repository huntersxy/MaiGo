package services

// episode 管理服务，对齐 Python core/runtime/services/episode_admin_service.py

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
)

// EpisodeAdminDeps episode 管理服务依赖的存储与重建队列能力
type EpisodeAdminDeps struct {
	// Initialize 确保运行时已初始化
	Initialize func(ctx context.Context) error
	// EnqueueEpisodeSourceRebuild 入队单个来源的 episode 重建
	EnqueueEpisodeSourceRebuild func(source string, reason string, debounceSeconds float64)
	// ProcessEpisodeSourceRebuildBatch 处理 episode 来源重建批次
	ProcessEpisodeSourceRebuildBatch func(ctx context.Context, params map[string]any) (map[string]any, error)
	// QueryEpisodes 按条件查询 episode
	QueryEpisodes func(query string, filter storage.EpisodeQueryFilter, limit int) ([]map[string]any, error)
	// GetEpisodeByID 读取单个 episode
	GetEpisodeByID func(episodeID string) (map[string]any, error)
	// GetEpisodeParagraphs 读取 episode 的段落
	GetEpisodeParagraphs func(episodeID string, limit int) ([]map[string]any, error)
	// GetEpisodeSourceRebuildSummary 读取重建队列概况
	GetEpisodeSourceRebuildSummary func(failedLimit int) (map[string]any, error)
	// DiscardMigrationEpisodeRebuilds 丢弃迁移回填任务
	DiscardMigrationEpisodeRebuilds func(dryRun bool) (map[string]any, error)
	// ListEpisodeSourcesForRebuild 列出待重建来源
	ListEpisodeSourcesForRebuild func() ([]string, error)
	// GetAllSources 列出全部来源
	GetAllSources func() ([]map[string]any, error)
}

// EpisodeAdminService 对齐 MemoryEpisodeAdminService
type EpisodeAdminService struct {
	Deps EpisodeAdminDeps
}

// NewEpisodeAdminService 构造 episode 管理服务
func NewEpisodeAdminService(deps EpisodeAdminDeps) *EpisodeAdminService {
	return &EpisodeAdminService{Deps: deps}
}

// RebuildEpisodesForSources 对齐 rebuild_episodes_for_sources
func (s *EpisodeAdminService) RebuildEpisodesForSources(ctx context.Context, sources []string) (map[string]any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	sourceTokens := tokens(sources)
	if len(sourceTokens) > 0 && s.Deps.EnqueueEpisodeSourceRebuild != nil {
		for _, source := range sourceTokens {
			s.Deps.EnqueueEpisodeSourceRebuild(source, "episode_admin_rebuild", 0)
		}
	}
	result, err := s.processBatch(ctx, map[string]any{
		"sources":          sourceTokens,
		"limit":            pyIntOr(len(sourceTokens), 1, 1),
		"max_wait_seconds": 0.0,
	})
	if err != nil {
		return nil, err
	}
	result["sources"] = sourceTokens
	return result, nil
}

// processBatch 调用注入的批量处理端口并归一空结果
func (s *EpisodeAdminService) processBatch(ctx context.Context, params map[string]any) (map[string]any, error) {
	if s.Deps.ProcessEpisodeSourceRebuildBatch == nil {
		return nil, fmt.Errorf("episode rebuild service unavailable")
	}
	result, err := s.Deps.ProcessEpisodeSourceRebuildBatch(ctx, params)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

// MemoryEpisodeAdmin 对齐 memory_episode_admin
func (s *EpisodeAdminService) MemoryEpisodeAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if s.Deps.Initialize != nil {
		if err := s.Deps.Initialize(ctx); err != nil {
			return nil, err
		}
	}
	act := normalizeAction(action)
	switch act {
	case "query", "list":
		return s.queryEpisodes(payload)
	case "get":
		return s.getEpisode(payload)
	case "status":
		summary, err := s.Deps.GetEpisodeSourceRebuildSummary(pyIntOr(payload["limit"], 20, 1))
		if err != nil {
			return nil, err
		}
		out := map[string]any{"success": true}
		for key, value := range summary {
			out[key] = value
		}
		return out, nil
	case "discard_migration_backfill":
		dryRun := true
		if value, ok := payload["dry_run"]; ok {
			dryRun = pyTruthy(value)
		}
		result, err := s.Deps.DiscardMigrationEpisodeRebuilds(dryRun)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"success": true}
		for key, value := range result {
			out[key] = value
		}
		return out, nil
	case "rebuild":
		return s.rebuildAction(ctx, payload)
	case "process_sources":
		result, err := s.processBatch(ctx, map[string]any{
			"limit":            pyIntOr(payload["limit"], 20, 1),
			"max_retry":        pyIntOr(payload["max_retry"], 3, 0),
			"max_wait_seconds": 0.0,
		})
		if err != nil {
			return nil, err
		}
		success := pyIntOr(result["failed"], 0, 0) == 0 && pyIntOr(result["unfinished"], 0, 0) == 0
		out := map[string]any{"success": success}
		for key, value := range result {
			out[key] = value
		}
		return out, nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 episode action: %s", act)}, nil
	}
}

// queryEpisodes 对齐 action in {query,list}
func (s *EpisodeAdminService) queryEpisodes(payload map[string]any) (any, error) {
	timeFrom := payload["time_from"]
	if value, ok := payload["time_start"]; ok {
		timeFrom = value
	}
	timeTo := payload["time_to"]
	if value, ok := payload["time_end"]; ok {
		timeTo = value
	}
	person := strings.TrimSpace(asString(payload["person_id"]))
	if person == "" {
		person = strings.TrimSpace(asString(payload["person"]))
	}
	source := strings.TrimSpace(asString(payload["source"]))
	items, err := s.Deps.QueryEpisodes(
		strings.TrimSpace(asString(payload["query"])),
		storage.EpisodeQueryFilter{TimeFrom: optionalFloat(timeFrom), TimeTo: optionalFloat(timeTo), Person: person, Source: source},
		pyIntOr(payload["limit"], 20, 1),
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "items": items, "count": len(items)}, nil
}

// getEpisode 对齐 action=get
func (s *EpisodeAdminService) getEpisode(payload map[string]any) (any, error) {
	episodeID := strings.TrimSpace(asString(payload["episode_id"]))
	if episodeID == "" {
		return map[string]any{"success": false, "error": "episode_id 不能为空"}, nil
	}
	episode, err := s.Deps.GetEpisodeByID(episodeID)
	if err != nil {
		return nil, err
	}
	if episode == nil {
		return map[string]any{"success": false, "error": "episode 不存在"}, nil
	}
	paragraphs, err := s.Deps.GetEpisodeParagraphs(episodeID, pyIntOr(payload["paragraph_limit"], 100, 1))
	if err != nil {
		return nil, err
	}
	episode = cloneAnyMap(episode)
	episode["paragraphs"] = paragraphs
	return map[string]any{"success": true, "episode": episode}, nil
}

// rebuildAction 对齐 action=rebuild
func (s *EpisodeAdminService) rebuildAction(ctx context.Context, payload map[string]any) (any, error) {
	sources := tokens(payload["sources"])
	if len(sources) == 0 {
		source := strings.TrimSpace(asString(payload["source"]))
		if source != "" {
			sources = []string{source}
		}
	}
	if len(sources) == 0 && pyTruthy(payload["all"]) && s.Deps.ListEpisodeSourcesForRebuild != nil {
		listed, err := s.Deps.ListEpisodeSourcesForRebuild()
		if err != nil {
			return nil, err
		}
		sources = tokens(listed)
		if len(sources) == 0 && s.Deps.GetAllSources != nil {
			rows, err := s.Deps.GetAllSources()
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				sources = append(sources, strings.TrimSpace(asString(row["source"])))
			}
			sources = tokens(sources)
		}
	}
	if len(sources) == 0 {
		return map[string]any{"success": false, "error": "未提供可重建的 source"}, nil
	}
	result, err := s.RebuildEpisodesForSources(ctx, sources)
	if err != nil {
		return nil, err
	}
	completed := pyIntOr(result["rebuilt"], 0, 0)
	success := len(asList(result["failures"])) == 0 && pyIntOr(result["unfinished"], 0, 0) == 0 && completed == len(sources)
	out := map[string]any{"success": success}
	for key, value := range result {
		out[key] = value
	}
	return out, nil
}
