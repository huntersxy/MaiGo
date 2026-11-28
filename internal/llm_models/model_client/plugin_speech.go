package model_client

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
)

type pluginSpeechSynthesizer struct {
	model *pluginModel
}

func newPluginSpeechSynthesizer(cfg ModelConfig) (SpeechSynthesizer, error) {
	pluginID, invoker, err := resolvePluginProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &pluginSpeechSynthesizer{
		model: &pluginModel{cfg: cfg, pluginID: pluginID, invoker: invoker},
	}, nil
}

func (s *pluginSpeechSynthesizer) SynthesizeSpeech(ctx context.Context, text string, voiceID string) (SpeechAudio, error) {
	if s == nil || s.model == nil {
		return SpeechAudio{}, fmt.Errorf("插件语音合成客户端未初始化")
	}
	preset := lookupPluginTTSPreset(s.model.cfg.ClientType, s.model.cfg.Model)
	if preset == nil {
		return SpeechAudio{}, fmt.Errorf("%s 没有声明语音合成模型 %s", s.model.cfg.ClientType, s.model.cfg.Model)
	}
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		for key := range preset.Voices {
			voiceID = key
			break
		}
	}
	if len(preset.Voices) > 0 {
		if _, ok := preset.Voices[voiceID]; !ok {
			return SpeechAudio{}, fmt.Errorf("所选音色不在插件声明的音色列表中")
		}
	}
	request := map[string]any{
		"text":             strings.TrimSpace(text),
		"model_identifier": s.model.cfg.Model,
		"voice_id":         voiceID,
		"model_info":       s.model.modelInfoSnapshot(),
		"api_provider":     s.model.apiProviderSnapshot(true),
	}
	result, err := s.model.invoke(ctx, pluginOperationAudioSynthesis, request)
	if err != nil {
		return SpeechAudio{}, err
	}
	mime := strings.TrimSpace(pluginFirstString(result, "media_type"))
	encoded := strings.TrimSpace(pluginFirstString(result, "audio_base64"))
	switch mime {
	case "audio/wav", "audio/mpeg", "audio/ogg", "audio/flac", "audio/mp4", "audio/webm":
	default:
		return SpeechAudio{}, &ParseError{Message: "插件返回了不支持的音频 media_type"}
	}
	if encoded == "" {
		return SpeechAudio{}, &ParseError{Message: "插件必须返回非空 audio_base64"}
	}
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return SpeechAudio{}, &ParseError{Message: "插件 audio_base64 解码失败"}
	}
	return SpeechAudio{Data: audio, MediaType: mime}, nil
}

var _ SpeechSynthesizer = (*pluginSpeechSynthesizer)(nil)
