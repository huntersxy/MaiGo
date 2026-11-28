package model_client

// OpenAI 兼容语音转写
// 对齐 MaiBot OpenaiClient._execute_audio_transcription_request：
//   - POST {base_url}/audio/transcriptions，multipart 表单，file 固定 audio.wav
//   - extra_params 拆分 headers/query/body，body 以表单字段发送
//   - 响应优先解析 JSON {"text": "..."}；response_format=text 时接受纯文本

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const openAIAudioTranscriptionsPath = "/audio/transcriptions"

type openAIAudioTranscriber struct {
	client *chatCompletionsClient
}

// Transcribe 执行 OpenAI 兼容语音转写
func (t *openAIAudioTranscriber) Transcribe(ctx context.Context, req AudioTranscriptionRequest) (AudioTranscriptionResult, error) {
	started := time.Now()
	recorder := snapshotAttemptRecorderFromContext(ctx)
	audioBase64 := strings.TrimSpace(req.AudioBase64)
	if audioBase64 == "" {
		return AudioTranscriptionResult{}, &ParseError{Message: "音频转写请求缺少音频数据"}
	}
	audioBytes, err := decodeBase64Audio(audioBase64)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, nil, err)
	}

	extra := mergeAnyMaps(t.client.cfg.ExtraParams, req.ExtraParams)
	overrides := splitAudioTranscriptionOverrides(extra)
	formBody := map[string]any{}
	for key, value := range overrides.body {
		switch key {
		case "file", "model":
			continue
		}
		formBody[key] = value
	}

	deepseek := t.client.cfg.ModelType == "asr" && strings.TrimSpace(t.client.model) == "deepseek-asr"
	endpoint := openAIAudioTranscriptionsPath
	operation := "audio.transcriptions.create"
	uploadName, uploadData := "audio.wav", audioBytes
	if deepseek {
		endpoint = resolveDeepseekASREndpoint(t.client.baseURL)
		operation = "deepseek.tools.asr"
		prepared, err := PrepareDeepseekAudio(audioBytes)
		if err != nil {
			return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, nil, &ParseError{Message: err.Error(), Err: err})
		}
		uploadName, uploadData = prepared.Name, prepared.Data
	} else if t.client.cfg.ModelType == "asr" {
		uploadName = TranscriptionFilename(audioBytes)
	}
	body, contentType, err := buildAudioTranscriptionForm(t.client.model, uploadName, uploadData, formBody)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, nil, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.client.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, nil, &NetworkError{Message: err.Error(), Err: err})
	}
	httpReq.Header.Set("Content-Type", contentType)
	if t.client.apiKey != "" {
		httpReq.Header.Set(authorizationHeader, "Bearer "+t.client.apiKey)
	}
	for key, value := range overrides.headers {
		httpReq.Header.Set(key, value)
	}
	if len(overrides.query) > 0 {
		query := httpReq.URL.Query()
		for key, value := range overrides.query {
			query.Set(key, value)
		}
		httpReq.URL.RawQuery = query.Encode()
	}

	providerRequest := map[string]any{
		"base_url":      t.client.baseURL,
		"endpoint":      endpoint,
		"method":        http.MethodPost,
		"operation":     operation,
		"file_name":     "audio.wav",
		"model":         t.client.model,
		"extra_body":    formBody,
		"extra_headers": overrides.headers,
		"extra_query":   overrides.query,
		"organization":  t.client.cfg.Organization,
		"project":       t.client.cfg.Project,
	}
	raw, err := t.do(httpReq)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, providerRequest, err)
	}
	text, err := parseAudioTranscriptionResponse(raw, deepseek)
	if err != nil {
		return AudioTranscriptionResult{}, t.snapshotFailure(recorder, started, req, providerRequest, err)
	}
	return AudioTranscriptionResult{Text: text, Raw: raw}, nil
}

func (t *openAIAudioTranscriber) do(req *http.Request) ([]byte, error) {
	client := t.client.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := abortErrorFromContext(req.Context()); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &NetworkError{Message: err.Error(), Err: err}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, &NetworkError{Message: readErr.Error(), Err: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Message: truncateErrBody(raw), Op: "audio.transcriptions.create", RequestID: providerRequestID(resp.Header)}
	}
	return raw, nil
}

func (t *openAIAudioTranscriber) snapshotFailure(recorder *snapshotAttemptRecorder, started time.Time, req AudioTranscriptionRequest, providerRequest map[string]any, err error) error {
	if err == nil {
		return nil
	}
	if providerRequest == nil {
		providerRequest = map[string]any{
			"base_url":  t.client.baseURL,
			"endpoint":  openAIAudioTranscriptionsPath,
			"method":    http.MethodPost,
			"operation": "audio.transcriptions.create",
			"file_name": "audio.wav",
			"model":     t.client.model,
		}
	}
	items := []ContextItem{NewProviderOpaqueItem("audio_transcription", fmt.Sprintf("音频转写请求（audio/wav，base64 长度 %d）", len(strings.TrimSpace(req.AudioBase64))))}
	internal := map[string]any{
		"operation":    "audio_transcription",
		"audio_length": len(strings.TrimSpace(req.AudioBase64)),
		"mime_type":    "audio/wav",
	}
	spec := newRequestSnapshotSpec(t.client.cfg, "openai.audio_transcription", "audio_transcription", nil, nil, providerRequest, internal)
	spec.LogicalTurnID = snapshotLogicalTurnID(recorder)
	spec.ContextItems = items
	spec.Error = err
	spec.StartedAt = started
	recordSnapshotAttempt(recorder, &spec, started)
	return captureRequestSnapshot(spec)
}

func decodeBase64Audio(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, &ParseError{Message: "音频转写请求的 base64 数据无效", Err: err}
	}
	return decoded, nil
}

func buildAudioTranscriptionForm(modelName, fileName string, audio []byte, body map[string]any) ([]byte, string, error) {
	if fileName == "" {
		fileName = "audio.wav"
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err := writer.WriteField("model", modelName); err != nil {
		return nil, "", err
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, "", err
	}
	for key, value := range body {
		if err := writer.WriteField(key, audioFormValue(value)); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func audioFormValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if value == nil {
		return ""
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func splitAudioTranscriptionOverrides(extraParams map[string]any) chatCompletionsOverrides {
	overrides := chatCompletionsOverrides{
		headers: map[string]string{},
		query:   map[string]string{},
		body:    map[string]any{},
	}
	if len(extraParams) == 0 {
		return overrides
	}
	if raw, ok := extraParams["headers"]; ok {
		overrides.headers = chatStringMap(raw)
	}
	if raw, ok := extraParams["query"]; ok {
		overrides.query = chatStringMap(raw)
	}
	if raw, ok := extraParams["body"]; ok {
		if body, ok := raw.(map[string]any); ok {
			for key, value := range body {
				overrides.body[key] = value
			}
		}
	}
	for key, value := range extraParams {
		switch key {
		case "headers", "query", "body":
			continue
		}
		overrides.body[key] = value
	}
	return overrides
}

func resolveDeepseekASREndpoint(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "/v1/tools/asr"
	}
	path := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		return "/tools/asr"
	}
	return "/v1/tools/asr"
}

func parseAudioTranscriptionResponse(raw []byte, deepseek bool) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "", &ParseError{Message: "音频转写响应解析失败，缺少文本内容。"}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		if text, ok := payload["text"].(string); ok {
			return text, nil
		}
		if deepseek {
			return "", &ParseError{Message: "音频转写响应解析失败，缺少文本内容。"}
		}
		return "", &ParseError{Message: "音频转写响应解析失败，缺少文本内容。"}
	}
	if deepseek {
		return "", &ParseError{Message: "音频转写响应解析失败，缺少文本内容。"}
	}
	// response_format=text 时上游直接返回纯文本
	return trimmed, nil
}
