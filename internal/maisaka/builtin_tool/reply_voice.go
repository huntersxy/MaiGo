package tools

import (
	"context"
	"fmt"
	"strings"

	"MaiGo/internal/cli"
	maibotcfg "MaiGo/internal/config/maibot"
)

// VoiceMode 语音附件模式，对齐 Python reply_voice.VoiceMode
type VoiceMode string

const (
	VoiceModeOnly     VoiceMode = "voice_only"
	VoiceModeWithText VoiceMode = "with_text"
)

var voiceModes = []VoiceMode{VoiceModeOnly, VoiceModeWithText}

// VoiceAttachmentSchema 返回 attach_voice 的 JSON Schema；未配置 TTS 或 CLI 平台时返回 nil
func VoiceAttachmentSchema(platform string, model maibotcfg.ModelConfigFile) map[string]any {
	if platform == cli.MaisakaCLIPlatformName || len(model.ModelTaskConfig.Tts.ModelList) == 0 {
		return nil
	}
	enum := make([]any, 0, len(voiceModes))
	for _, mode := range voiceModes {
		enum = append(enum, string(mode))
	}
	return map[string]any{
		"type":        "string",
		"enum":        enum,
		"description": "需要用语音回复时选择 voice_only 朗读整段正文；需要同时发送文字时选择 with_text。平时省略。",
	}
}

func parseVoiceMode(raw any) (VoiceMode, error) {
	text := strings.TrimSpace(fmt.Sprint(raw))
	if text == "" {
		return "", fmt.Errorf("语音附件模式为空")
	}
	for _, mode := range voiceModes {
		if text == string(mode) {
			return mode, nil
		}
	}
	return "", fmt.Errorf("当前回复不可使用指定的语音附件模式")
}

func resolveAttachVoiceMode(ctx context.Context, raw string) (VoiceMode, error) {
	mode := strings.TrimSpace(raw)
	if mode == "" {
		return "", nil
	}
	tc := GetToolContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("生成语音附件失败：缺少工具上下文")
	}
	file, ok := maibotcfg.ModelConfigFile{}, false
	if tc.ModelConfigSnapshot != nil {
		file, ok = tc.ModelConfigSnapshot()
	}
	if schema := VoiceAttachmentSchema(tc.Platform, file); !ok || schema == nil {
		return "", fmt.Errorf("当前回复不可使用语音附件")
	}
	return parseVoiceMode(mode)
}

func sendReplyVoiceOnly(ctx context.Context, content string, replyTo int64) error {
	tc := GetToolContext(ctx)
	if tc == nil || tc.SynthesizeReplySpeech == nil || tc.SendVoiceBinary == nil {
		return fmt.Errorf("语音发送端口未接入")
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return fmt.Errorf("没有可朗读的回复正文")
	}
	audio, err := tc.SynthesizeReplySpeech(ctx, text)
	if err != nil {
		return err
	}
	return tc.SendVoiceBinary(WithReplyAnchor(ctx, replyTo), audio)
}

func sendReplyVoiceWithText(ctx context.Context, content string) error {
	tc := GetToolContext(ctx)
	if tc == nil || tc.SynthesizeReplySpeech == nil || tc.SendVoiceBinary == nil {
		return fmt.Errorf("语音发送端口未接入")
	}
	text := strings.TrimSpace(content)
	if text == "" {
		return fmt.Errorf("没有可朗读的回复正文")
	}
	audio, err := tc.SynthesizeReplySpeech(ctx, text)
	if err != nil {
		return err
	}
	return tc.SendVoiceBinary(ctx, audio)
}
