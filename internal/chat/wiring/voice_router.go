package chatwiring

// 基于 llm.Router 的语音转写适配器
// 主链只注入路由器，不提前解析模型配置；每次转写按任务路由创建客户端，
// 配置/模型缺失时把真实错误交给 VoicePort，避免启动期静默降级

import (
	"context"
	"errors"

	"MaiGo/internal/llm_models"
)

// RouterAudioTranscriber 把 llm.Router 的语音任务路由适配成 AudioTranscriber
type RouterAudioTranscriber struct {
	Router *llm.Router
}

// NewRouterAudioTranscriber 构造任务路由转写器
func NewRouterAudioTranscriber(router *llm.Router) *RouterAudioTranscriber {
	return &RouterAudioTranscriber{Router: router}
}

// Transcribe 对齐 MaiBot voice 任务的音频转写请求
func (t *RouterAudioTranscriber) Transcribe(ctx context.Context, req llm.AudioTranscriptionRequest) (llm.AudioTranscriptionResult, error) {
	if t == nil || t.Router == nil {
		return llm.AudioTranscriptionResult{}, errors.New("LLM 路由器未配置")
	}
	_, result, err := t.Router.TranscribeAudioForTask(ctx, llm.TaskVoice, "audio", "", req)
	if err != nil {
		return llm.AudioTranscriptionResult{}, err
	}
	return result, nil
}

var _ llm.AudioTranscriber = (*RouterAudioTranscriber)(nil)
