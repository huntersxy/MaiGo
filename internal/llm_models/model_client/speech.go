package model_client

import (
	"context"
	"fmt"
)

// SpeechAudio 对齐 MaiBot base_client.SpeechAudio
type SpeechAudio struct {
	Data      []byte
	MediaType string
}

// SpeechSynthesizer 语音合成客户端
type SpeechSynthesizer interface {
	SynthesizeSpeech(ctx context.Context, text string, voiceID string) (SpeechAudio, error)
}

const pluginOperationAudioSynthesis = "audio_synthesis"

// NewSpeechSynthesizer 按 client_type 创建合成客户端
func NewSpeechSynthesizer(cfg ModelConfig) (SpeechSynthesizer, error) {
	if isChatCompletionsClientType(cfg.ClientType) || isOpenAIResponsesClientType(cfg.ClientType) {
		client, err := newChatCompletionsClient(cfg)
		if err != nil {
			return nil, err
		}
		return &openAISpeechSynthesizer{client: client}, nil
	}
	if !isOpenAICompatClientType(cfg.ClientType) {
		return newPluginSpeechSynthesizer(cfg)
	}
	return nil, fmt.Errorf("client_type %q 尚未实现语音合成", cfg.ClientType)
}
