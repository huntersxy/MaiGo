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

// AttachReplyVoice 整段正文合成一次语音并发送，对齐 Python attach_reply_voice
func AttachReplyVoice(ctx context.Context, tc *ToolContext, content string, mode VoiceMode) error {
	if tc == nil {
		return fmt.Errorf("缺少工具上下文")
	}
	if tc.SynthesizeReplySpeech == nil {
		return fmt.Errorf("语音合成功能未接入")
	}
	if tc.SendVoiceBinary == nil {
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
	if mode == VoiceModeWithText {
		return tc.SendVoiceBinary(ctx, audio)
	}
	// voice_only：正文已通过文本路径发送时不重复；无正文时单独发送语音
	return tc.SendVoiceBinary(ctx, audio)
}
