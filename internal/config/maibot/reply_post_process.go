package maibot

import "fmt"

// ReplyPostProcessSettings 是 bot_config 中回复后处理三个子表的合并视图
// 默认值逐项对齐 official_configs.py：ResponsePostProcessConfig / ChineseTypoConfig / ResponseSplitterConfig
type ReplyPostProcessSettings struct {
	Enable      bool
	TypingSpeed float64

	SplitterMode            string
	SplitterEnable          bool
	MaxLength               int
	MaxSentenceNum          int
	MaxSplitNum             int
	EnableKaomojiProtection bool
	EnableOverflowReturnAll bool

	TypoEnable                     bool
	TypoEnableCorrectionQuote      bool
	TypoCorrectionQuoteProbability float64
	TypoErrorRate                  float64
	TypoMinFreq                    int
	TypoToneErrorRate              float64
	TypoWordReplaceRate            float64
}

// DefaultReplyPostProcessSettings 对齐 MaiBot 官方默认配置
func DefaultReplyPostProcessSettings() ReplyPostProcessSettings {
	return ReplyPostProcessSettings{
		Enable:      true,
		TypingSpeed: 1.0,

		SplitterMode:            "rule",
		SplitterEnable:          true,
		MaxLength:               512,
		MaxSentenceNum:          8,
		MaxSplitNum:             3,
		EnableKaomojiProtection: false,
		EnableOverflowReturnAll: false,

		TypoEnable:                     true,
		TypoEnableCorrectionQuote:      true,
		TypoCorrectionQuoteProbability: 1.0,
		TypoErrorRate:                  0.01,
		TypoMinFreq:                    9,
		TypoToneErrorRate:              0.1,
		TypoWordReplaceRate:            0.006,
	}
}

// ReplyPostProcessSettings 合并 TOML 显式配置与 pydantic 默认值
// 逐键判断存在性：段存在但字段缺失时仍取默认值，与 pydantic 行为一致
func (b BotConfigFile) ReplyPostProcessSettings() ReplyPostProcessSettings {
	s := DefaultReplyPostProcessSettings()
	m := b.LoadMeta

	if hasKey(m.PostProcessKeys, "enable_response_post_process") {
		s.Enable = b.ResponsePostProcess.EnableResponsePostProcess
	}
	if hasKey(m.PostProcessKeys, "typing_speed") {
		s.TypingSpeed = b.ResponsePostProcess.TypingSpeed
	}

	if hasKey(m.ResponseSplitterKeys, "mode") {
		s.SplitterMode = b.ResponseSplitter.Mode
	}
	if hasKey(m.ResponseSplitterKeys, "enable") {
		s.SplitterEnable = b.ResponseSplitter.Enable
	}
	if hasKey(m.ResponseSplitterKeys, "max_length") {
		s.MaxLength = b.ResponseSplitter.MaxLength
	}
	if hasKey(m.ResponseSplitterKeys, "max_sentence_num") {
		s.MaxSentenceNum = b.ResponseSplitter.MaxSentenceNum
	}
	if hasKey(m.ResponseSplitterKeys, "max_split_num") {
		s.MaxSplitNum = b.ResponseSplitter.MaxSplitNum
	}
	if hasKey(m.ResponseSplitterKeys, "enable_kaomoji_protection") {
		s.EnableKaomojiProtection = b.ResponseSplitter.EnableKaomojiProtection
	}
	if hasKey(m.ResponseSplitterKeys, "enable_overflow_return_all") {
		s.EnableOverflowReturnAll = b.ResponseSplitter.EnableOverflowReturnAll
	}

	if hasKey(m.ChineseTypoKeys, "enable") {
		s.TypoEnable = b.ChineseTypo.Enable
	}
	if hasKey(m.ChineseTypoKeys, "enable_correction_quote") {
		s.TypoEnableCorrectionQuote = b.ChineseTypo.EnableCorrectionQuote
	}
	if hasKey(m.ChineseTypoKeys, "correction_quote_probability") {
		s.TypoCorrectionQuoteProbability = b.ChineseTypo.CorrectionQuoteProbability
	}
	if hasKey(m.ChineseTypoKeys, "error_rate") {
		s.TypoErrorRate = b.ChineseTypo.ErrorRate
	}
	if hasKey(m.ChineseTypoKeys, "min_freq") {
		s.TypoMinFreq = b.ChineseTypo.MinFreq
	}
	if hasKey(m.ChineseTypoKeys, "tone_error_rate") {
		s.TypoToneErrorRate = b.ChineseTypo.ToneErrorRate
	}
	if hasKey(m.ChineseTypoKeys, "word_replace_rate") {
		s.TypoWordReplaceRate = b.ChineseTypo.WordReplaceRate
	}
	return s
}

// ValidateReplyPostProcess 按 pydantic 字段约束校验；MaiBot 在配置非法时无法启动
func ValidateReplyPostProcess(b BotConfigFile) error {
	s := b.ReplyPostProcessSettings()
	if s.TypingSpeed < 0 || s.TypingSpeed > 2 {
		return fmt.Errorf("response_post_process.typing_speed 必须在 0 到 2 之间")
	}
	if s.SplitterMode != "rule" && s.SplitterMode != "llm" {
		return fmt.Errorf("response_splitter.mode 必须是 rule 或 llm")
	}
	if s.MaxSplitNum < 1 {
		return fmt.Errorf("response_splitter.max_split_num 必须大于等于 1")
	}
	if s.TypoCorrectionQuoteProbability < 0 || s.TypoCorrectionQuoteProbability > 1 {
		return fmt.Errorf("chinese_typo.correction_quote_probability 必须在 0 到 1 之间")
	}
	if s.TypoErrorRate < 0 || s.TypoErrorRate > 1 {
		return fmt.Errorf("chinese_typo.error_rate 必须在 0 到 1 之间")
	}
	if s.TypoToneErrorRate < 0 || s.TypoToneErrorRate > 1 {
		return fmt.Errorf("chinese_typo.tone_error_rate 必须在 0 到 1 之间")
	}
	if s.TypoWordReplaceRate < 0 || s.TypoWordReplaceRate > 1 {
		return fmt.Errorf("chinese_typo.word_replace_rate 必须在 0 到 1 之间")
	}
	return nil
}
