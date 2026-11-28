package webuiapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models/model_client"
)

type speechPreviewRequest struct {
	ModelIdentifier string  `json:"model_identifier"`
	VoiceID         string  `json:"voice_id"`
	APIProvider     *string `json:"api_provider"`
	DraftAPIKey     *string `json:"draft_api_key"`
}

// handleModelsPreviewSpeech 对应 POST /api/webui/models/preview-speech
func (d Deps) handleModelsPreviewSpeech(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	var request speechPreviewRequest
	if err := sonic.Unmarshal(readBody(r), &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求体不是合法 JSON: " + err.Error()})
		return
	}
	hasProvider := request.APIProvider != nil
	hasDraft := request.DraftAPIKey != nil
	if hasProvider == hasDraft {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请选择已有提供商，或填写新连接的 API Key"})
		return
	}
	provider := maibotcfg.APIProvider{}
	if hasDraft {
		key := ""
		if request.DraftAPIKey != nil {
			key = strings.TrimSpace(*request.DraftAPIKey)
		}
		if key == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请填写 API Key"})
			return
		}
		officialURL, err := previewSpeechOfficialURL(strings.TrimSpace(request.ModelIdentifier))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		provider = maibotcfg.APIProvider{Name: "语音试听", BaseUrl: officialURL, ApiKey: key, ClientType: "openai"}
	} else {
		raw, ok := d.modelProviderByName(strings.TrimSpace(*request.APIProvider))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "找不到所选提供商"})
			return
		}
		encoded, _ := sonic.MarshalString(raw)
		_ = sonic.UnmarshalString(encoded, &provider)
	}
	cfg := model_client.ModelConfig{
		ClientType: strings.TrimSpace(provider.ClientType),
		APIKey:     strings.TrimSpace(provider.ApiKey),
		BaseURL:    strings.TrimSpace(provider.BaseUrl),
		Model:      strings.TrimSpace(request.ModelIdentifier),
	}
	synth, err := model_client.NewSpeechSynthesizer(cfg)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	runCtx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	audio, err := synth.SynthesizeSpeech(runCtx, "你好，我是麦麦，很高兴和你聊天。", strings.TrimSpace(request.VoiceID))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", audio.MediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(audio.Data)
}

func previewSpeechOfficialURL(model string) (string, error) {
	switch model {
	case "deepseek-tts":
		return "https://api.deepseek.com", nil
	}
	if _, ok := model_client.StepTTSDefaultVoices[model]; ok {
		return "https://api.stepfun.com/v1", nil
	}
	return "", fmt.Errorf("插件模板请使用已保存的提供商连接")
}
