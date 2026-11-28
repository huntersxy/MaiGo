package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/llm_models"
	"MaiGo/internal/llm_models/model_client"
)

// SynthesizeReplySpeech 使用当前语音合成分工朗读回复正文，对齐 src/services/tts_service.py
func SynthesizeReplySpeech(ctx context.Context, file maibotcfg.ModelConfigFile, text string) ([]byte, error) {
	task := file.ModelTaskConfig.Tts
	if len(task.ModelList) != 1 {
		return nil, fmt.Errorf("请在语音合成分工中选用一个模型")
	}
	name := strings.TrimSpace(task.ModelList[0])
	if name == "" {
		return nil, fmt.Errorf("请在语音合成分工中选用一个模型")
	}
	var info *maibotcfg.ModelInfo
	for i := range file.Models {
		if file.Models[i].Name == name {
			info = &file.Models[i]
			break
		}
	}
	if info == nil || info.ModelType != "tts" {
		return nil, fmt.Errorf("语音合成分工中的 %s 不是支持的语音合成模型", name)
	}
	cfg, err := llm.ModelConfigFromMaiBotFile(file, name)
	if err != nil {
		return nil, err
	}
	synth, err := model_client.NewSpeechSynthesizer(cfg)
	if err != nil {
		return nil, err
	}
	voiceID := ""
	if raw, ok := info.ExtraParams["voice_id"]; ok {
		voiceID = strings.TrimSpace(fmt.Sprint(raw))
	}
	timeout := task.HardTimeout
	if timeout <= 0 {
		timeout = 120
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	audio, err := synth.SynthesizeSpeech(runCtx, text, voiceID)
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("语音合成超过分工设置的 %g 秒", timeout)
		}
		return nil, err
	}
	return audio.Data, nil
}
