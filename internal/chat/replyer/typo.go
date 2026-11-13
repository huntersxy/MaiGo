package replyer

import (
	"math/rand/v2"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"
)

// applyTypoPostProcess 对齐 utils._apply_response_post_processing
func applyTypoPostProcess(pieces []sentencePiece, cfg TypoConfig, enable bool, rng *rand.Rand) []Segment {
	var generator *typoGenerator
	if enable && cfg.Enable {
		gen, err := newTypoGenerator(cfg, rng)
		if err != nil {
			// 数据内嵌，加载失败属于构建/环境错误；按 MiaBot 无生成器分支继续（不伪造错字）
			zap.L().Error("加载错别字生成器失败，本次跳过错别字", zap.Error(err))
		} else {
			generator = gen
		}
	}

	segments := make([]Segment, 0, len(pieces)*2)
	for _, piece := range pieces {
		if generator == nil {
			segments = append(segments, Segment{Text: piece.content, Separator: piece.separator})
			continue
		}
		typoed, correction := generator.createTypoSentence(piece.content)
		if correction == "" {
			segments = append(segments, Segment{Text: typoed, Separator: piece.separator})
			continue
		}
		if rng.Float64() < 0.5 {
			quotePrevious := cfg.EnableCorrectionQuote && rng.Float64() < cfg.CorrectionQuoteProbability
			segments = append(segments, Segment{Text: typoed, Separator: piece.separator})
			segments = append(segments, Segment{
				Text:          correction,
				QuotePrevious: quotePrevious,
				IsCorrection:  true,
			})
			continue
		}
		segments = append(segments, Segment{Text: piece.content, Separator: piece.separator})
	}
	return segments
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
