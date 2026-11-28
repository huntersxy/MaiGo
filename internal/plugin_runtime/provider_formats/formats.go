package providerformats

// ProviderFormats 对齐 MaiBot src/plugin_runtime/provider_formats.py：插件 @LLMProvider(formats=...) 声明
type ProviderFormats struct {
	ASR map[string]SpeechPreset `json:"asr"`
	TTS map[string]SpeechPreset `json:"tts"`
	Web []string                `json:"web"`
}

// SpeechPreset 单个 ASR/TTS 模板
type SpeechPreset struct {
	Label  string            `json:"label"`
	Voices map[string]string `json:"voices"`
}
