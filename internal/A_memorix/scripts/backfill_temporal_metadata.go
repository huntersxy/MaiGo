package scripts

// 段落时序字段回填脚本，对齐 src/A_memorix/scripts/backfill_temporal_metadata.py

import (
	"context"

	"maibot-go/internal/A_memorix/core/storage"
)

// newBackfillTemporalMetadataParser 构造参数解析器，对齐 argparse 定义
func newBackfillTemporalMetadataParser(ctx context.Context) *ArgParser {
	parser := newScriptParser(ctx, "backfill_temporal_metadata.py", "Backfill temporal metadata for A_Memorix paragraphs")
	parser.AddString([]string{"--data-dir"}, DefaultDataDir(), "数据目录")
	parser.AddBool([]string{"--dry-run"}, "仅统计，不写入")
	parser.AddInt([]string{"--limit"}, 100000, "最大处理条数")
	parser.AddBool([]string{"--no-created-fallback"}, "不使用 created_at 回填，仅输出候选数量")
	return parser
}

// RunBackfillTemporalMetadata 执行时序字段回填，返回进程退出码
// 说明：Python main 忽略 backfill 返回值恒返回 0，Go 版保持一致，仅异常返回 1
func RunBackfillTemporalMetadata(ctx context.Context, argv []string) int {
	parser := newBackfillTemporalMetadataParser(ctx)
	args, code, handled := parser.Parse(argv)
	if handled {
		return code
	}

	dataDir := ResolveRepoPath(args.String("data-dir"), DefaultDataDir())
	limit := args.Int("limit")
	if limit < 1 {
		limit = 1
	}
	dryRun := args.Bool("dry-run")
	noCreatedFallback := args.Bool("no-created-fallback")

	summary, err := backfillTemporalMetadata(dataDir, limit, dryRun, noCreatedFallback)
	if err != nil {
		scriptErrorf(ctx, "❌ 回填时序字段失败: %v\n", err)
		return 1
	}

	switch {
	case dryRun:
		scriptPrintf(ctx, "[dry-run] candidates=%d\n", summary["candidates"])
	case noCreatedFallback:
		scriptPrintf(ctx, "skip update (no-created-fallback), candidates=%d\n", summary["candidates"])
	default:
		scriptPrintf(ctx, "updated=%d\n", summary["updated"])
	}
	return 0
}

// backfillTemporalMetadata 对齐 backfill：数据目录就是 metadata.db 所在目录
func backfillTemporalMetadata(dataDir string, limit int, dryRun bool, noCreatedFallback bool) (map[string]int, error) {
	store := storage.NewStore(dataDir, "")
	if err := store.Connect(dataDir, true); err != nil {
		return nil, err
	}
	summary, err := store.BackfillTemporalMetadataFromCreatedAt(limit, dryRun, noCreatedFallback)
	closeErr := store.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return summary, nil
}
