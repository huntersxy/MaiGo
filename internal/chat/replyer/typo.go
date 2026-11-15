package replyer

import (
	"context"
	"math/rand/v2"
	"strings"
	"unicode/utf8"

	"MaiGo/internal/services/pythoncompat"

	"go.uber.org/zap"
)

// TypoEngine 错别字注入后端；nil 时按 Python 兼容侧车配置自动选择。
//
// 默认顺序是「侧车优先、失败回落本地」。侧车直接跑 Python 原版
// typo_generator.py，本地实现保留为侧车不可用时的兜底，二者算法一致。
type TypoEngine interface {
	// CreateTypoSentences 批量返回注入结果，顺序与输入一致
	CreateTypoSentences(ctx context.Context, sentences []string, cfg TypoConfig) ([]TypoResult, error)
}

// TypoResult 单句的注入结果
type TypoResult struct {
	Text       string
	Correction string
}

// applyTypoPostProcess 对齐 utils._apply_response_post_processing
//
// 成批调用注入后端（侧车一次 HTTP 拿全部句子），再逐句套用 Python 的
// 「50% 概率改用错字 + 可选引用纠正」判定；随机数仍取自调用方 rng，
// 保证与 Go 侧断句共用同一条随机流。
func applyTypoPostProcess(ctx context.Context, pieces []sentencePiece, cfg TypoConfig, enable bool, rng *rand.Rand, engine TypoEngine) []Segment {
	enabled := enable && cfg.Enable
	var typoed []TypoResult
	if enabled {
		sentences := make([]string, len(pieces))
		for index, piece := range pieces {
			sentences[index] = piece.content
		}
		results, err := createTypoSentences(ctx, engine, sentences, cfg)
		if err != nil {
			// 数据内嵌/侧车可用时不应失败；失败按 MiaBot「无生成器」分支继续，不伪造错字
			zap.L().Error("生成错别字失败，本次跳过错别字", zap.Error(err))
		} else {
			typoed = results
		}
	}

	segments := make([]Segment, 0, len(pieces)*2)
	for index, piece := range pieces {
		if typoed == nil {
			segments = append(segments, Segment{Text: piece.content, Separator: piece.separator})
			continue
		}
		result := typoed[index]
		if result.Correction == "" {
			segments = append(segments, Segment{Text: result.Text, Separator: piece.separator})
			continue
		}
		if rng.Float64() < 0.5 {
			quotePrevious := cfg.EnableCorrectionQuote && rng.Float64() < cfg.CorrectionQuoteProbability
			segments = append(segments, Segment{Text: result.Text, Separator: piece.separator})
			segments = append(segments, Segment{
				Text:          result.Correction,
				QuotePrevious: quotePrevious,
				IsCorrection:  true,
			})
			continue
		}
		segments = append(segments, Segment{Text: piece.content, Separator: piece.separator})
	}
	return segments
}

// createTypoSentences 按后端优先级生成错别字
//
// engine 为 nil 时先试 Python 兼容侧车（默认全局配置），不可用或失败则回落
// 本地移植实现；显式注入 engine 时只走该后端，失败直接返回错误，便于测试。
func createTypoSentences(ctx context.Context, engine TypoEngine, sentences []string, cfg TypoConfig) ([]TypoResult, error) {
	if len(sentences) == 0 {
		return nil, nil
	}
	if engine != nil {
		return engine.CreateTypoSentences(ctx, sentences, cfg)
	}
	if sidecar := defaultTypoEngine(); sidecar != nil {
		if results, err := sidecar.CreateTypoSentences(ctx, sentences, cfg); err == nil {
			return results, nil
		} else {
			zap.L().Warn("Python 兼容侧车不可用，回落到本地错别字实现", zap.Error(err))
		}
	}
	return localTypoSentences(sentences, cfg)
}

// defaultTypoEngine 进程级侧车客户端；未配置 MAIBOT_COMPAT_URL 时为 nil
func defaultTypoEngine() TypoEngine {
	client := pythoncompat.DefaultClient()
	if !client.Configured() {
		return nil
	}
	return sidecarTypoEngine{client: client}
}

// sidecarTypoEngine 把 pythoncompat.Client 适配成 TypoEngine
type sidecarTypoEngine struct {
	client *pythoncompat.Client
}

func (e sidecarTypoEngine) CreateTypoSentences(ctx context.Context, sentences []string, cfg TypoConfig) ([]TypoResult, error) {
	results, err := e.client.CreateTypoSentences(ctx, sentences, pythoncompat.TypoConfig{
		ErrorRate:       cfg.ErrorRate,
		MinFreq:         cfg.MinFreq,
		ToneErrorRate:   cfg.ToneErrorRate,
		WordReplaceRate: cfg.WordReplaceRate,
	}, nil)
	if err != nil {
		return nil, err
	}
	out := make([]TypoResult, len(results))
	for index, item := range results {
		out[index] = TypoResult{Text: item.Text, Correction: item.Correction}
	}
	return out, nil
}

// localTypoEngine 本地移植实现的适配器，供故障回落与对照测试使用
type localTypoEngine struct {
	rng *rand.Rand
}

func (e localTypoEngine) CreateTypoSentences(_ context.Context, sentences []string, cfg TypoConfig) ([]TypoResult, error) {
	return localTypoSentencesWithRNG(sentences, cfg, e.rng)
}

// localTypoSentences 用进程级随机源跑本地实现
func localTypoSentences(sentences []string, cfg TypoConfig) ([]TypoResult, error) {
	return localTypoSentencesWithRNG(sentences, cfg, nil)
}

func localTypoSentencesWithRNG(sentences []string, cfg TypoConfig, rng *rand.Rand) ([]TypoResult, error) {
	generator, err := newTypoGenerator(cfg, rng)
	if err != nil {
		return nil, err
	}
	out := make([]TypoResult, len(sentences))
	for index, sentence := range sentences {
		text, correction := generator.createTypoSentence(sentence)
		out[index] = TypoResult{Text: text, Correction: correction}
	}
	return out, nil
}

// NormalizeTypoSentence 去掉段尾逗号分隔符逻辑，供 LLM 断句路径使用
func NormalizeTypoSentence(sentence string) (content, separator string) {
	content = strings.TrimRight(sentence, "，, \t\r\n")
	if content == "" {
		return sentence, ""
	}
	sepRunes := []rune(sentence[len(content):])
	separator = string(sepRunes)
	if strings.ContainsAny(separator, "，,") && strings.TrimSpace(content) != "" {
		return content, separator
	}
	return sentence, ""
}

func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}
