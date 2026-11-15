package emojisystem

import (
	"context"
	"math/rand"

	"MaiGo/internal/plugin_runtime/hooks"
)

// SelectParams Maisaka send_emoji 选择链入参
type SelectParams struct {
	StreamID         string
	RequestedEmotion string
	Reasoning        string
	ContextTexts     []string
	SampleSize       int
}

// SelectResult 选择结果
type SelectResult struct {
	Emoji            *Emoji
	MatchedEmotion   string
	RequestedEmotion string
}

// EmojiPicker 对齐 maisaka_tool 注入的 emoji_selector 回调
type EmojiPicker func(ctx context.Context, mgr *Manager, emotion, reasoning string, contextTexts []string, sampleSize int) (*SelectResult, error)

// DefaultSelector 对齐 maisaka_tool 默认 selector；带 emoji.maisaka.* Hook 的完整流程
func DefaultSelector(ctx context.Context, mgr *Manager, params SelectParams) (*SelectResult, error) {
	return SelectWithPicker(ctx, mgr, params, defaultPicker)
}

// SelectWithPicker 执行 maisaka_tool.send_emoji_for_maisaka 的完整流程：before_select Hook → picker → after_select Hook
func SelectWithPicker(ctx context.Context, mgr *Manager, params SelectParams, picker EmojiPicker) (*SelectResult, error) {
	if mgr == nil {
		return nil, errMemoryUnavailable
	}
	if picker == nil {
		picker = defaultPicker
	}
	state := newSelectionState(params)

	beforeResult := hooks.EmitGlobal(ctx, hookEmojiMaisakaBeforeSelect, state.kwargs(map[string]any{
		"abort_message": defaultEmojiSelectAbortMessage,
	}))
	logEmojiHookResult(hookEmojiMaisakaBeforeSelect, beforeResult)
	if beforeResult.Aborted {
		return nil, &SelectionAbortedError{Message: hookAbortMessage(beforeResult.Kwargs, defaultEmojiSelectAbortMessage)}
	}
	state.applyHookResult(beforeResult.Kwargs)

	selected, err := picker(ctx, mgr, state.resolveEmotion(), state.reasoning, state.contextTexts, state.sampleSize)
	if err != nil {
		return nil, err
	}
	if selected == nil {
		selected = &SelectResult{}
	}
	matchedEmotion := selected.MatchedEmotion
	selectedEmoji := selected.Emoji

	afterResult := hooks.EmitGlobal(ctx, hookEmojiMaisakaAfterSelect, state.kwargs(map[string]any{
		"selected_emoji":      mgr.SerializeForHook(selectedEmoji),
		"selected_emoji_hash": emojiFileHash(selectedEmoji),
		"matched_emotion":     matchedEmotion,
		"abort_message":       defaultEmojiSendAbortMessage,
	}))
	logEmojiHookResult(hookEmojiMaisakaAfterSelect, afterResult)
	if afterResult.Aborted {
		return nil, &SelectionAbortedError{Message: hookAbortMessage(afterResult.Kwargs, defaultEmojiSendAbortMessage)}
	}

	requestedEmotion := hookStringValue(afterResult.Kwargs, "requested_emotion", state.emotion)
	matchedEmotion = hookStringValue(afterResult.Kwargs, "matched_emotion", matchedEmotion)
	if override := resolveSelectedEmoji(ctx, mgr, afterResult.Kwargs); override != nil {
		selectedEmoji = override
	}
	if selectedEmoji == nil {
		return nil, errNoEmojiAvailable
	}
	return &SelectResult{
		Emoji:            selectedEmoji,
		MatchedEmotion:   matchedEmotion,
		RequestedEmotion: requestedEmotion,
	}, nil
}

// defaultPicker 对齐 Maisaka 默认 selector：按情绪相似度选图，失败时从样本池随机
func defaultPicker(ctx context.Context, mgr *Manager, emotion, reasoning string, contextTexts []string, sampleSize int) (*SelectResult, error) {
	_ = ctx
	_ = reasoning
	_ = contextTexts
	emoji, matched, err := mgr.SelectForEmotion(emotion)
	if err != nil {
		limit := sampleSize
		if limit <= 0 {
			limit = defaultEmojiSampleSize
		}
		candidates := mgr.SampleCandidates(limit)
		if len(candidates) == 0 {
			return nil, err
		}
		pick := candidates[rand.Intn(len(candidates))]
		return &SelectResult{Emoji: &pick, MatchedEmotion: matched}, nil
	}
	return &SelectResult{Emoji: emoji, MatchedEmotion: matched}, nil
}

func emojiFileHash(emoji *Emoji) string {
	if emoji == nil {
		return ""
	}
	return stringsTrim(emoji.FileHash)
}
