package pythoncompat

import (
	"context"
	"fmt"
)

// TypoConfig 对应 ChineseTypoGenerator 的构造参数
type TypoConfig struct {
	ErrorRate       float64 `json:"error_rate"`
	MinFreq         int     `json:"min_freq"`
	ToneErrorRate   float64 `json:"tone_error_rate"`
	WordReplaceRate float64 `json:"word_replace_rate"`
}

// TypoResult 单句结果：注入错字后的文本与随机纠正建议（可能为空）
type TypoResult struct {
	Text       string `json:"text"`
	Correction string `json:"correction"`
}

type typoRequest struct {
	Sentences []string   `json:"sentences"`
	Config    TypoConfig `json:"config"`
	Seed      *int64     `json:"seed,omitempty"`
}

type typoResponse struct {
	Sentences []struct {
		Text       string  `json:"text"`
		Correction *string `json:"correction"`
	} `json:"sentences"`
}

// CreateTypoSentences 调用 Python 原版 create_typo_sentence 批量注入错别字。
//
// seed 为 nil 时由 Python 侧按随机种子重置，与 Go 侧「每请求一个 RNG」的
// 调用形态一致；需要复现时传入固定 seed。
func (c *Client) CreateTypoSentences(ctx context.Context, sentences []string, cfg TypoConfig, seed *int64) ([]TypoResult, error) {
	if len(sentences) == 0 {
		return nil, nil
	}
	release, err := c.ensureRunning(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var response typoResponse
	if err := c.post(ctx, "/typo", typoRequest{Sentences: sentences, Config: cfg, Seed: seed}, &response); err != nil {
		return nil, err
	}
	if len(response.Sentences) != len(sentences) {
		return nil, fmt.Errorf(
			"pythoncompat: /typo 返回 %d 条，期望 %d 条",
			len(response.Sentences), len(sentences),
		)
	}
	out := make([]TypoResult, len(response.Sentences))
	for index, item := range response.Sentences {
		result := TypoResult{Text: item.Text}
		if item.Correction != nil {
			result.Correction = *item.Correction
		}
		out[index] = result
	}
	return out, nil
}
