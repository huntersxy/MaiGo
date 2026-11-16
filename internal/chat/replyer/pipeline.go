package replyer

import (
	"context"
	"math/rand/v2"

	"MaiGo/internal/learners"

	"go.uber.org/zap"
)

// PipelineResult ApplyPipeline 输出
type PipelineResult struct {
	Segments   []Segment
	Expression learners.ExpressionSelectionResult
}

// ApplyPipeline 在 Planner 决定回复后处理纯文本，不直接发送
// 对齐 utils.process_llm_response_segments（规则断句路径）
func ApplyPipeline(text string, opts Options) PipelineResult {
	cfg := opts.mergedConfig()
	rng := opts.rng()

	var expression learners.ExpressionSelectionResult
	if opts.Expression != nil {
		ctx := context.Background()
		expression = SelectExpressions(ctx, *opts.Expression)
	}

	if opts.SkipPostProcess || !cfg.EnablePostProcess {
		segs := []Segment{{Text: text}}
		segs = attachTypingDelays(segs, opts.timingConfig(), cfg.TypingSpeed)
		return PipelineResult{Segments: segs, Expression: expression}
	}

	if cfg.Splitter.Mode == "llm" && opts.splitterEnabled(cfg) {
		return applyLLMSplitterPipeline(text, opts, cfg, rng, expression)
	}

	protected, kaomojiMap := protectKaomoji(text, cfg.Splitter.EnableKaomojiProtection)
	cleaned := stripChineseParentheticalContent(protected)
	if cleaned == "" {
		fallback := "呃呃"
		segs := []Segment{{Text: fallback}}
		segs = attachTypingDelays(segs, opts.timingConfig(), cfg.TypingSpeed)
		return PipelineResult{Segments: segs, Expression: expression}
	}

	maxLength := cfg.Splitter.MaxLength * 2
	if westernRatio(cleaned) < 0.1 && runeLen(cleaned) > maxLength {
		fallback := randomDefaultReply(opts.botNickname(), rng)
		segs := []Segment{{Text: fallback}}
		segs = attachTypingDelays(segs, opts.timingConfig(), cfg.TypingSpeed)
		return PipelineResult{Segments: segs, Expression: expression}
	}

	var pieces []sentencePiece
	if opts.splitterEnabled(cfg) {
		pieces = SplitIntoSentenceSegments(cleaned, rng)
	} else {
		pieces = []sentencePiece{{content: cleaned, separator: ""}}
	}

	segments := afterSplitSegments(pieces, opts)

	if len(segments) > cfg.Splitter.MaxSentenceNum {
		if cfg.Splitter.EnableOverflowReturnAll {
			segments = []Segment{{Text: cleaned}}
		} else {
			fallback := randomDefaultReply(opts.botNickname(), rng)
			segs := []Segment{{Text: fallback}}
			segs = attachTypingDelays(segs, opts.timingConfig(), cfg.TypingSpeed)
			return PipelineResult{Segments: segs, Expression: expression}
		}
	}

	segments = mergeSegmentsToMaxCount(segments, cfg.Splitter.MaxSplitNum)

	if cfg.Splitter.EnableKaomojiProtection {
		texts := make([]string, len(segments))
		for i, seg := range segments {
			texts[i] = seg.Text
		}
		recovered := recoverKaomoji(texts, kaomojiMap, true)
		for i := range segments {
			segments[i].Text = recovered[i]
		}
	}

	segments = attachTypingDelays(segments, opts.timingConfig(), cfg.TypingSpeed)
	return PipelineResult{Segments: segments, Expression: expression}
}

// applyLLMSplitterPipeline 对齐 utils.process_llm_response_segments_async 的 LLM 分支：
// 不保护颜文字、不清理括号，断句失败时整段不拆；错别字与压缩规则共用
func applyLLMSplitterPipeline(text string, opts Options, cfg Config, rng *rand.Rand, expression learners.ExpressionSelectionResult) PipelineResult {
	pieces, err := SplitTextWithLLM(opts.context(), text, opts.LLMSplitter)
	if err != nil {
		zap.L().Warn("LLM 断句失败，本次回复不断句", zap.Error(err))
		pieces = []sentencePiece{{content: text}}
	}

	segments := afterSplitSegments(pieces, opts)

	if len(segments) > cfg.Splitter.MaxSentenceNum {
		if cfg.Splitter.EnableOverflowReturnAll {
			segments = []Segment{{Text: text}}
		} else {
			fallback := randomDefaultReply(opts.botNickname(), rng)
			segs := []Segment{{Text: fallback}}
			segs = attachTypingDelays(segs, opts.timingConfig(), cfg.TypingSpeed)
			return PipelineResult{Segments: segs, Expression: expression}
		}
	}

	segments = mergeSegmentsToMaxCount(segments, cfg.Splitter.MaxSplitNum)
	segments = attachTypingDelays(segments, opts.timingConfig(), cfg.TypingSpeed)
	return PipelineResult{Segments: segments, Expression: expression}
}

// ApplyPipelineForTest 暴露段列表供 test/ 校验；生产代码请用 ApplyPipeline
func ApplyPipelineForTest(ctx context.Context, text string, opts Options) []Segment {
	if opts.Ctx == nil {
		opts.Ctx = ctx
	}
	return ApplyPipeline(text, opts).Segments
}

// afterSplitSegments 把断句结果交给插件 Hook 改写，再进入段数与合并限制
func afterSplitSegments(pieces []sentencePiece, opts Options) []Segment {
	segments := make([]Segment, 0, len(pieces))
	for _, piece := range pieces {
		segments = append(segments, Segment{Text: piece.content, Separator: piece.separator})
	}
	if opts.AfterSplit != nil {
		if modified := opts.AfterSplit(segments, opts.chineseTypoEnabled()); modified != nil {
			segments = modified
		}
	}
	if len(segments) == 0 {
		return []Segment{{Text: ""}}
	}
	return segments
}
