package model_client

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// StepTTSDefaultVoices 对齐 MaiBot STEP_TTS_DEFAULT_VOICES
var StepTTSDefaultVoices = map[string]string{
	"stepaudio-3-tts":   "zixinnansheng",
	"stepaudio-2.5-tts": "zixinnansheng",
	"step-tts-2":        "zixinnansheng",
	"step-tts-mini":     "cixingnansheng",
}

type openAISpeechSynthesizer struct {
	client *chatCompletionsClient
}

func (s *openAISpeechSynthesizer) SynthesizeSpeech(ctx context.Context, text string, voiceID string) (SpeechAudio, error) {
	if s == nil || s.client == nil {
		return SpeechAudio{}, fmt.Errorf("OpenAI 语音合成客户端未初始化")
	}
	model := strings.TrimSpace(s.client.model)
	text = strings.TrimSpace(text)
	voiceID = strings.TrimSpace(voiceID)
	var path string
	var body map[string]any
	switch {
	case model == "deepseek-tts":
		if voiceID == "" {
			voiceID = "stella"
		}
		if voiceID != "stella" && voiceID != "tide" {
			return SpeechAudio{}, fmt.Errorf("未知的 DeepSeek TTS 音色：%s", voiceID)
		}
		path = "/tools/tts"
		body = map[string]any{"text": text, "voice_id": voiceID, "format": "wav"}
	case StepTTSDefaultVoices[model] != "":
		if voiceID == "" {
			voiceID = StepTTSDefaultVoices[model]
		}
		if utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 1000 {
			return SpeechAudio{}, fmt.Errorf("Step 单次合成支持 1 至 1000 个字符")
		}
		if voiceID == "" {
			return SpeechAudio{}, fmt.Errorf("请选择 Step 音色")
		}
		path = "/audio/speech"
		body = map[string]any{"model": model, "input": text, "voice": voiceID, "response_format": "wav"}
	default:
		return SpeechAudio{}, fmt.Errorf("不支持的语音合成模型：%s", model)
	}
	base, err := url.Parse(s.client.baseURL)
	if err != nil {
		return SpeechAudio{}, err
	}
	endpoint := path
	if !strings.HasSuffix(strings.TrimRight(base.Path, "/"), "/v1") {
		endpoint = "/v1" + path
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return SpeechAudio{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.client.baseURL, "/")+endpoint, bytes.NewReader(payload))
	if err != nil {
		return SpeechAudio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav")
	if s.client.apiKey != "" {
		req.Header.Set(authorizationHeader, "Bearer "+s.client.apiKey)
	}
	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return SpeechAudio{}, &NetworkError{Message: err.Error(), Err: err}
	}
	defer resp.Body.Close()
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return SpeechAudio{}, err
	}
	if resp.StatusCode >= 400 {
		return SpeechAudio{}, chatCompletionsHTTPError(resp.StatusCode, audio, resp.Header)
	}
	if len(audio) <= 44 || !bytes.HasPrefix(audio, []byte("RIFF")) || !bytes.Equal(audio[8:12], []byte("WAVE")) {
		return SpeechAudio{}, &ParseError{Message: fmt.Sprintf("%s 未返回有效的 WAV 音频", model)}
	}
	declaredSize := int(binary.LittleEndian.Uint32(audio[4:8]))
	if declaredSize != 0xFFFFFFFF && declaredSize+8 > len(audio) {
		return SpeechAudio{}, &ParseError{Message: fmt.Sprintf("%s 返回的 WAV 音频不完整", model)}
	}
	return SpeechAudio{Data: audio, MediaType: "audio/wav"}, nil
}
