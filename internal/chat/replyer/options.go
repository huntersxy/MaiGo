package replyer

import (
	"context"
	"math/rand/v2"
)

// Config 对齐 MaiBot response_post_process / response_splitter / chinese_typo 默认值
type Config struct {
	EnablePostProcess bool
	TypingSpeed       float64

	Splitter SplitterConfig
	Typo     TypoConfig
}

// SplitterConfig 规则断句相关参数
type SplitterConfig struct {
	Enable                  bool
		Mode                    string // rule | llm；llm 走 pipeline/speak_pipeline
	MaxLength               int
	MaxSentenceNum          int
	MaxSplitNum             int
	EnableKaomojiProtection bool
	EnableOverflowReturnAll bool
}

// TypoConfig 错别字注入；完整行为见 typo_generator.py
type TypoConfig struct {
	Enable                     bool
	EnableCorrectionQuote      bool
	CorrectionQuoteProbability float64
	ErrorRate                  float64
	MinFreq                    int
	ToneErrorRate              float64
	WordReplaceRate            float64
}

// TimingConfig 单条消息的打字延迟参数
type TimingConfig struct {
	ChineseTimeSec float64
	EnglishTimeSec float64
	IsEmoji        bool
	IsImage        bool
}

// ExpressionRequest 表达方式选择入参（learners/habits 路径）
type ExpressionRequest struct {
	SessionID     string
	TargetMessage string
	ReplyReason   string
	// ReplyToolArgs Planner reply 工具参数，透传 expression_intent/reply_reference
	ReplyToolArgs map[string]any
}

// Options ApplyPipeline 可选参数；零值为「使用 DefaultConfig」
type Options struct {
	Config Config

	// 显式覆盖 Config 内开关（nil 表示跟随 Config）
	EnableSplitter  *bool
	EnableTypo      *bool
	SkipPostProcess bool

	BotNickname string
	// RNG 可选随机源；nil 时按全局随机种子新建（生产默认），测试须注入固定种子
	RNG *rand.Rand

	// Ctx 供 LLM 断句等外部调用使用；nil 时用 context.Background()
	Ctx context.Context
	// LLMSplitter LLM 语义断句器；mode=llm 时必填
	LLMSplitter LLMSplitterFunc

	Timing     TimingConfig
	Expression *ExpressionRequest
}

// DefaultConfig 对齐 official_configs.py 默认项
func DefaultConfig() Config {
	return Config{
		EnablePostProcess: true,
		TypingSpeed:       1.0,
		Splitter: SplitterConfig{
			Enable:                  true,
			Mode:                    "rule",
			MaxLength:               512,
			MaxSentenceNum:          8,
			MaxSplitNum:             3,
			EnableKaomojiProtection: false,
			EnableOverflowReturnAll: false,
		},
		Typo: TypoConfig{
			Enable:                     true,
			EnableCorrectionQuote:      true,
			CorrectionQuoteProbability: 1.0,
			ErrorRate:                  0.01,
			MinFreq:                    9,
			ToneErrorRate:              0.1,
			WordReplaceRate:            0.006,
		},
	}
}

func (o *Options) mergedConfig() Config {
	cfg := DefaultConfig()
	if o == nil {
		return cfg
	}
	c := o.Config
	if c != (Config{}) {
		cfg.EnablePostProcess = c.EnablePostProcess
		if c.TypingSpeed != 0 {
			cfg.TypingSpeed = c.TypingSpeed
		}
		if c.Splitter.Mode != "" {
			cfg.Splitter.Mode = c.Splitter.Mode
		}
		cfg.Splitter.Enable = c.Splitter.Enable
		if c.Splitter.MaxLength != 0 {
			cfg.Splitter.MaxLength = c.Splitter.MaxLength
		}
		if c.Splitter.MaxSentenceNum != 0 {
			cfg.Splitter.MaxSentenceNum = c.Splitter.MaxSentenceNum
		}
		if c.Splitter.MaxSplitNum != 0 {
			cfg.Splitter.MaxSplitNum = c.Splitter.MaxSplitNum
		}
		cfg.Splitter.EnableKaomojiProtection = c.Splitter.EnableKaomojiProtection
		cfg.Splitter.EnableOverflowReturnAll = c.Splitter.EnableOverflowReturnAll
		if c.Typo != (TypoConfig{}) {
			cfg.Typo = c.Typo
		}
	}
	return cfg
}

func (o *Options) rng() *rand.Rand {
	if o != nil && o.RNG != nil {
		return o.RNG
	}
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

func (o *Options) splitterEnabled(cfg Config) bool {
	if o != nil && o.EnableSplitter != nil {
		return *o.EnableSplitter
	}
	return cfg.Splitter.Enable
}

func (o *Options) typoEnabled(cfg Config) bool {
	if o != nil && o.EnableTypo != nil {
		return *o.EnableTypo
	}
	return cfg.Typo.Enable
}

func (o *Options) botNickname() string {
	if o != nil && o.BotNickname != "" {
		return o.BotNickname
	}
	return "麦麦"
}

func (o *Options) timingConfig() TimingConfig {
	if o == nil {
		return TimingConfig{ChineseTimeSec: 0.3, EnglishTimeSec: 0.15}
	}
	t := o.Timing
	if t.ChineseTimeSec == 0 {
		t.ChineseTimeSec = 0.3
	}
	if t.EnglishTimeSec == 0 {
		t.EnglishTimeSec = 0.15
	}
	return t
}

func (o *Options) context() context.Context {
	if o != nil && o.Ctx != nil {
		return o.Ctx
	}
	return context.Background()
}
