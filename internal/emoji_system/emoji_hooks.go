package emojisystem

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"MaiGo/internal/plugin_runtime/hooks"

	"go.uber.org/zap"
)

const (
	hookEmojiRegisterAfterBuildDescription = "emoji.register.after_build_description"
	hookEmojiMaisakaBeforeSelect           = "emoji.maisaka.before_select"
	hookEmojiMaisakaAfterSelect            = "emoji.maisaka.after_select"

	defaultEmojiSelectAbortMessage = "表情选择已被 Hook 中止。"
	defaultEmojiSendAbortMessage   = "表情发送已被 Hook 中止。"

	defaultEmojiSampleSize = 20
)

// DefaultSampleSize 对齐 maisaka_tool 的 sample_size 默认值
const DefaultSampleSize = defaultEmojiSampleSize

// SelectionAbortedError Hook 中止表情选择/发送；Message 对齐 maisaka_tool 的 abort_message
type SelectionAbortedError struct {
	Message     string
	HookName    string
	HandlerName string
	Reason      string
}

func (e *SelectionAbortedError) Error() string {
	if e == nil {
		return defaultEmojiSelectAbortMessage
	}
	if msg := stringsTrim(e.Message); msg != "" {
		return msg
	}
	return defaultEmojiSelectAbortMessage
}

// AbortDetails 返回中止来源，供工具结果反馈给 Planner
func (e *SelectionAbortedError) AbortDetails() map[string]any {
	if e == nil {
		return map[string]any{"hook_name": "", "handler_name": "", "reason": ""}
	}
	return map[string]any{"hook_name": e.HookName, "handler_name": e.HandlerName, "reason": e.Reason}
}

// logEmojiHookResult 记录 Hook 执行期错误（对齐 MaiBot logger.warning）
func logEmojiHookResult(hookName string, result hooks.DispatchResult) {
	for _, msg := range result.Errors {
		zap.L().Warn("表情 Hook 执行失败", zap.String("hook", hookName), zap.String("error", msg))
	}
}

// applyRegisterDescriptionHook 对齐 emoji_manager._build_emoji_description 的 Hook 段：
// abort 时放弃注册；否则读回 description 覆盖值（可能为空，由调用方拒绝）。
func applyRegisterDescriptionHook(ctx context.Context, mgr *Manager, emoji *Emoji, description, imageFormat string) (string, error) {
	result := hooks.EmitGlobal(ctx, hookEmojiRegisterAfterBuildDescription, map[string]any{
		"emoji":        mgr.SerializeForHook(emoji),
		"description":  description,
		"image_format": strings.ToLower(stringsTrim(imageFormat)),
	})
	logEmojiHookResult(hookEmojiRegisterAfterBuildDescription, result)
	if result.Aborted {
		return "", errDescriptionHookAborted
	}
	return hookStringValue(result.Kwargs, "description", description), nil
}

// emojiImageFormat 对齐 ImageDataModel.image_format：小写文件扩展名
func emojiImageFormat(fileName string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(stringsTrim(fileName))), ".")
}

// selectionState 保存 emoji.maisaka.* Hook 之间可被读写回的可变入参
type selectionState struct {
	params       SelectParams
	emotion      string
	reasoning    string
	contextTexts []string
	sampleSize   int
}

func newSelectionState(params SelectParams) selectionState {
	state := selectionState{
		params:       params,
		emotion:      stringsTrim(params.RequestedEmotion),
		reasoning:    stringsTrim(params.Reasoning),
		contextTexts: normalizeContextTexts(params.ContextTexts),
		sampleSize:   params.SampleSize,
	}
	if state.sampleSize <= 0 {
		state.sampleSize = defaultEmojiSampleSize
	}
	return state
}

func (s *selectionState) streamID() string {
	return stringsTrim(s.params.StreamID)
}

func (s *selectionState) kwargs(extra map[string]any) map[string]any {
	out := map[string]any{
		"stream_id":         s.streamID(),
		"requested_emotion": s.emotion,
		"reasoning":         s.reasoning,
		"context_texts":     stringSliceValues(s.contextTexts),
		"sample_size":       s.sampleSize,
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

// applyHookResult 读回 requested_emotion / reasoning / context_texts / sample_size
func (s *selectionState) applyHookResult(kwargs map[string]any) {
	if kwargs == nil {
		return
	}
	s.emotion = hookStringValue(kwargs, "requested_emotion", s.emotion)
	s.reasoning = hookStringValue(kwargs, "reasoning", s.reasoning)
	if texts, ok := hookStringSlice(kwargs, "context_texts"); ok {
		s.contextTexts = texts
	}
	s.sampleSize = hookPositiveInt(kwargs, "sample_size", s.sampleSize)
}

// resolveEmotion 保留 Hook 未给出情绪时的兜底：取最后一条上下文文本
func (s *selectionState) resolveEmotion() string {
	if s.emotion != "" {
		return s.emotion
	}
	if len(s.contextTexts) > 0 {
		return stringsTrim(s.contextTexts[len(s.contextTexts)-1])
	}
	return ""
}

// resolveSelectedEmoji 对齐 maisaka_tool._resolve_selected_emoji：hash 优先，其次 dict/字符串
func resolveSelectedEmoji(ctx context.Context, mgr *Manager, kwargs map[string]any) *Emoji {
	if kwargs == nil || mgr == nil {
		return nil
	}
	hash := hookStringValue(kwargs, "selected_emoji_hash", "")
	if hash == "" {
		hash = emojiHashFromHookValue(kwargs["selected_emoji"])
	}
	if hash == "" {
		return nil
	}
	emoji, err := mgr.GetByHash(ctx, hash)
	if err != nil || emoji == nil {
		return nil
	}
	return emoji
}

func emojiHashFromHookValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return stringsTrim(v)
	case map[string]any:
		for _, key := range []string{"file_hash", "hash"} {
			if text, ok := v[key].(string); ok && stringsTrim(text) != "" {
				return stringsTrim(text)
			}
		}
	}
	return ""
}

func hookAbortMessage(kwargs map[string]any, fallback string) string {
	message := hookStringValue(kwargs, "abort_message", fallback)
	if message == "" {
		return fallback
	}
	return message
}

// hookStringValue 对齐 Python str(value or "").strip()
func hookStringValue(kwargs map[string]any, key, fallback string) string {
	if kwargs == nil {
		return fallback
	}
	raw, ok := kwargs[key]
	if !ok {
		return fallback
	}
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return stringsTrim(text)
	}
	return stringsTrim(fmt.Sprintf("%v", raw))
}

// hookStringSlice 仅在值为列表时覆盖（对齐 isinstance(kwargs.get("context_texts"), list)）
func hookStringSlice(kwargs map[string]any, key string) ([]string, bool) {
	if kwargs == nil {
		return nil, false
	}
	raw, ok := kwargs[key]
	if !ok {
		return nil, false
	}
	switch v := raw.(type) {
	case []string:
		return normalizeContextTexts(v), true
	case []any:
		items := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok {
				items = append(items, text)
			}
		}
		return normalizeContextTexts(items), true
	}
	return nil, false
}

// hookPositiveInt 对齐 _coerce_positive_int：int() 失败或非正数时回退默认值
func hookPositiveInt(kwargs map[string]any, key string, fallback int) int {
	if kwargs == nil {
		return fallback
	}
	raw, ok := kwargs[key]
	if !ok {
		return fallback
	}
	value, ok := hookScriptInt(raw)
	if !ok || value <= 0 {
		return fallback
	}
	return value
}

func hookScriptInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int8:
		return int(v), true
	case int16:
		return int(v), true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case uint:
		return int(v), true
	case uint8:
		return int(v), true
	case uint16:
		return int(v), true
	case uint32:
		return int(v), true
	case uint64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		text := stringsTrim(v)
		if text == "" {
			return 0, false
		}
		if value, err := strconv.Atoi(text); err == nil {
			return value, true
		}
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return int(value), true
		}
	}
	return 0, false
}

func normalizeContextTexts(texts []string) []string {
	if len(texts) == 0 {
		return nil
	}
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		if trimmed := stringsTrim(text); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func stringSliceValues(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}
