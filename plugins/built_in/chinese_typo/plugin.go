package chinesetypo

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"strings"

	"MaiGo/internal/chat/replyer/jiebacut"
	"MaiGo/internal/plugin_runtime/hooks"
)

const typoHook = "inject_chinese_typos"
const keywordHook = "inject_keyword_reactions"

type Hook struct {
	Event, Name, Description string
	TimeoutMS                int64
}

func HookDeclarations() []Hook {
	return []Hook{
		{Event: "maisaka.reply.after_split", Name: typoHook},
		{Event: "maisaka.replyer.before_request", Name: keywordHook, TimeoutMS: 3000,
			Description: "最新消息命中关键词或正则规则时，把预设反应提示追加到回复要求"},
	}
}

type Runtime struct {
	config Config
	rules  []reactionRule
}

func NewRuntime(config Config) *Runtime {
	return &Runtime{config: config, rules: compileRules(config.rules)}
}

func (r *Runtime) Handler(name string) hooks.HandlerFunc {
	if name == typoHook {
		return r.injectTypos
	}
	return r.injectReactions
}

func (r *Runtime) injectReactions(ctx context.Context, kwargs map[string]any) (hooks.HandlerResult, error) {
	result := hooks.HandlerResult{Success: true, Action: "continue"}
	text, _ := kwargs["match_text"].(string)
	if !r.config.Enabled || !r.config.reactionActive || trimSpace(text) == "" {
		return result, nil
	}
	var reactions []string
	for _, rule := range r.rules {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if match, ok := rule.match(ctx, text); ok {
			reactions = append(reactions, "- "+match)
		}
	}
	if len(reactions) == 0 {
		return result, nil
	}
	prompt := "【关键词反应】\n最新消息命中了预设反应规则，请在回复时优先参考以下要求：\n" + strings.Join(reactions, "\n")
	if extra, _ := kwargs["extra_prompt"].(string); trimSpace(extra) != "" {
		prompt = extra + "\n" + prompt
	}
	result.ModifiedKwargs = maps.Clone(kwargs)
	result.ModifiedKwargs["extra_prompt"] = prompt
	return result, nil
}

func (r *Runtime) injectTypos(ctx context.Context, kwargs map[string]any) (hooks.HandlerResult, error) {
	result := hooks.HandlerResult{Success: true, Action: "continue"}
	if !r.config.Enabled || !r.config.typo.enabled || kwargs["enable_chinese_typo"] != true {
		return result, nil
	}
	var segments []map[string]any
	switch input := kwargs["segments"].(type) {
	case []map[string]any:
		segments = input
	case []any:
		for _, item := range input {
			segment, ok := item.(map[string]any)
			if !ok {
				return result, fmt.Errorf("segments 必须为消息段列表")
			}
			segments = append(segments, segment)
		}
	default:
		return result, fmt.Errorf("segments 必须为消息段列表")
	}
	edited, err := r.ApplyTypos(ctx, segments, rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())))
	if err != nil {
		return result, err
	}
	result.ModifiedKwargs = maps.Clone(kwargs)
	result.ModifiedKwargs["segments"] = edited
	return result, nil
}

type replyPart struct{ text, separator string }

// ApplyTypos 对分段回复应用词库或规则模式。随机源由调用方持有。
func (r *Runtime) ApplyTypos(ctx context.Context, segments []map[string]any, random *rand.Rand) ([]any, error) {
	parts := make([]replyPart, len(segments))
	for i, segment := range segments {
		text, textOK := segment["text"].(string)
		separator, separatorOK := segment["separator"].(string)
		if !textOK || !separatorOK {
			return nil, fmt.Errorf("segments[%d] 的 text 和 separator 必须为字符串", i)
		}
		parts[i] = replyPart{text, separator}
	}
	var edit *dictionaryEdit
	var data *ruleData
	var tokenizer *jiebacut.Dictionary
	var err error
	settings := r.config.typo
	if settings.mode == "dictionary" {
		edit, err = dictionaryTypo(ctx, parts, settings.dictionaryChance, random)
	} else {
		data, err = loadRuleData()
		if err == nil {
			tokenizer, err = jiebacut.DefaultDictionary()
		}
	}
	if err != nil {
		return nil, err
	}
	output := make([]any, 0, len(segments)+1)
	for i, segment := range segments {
		text, correction := parts[i].text, ""
		if data != nil {
			text, correction, err = data.ruleTypo(ctx, tokenizer, text, settings, random)
			if err != nil {
				return nil, err
			}
			if correction != "" && random.Float64() >= 0.5 {
				text, correction = parts[i].text, ""
			}
		} else if edit != nil && edit.part == i {
			text = edit.text
			if random.Float64() < 1.0/3 {
				correction = edit.original
			}
		}
		updated := maps.Clone(segment)
		updated["text"] = text
		output = append(output, updated)
		if correction != "" {
			output = append(output, map[string]any{
				"text": correction, "separator": "", "is_correction": true,
				"quote_previous": settings.quoteCorrection && random.Float64() < settings.quoteChance,
			})
		}
	}
	return output, nil
}
