package scripts

// Episode 来源级重建工具，对齐 src/A_memorix/scripts/rebuild_episodes.py

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// rebuildEpisodesLoggerName 脚本日志名称
const rebuildEpisodesLoggerName = "A_Memorix.RebuildEpisodes"

// episodeLeaseSeconds 对齐脚本中的 lease_seconds = 1800.0
const episodeLeaseSeconds = 1800.0

// rebuildFailure 记录来源失败原因，保持 Python dict 的插入顺序
type rebuildFailure struct {
	source string
	reason string
}

// RunRebuildEpisodes 执行 rebuild_episodes.py 等价流程，返回进程退出码
func RunRebuildEpisodes(ctx context.Context, argv []string) int {
	parser := newScriptParser(ctx, "rebuild_episodes.py", "Rebuild A_Memorix episodes by source")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "插件数据目录")
	parser.AddString([]string{"--source"}, "", "指定单个 source 入队/重建")
	parser.AddBool([]string{"--all"}, "对所有 source 入队/重建")
	parser.AddBool([]string{"--wait"}, "在脚本内同步执行重建")
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}
	source := strings.TrimSpace(args.String("source"))
	rebuildAll := args.Bool("all")
	if rebuildAll == (source != "") {
		scriptErrorf(ctx, "usage: rebuild_episodes.py [-h] [--data-dir DATA_DIR] [--source SOURCE] [--all] [--wait]\nrebuild_episodes.py: error: 必须且只能选择一个：--source 或 --all\n")
		return 2
	}

	logger := newScriptLogger(ctx, rebuildEpisodesLoggerName)
	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	metadataDir := filepath.Join(dataDir, "metadata")
	store := storage.NewStore(metadataDir, "")
	if err := store.Connect(metadataDir, true); err != nil {
		scriptErrorf(ctx, "连接元数据存储失败: %v\n", err)
		return 1
	}
	defer func() { _ = store.Close() }()

	sources, err := resolveRebuildSources(store, source, rebuildAll)
	if err != nil {
		scriptErrorf(ctx, "%v\n", err)
		return 1
	}
	if len(sources) == 0 {
		scriptPrintln(ctx, "no sources to rebuild")
		return 0
	}

	reason := "script_rebuild_source"
	if rebuildAll {
		reason = "script_rebuild_all"
	}
	debounceSeconds := 5.0
	if args.Bool("wait") {
		debounceSeconds = 0
	}
	enqueued := 0
	for _, item := range sources {
		if store.EnqueueEpisodeSourceRebuild(item, reason, storage.EpisodeEnqueueOptions{DebounceSeconds: debounceSeconds}) {
			enqueued++
		}
	}
	scriptPrintf(ctx, "enqueued=%d sources=%d\n", enqueued, len(sources))
	if !args.Bool("wait") {
		return 0
	}

	pluginConfig := map[string]any{}
	if loaded, loadErr := LoadTOML(DefaultConfigPath()); loadErr == nil {
		pluginConfig = loaded
	}
	return runEpisodeRebuilds(ctx, store, pluginConfig, sources, logger)
}

// resolveRebuildSources 对齐 _resolve_sources
func resolveRebuildSources(store *storage.Store, source string, rebuildAll bool) ([]string, error) {
	if rebuildAll {
		return store.ListEpisodeSourcesForRebuild(nil)
	}
	if source == "" {
		return nil, fmt.Errorf("必须提供 --source 或 --all")
	}
	return []string{source}, nil
}

// runEpisodeRebuilds 对齐 _run_rebuilds
func runEpisodeRebuilds(ctx context.Context, store *storage.Store, pluginConfig map[string]any, sources []string, logger utils.Logger) int {
	service := utils.NewEpisodeService(storageEpisodeDeps(store), pluginConfig, nil, false, logger)
	failures := make([]rebuildFailure, 0)
	failureIndex := make(map[string]int)
	recordFailure := func(source string, reason string) {
		if _, exists := failureIndex[source]; exists {
			return
		}
		failureIndex[source] = len(failures)
		failures = append(failures, rebuildFailure{source: source, reason: reason})
	}
	completed := make(map[string]struct{})
	remaining := dedupeSourceTokens(sources)

	generation := service.GenerationSignature()
	generationHash, err := service.GenerationHash(generation)
	if err != nil {
		scriptErrorf(ctx, "计算 generation hash 失败: %v\n", err)
		return 1
	}

	// 对齐 Python for _ in remaining_sources.copy()：用初始长度做上界，避免边删边缩轮次
	maxRounds := len(remaining)
	for round := 0; round < maxRounds; round++ {
		claims, err := store.ClaimEpisodeSourceRebuildBatch(storage.EpisodeClaimOptions{
			GenerationHash: generationHash,
			Sources:        remaining,
			Limit:          1,
			MaxRetry:       3,
			LeaseSeconds:   episodeLeaseSeconds,
			// Python 显式传 max_wait_seconds=0.0；Go 零值代表默认 60 秒，这里用极小值表达立即到期
			MaxWaitSeconds: 0.000001,
		}, nil)
		if err != nil {
			scriptErrorf(ctx, "领取 Episode 重建任务失败: %v\n", err)
			return 1
		}
		if len(claims) == 0 {
			break
		}
		claim := claims[0]
		claimSource := strings.TrimSpace(utils.PyStr(claim["source"]))
		remaining = removeSourceToken(remaining, claimSource)
		leaseToken := strings.TrimSpace(utils.PyStr(claim["lease_token"]))
		claimedRevision := utils.PyIntOrZero(claim["claimed_revision"])

		plan, planErr := planWithLeaseHeartbeat(ctx, store, service, claimSource, leaseToken, claimedRevision, generationHash, generation)
		if planErr != nil {
			errorText := truncateText(planErr.Error(), 500)
			if _, failErr := store.FailEpisodeSourceRebuild(storage.EpisodeFailOptions{
				Source: claimSource, LeaseToken: leaseToken, ClaimedRevision: claimedRevision, Error: errorText,
			}, nil); failErr != nil {
				scriptErrorf(ctx, "回写 Episode 重建失败状态异常: %v\n", failErr)
			}
			recordFailure(claimSource, errorText)
			scriptPrintf(ctx, "failed source=%s error=%s\n", claimSource, errorText)
			continue
		}

		result, publishErr := store.PublishEpisodeSourceRebuild(storage.EpisodePublishOptions{
			Source:                 claimSource,
			LeaseToken:             leaseToken,
			ClaimedRevision:        claimedRevision,
			GenerationHash:         generationHash,
			EpisodesPayloads:       mapListFromAny(plan["payloads"]),
			EmptyGroupFingerprints: stringListFromAny(plan["empty_group_fingerprints"]),
		}, nil)
		if publishErr != nil {
			errorText := truncateText(publishErr.Error(), 500)
			if _, failErr := store.FailEpisodeSourceRebuild(storage.EpisodeFailOptions{
				Source: claimSource, LeaseToken: leaseToken, ClaimedRevision: claimedRevision, Error: errorText,
			}, nil); failErr != nil {
				scriptErrorf(ctx, "回写 Episode 重建失败状态异常: %v\n", failErr)
			}
			recordFailure(claimSource, errorText)
			scriptPrintf(ctx, "failed source=%s error=%s\n", claimSource, errorText)
			continue
		}
		if !isTruthy(result["published"]) {
			reason := "lease_lost_or_claim_mismatch"
			if isTruthy(result["superseded"]) {
				reason = "superseded"
			}
			recordFailure(claimSource, reason)
			scriptPrintf(ctx, "unfinished source=%s reason=%s\n", claimSource, reason)
			continue
		}
		completed[claimSource] = struct{}{}
		scriptPrintf(ctx, "rebuilt source=%s paragraphs=%d groups=%d episodes=%d fallback=%d\n",
			claimSource,
			utils.PyIntOrZero(plan["paragraph_count"]),
			utils.PyIntOrZero(plan["group_count"]),
			utils.PyIntOrZero(result["episode_count"]),
			utils.PyIntOrZero(plan["fallback_count"]),
		)
	}

	for _, source := range sources {
		if _, done := completed[source]; done {
			continue
		}
		if _, failed := failureIndex[source]; failed {
			continue
		}
		recordFailure(source, "not_claimed")
	}

	if len(failures) > 0 {
		for _, failure := range failures {
			scriptPrintf(ctx, "%s: %s\n", failure.source, failure.reason)
		}
		return 1
	}
	return 0
}

// planWithLeaseHeartbeat 在计划生成期间按 Python _maintain_source_lease 续租
func planWithLeaseHeartbeat(
	ctx context.Context,
	store *storage.Store,
	service *utils.EpisodeService,
	source string,
	leaseToken string,
	claimedRevision int,
	generationHash string,
	generation map[string]any,
) (map[string]any, error) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := time.Duration(math.Max(0.1, episodeLeaseSeconds/3.0) * float64(time.Second))
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = store.RenewEpisodeSourceRebuildLease(storage.EpisodeLeaseRenewOptions{
					Source: source, LeaseToken: leaseToken, ClaimedRevision: claimedRevision,
					GenerationHash: generationHash, LeaseSeconds: episodeLeaseSeconds,
				}, nil)
			}
		}
	}()
	plan, err := service.PlanSourceRebuild(ctx, source, generation)
	close(stop)
	<-done
	return plan, err
}

// storageEpisodeDeps 用真实元数据存储实现 EpisodeService 依赖
func storageEpisodeDeps(store *storage.Store) utils.EpisodeStoreDeps {
	return utils.EpisodeStoreDeps{
		GetParagraphEntitiesByHashes: func(hashes []string) (map[string][]map[string]any, error) {
			return store.GetParagraphEntitiesByHashes(hashes)
		},
		GetLiveParagraphsBySource: func(source string, excludeStale bool) ([]map[string]any, error) {
			return store.GetLiveParagraphsBySource(source, excludeStale, nil)
		},
		GetEpisodesBySource: func(source string) ([]map[string]any, error) {
			return store.GetEpisodesBySource(source, nil)
		},
		GetEpisodeEmptyGroupFingerprints: func(source string) ([]string, error) {
			return store.GetEpisodeEmptyGroupFingerprints(source, nil)
		},
		ReplaceEpisodesForSource: func(source string, payloads []map[string]any, emptyGroupFingerprints []string) (map[string]any, error) {
			return store.ReplaceEpisodesForSource(source, payloads, emptyGroupFingerprints, nil)
		},
	}
}

// dedupeSourceTokens 保序去重，对齐 list(dict.fromkeys(sources))
func dedupeSourceTokens(sources []string) []string {
	seen := make(map[string]struct{}, len(sources))
	out := make([]string, 0, len(sources))
	for _, item := range sources {
		token := strings.TrimSpace(item)
		if token == "" {
			continue
		}
		if _, exists := seen[token]; exists {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
	}
	return out
}

// removeSourceToken 从剩余来源中移除已领取项
func removeSourceToken(sources []string, target string) []string {
	out := make([]string, 0, len(sources))
	for _, item := range sources {
		if item != target {
			out = append(out, item)
		}
	}
	return out
}

// mapListFromAny 把 any 切片转为 map 切片
func mapListFromAny(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

// stringListFromAny 把 any 切片转为字符串切片
func stringListFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, utils.PyStr(item))
		}
		return out
	default:
		return nil
	}
}
