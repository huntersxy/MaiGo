package maibot

import "fmt"

// ReplyPostProcessSettings 是 bot_config 中回复后处理子表的合并视图
// 默认值逐项对齐 official_configs.py：ResponsePostProcessConfig / ResponseSplitterConfig
type ReplyPostProcessSettings struct {
	Enable               bool
	RemoveBracketContent bool
	TypingSpeed          float64

	SplitterMode            string
	SplitterEnable          bool
	MaxLength               int
	MaxSentenceNum          int
	MaxSplitNum             int
	EnableKaomojiProtection bool
	EnableOverflowReturnAll bool
}

// DefaultReplyPostProcessSettings 对齐 MaiBot 官方默认配置
func DefaultReplyPostProcessSettings() ReplyPostProcessSettings {
	return ReplyPostProcessSettings{
		Enable:               true,
		RemoveBracketContent: true,
		TypingSpeed:          1.0,

		SplitterMode:            "rule",
		SplitterEnable:          true,
		MaxLength:               512,
		MaxSentenceNum:          8,
		MaxSplitNum:             3,
		EnableKaomojiProtection: false,
		EnableOverflowReturnAll: false,
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
	if hasKey(m.PostProcessKeys, "remove_bracket_content") {
		s.RemoveBracketContent = b.ResponsePostProcess.RemoveBracketContent
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
	return nil
}
