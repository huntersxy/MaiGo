package test

import (
	"testing"

	tools "MaiGo/internal/maisaka/builtin_tool"
	maibotcfg "MaiGo/internal/config/maibot"
	"MaiGo/internal/cli"
)

func TestVoiceAttachmentSchemaHiddenForCLI(t *testing.T) {
	file := maibotcfg.ModelConfigFile{}
	file.ModelTaskConfig.Tts.ModelList = []string{"tts-model"}
	if schema := tools.VoiceAttachmentSchema(cli.MaisakaCLIPlatformName, file); schema != nil {
		t.Fatalf("CLI 平台不应暴露 attach_voice")
	}
}

func TestVoiceAttachmentSchemaRequiresTTSAssignment(t *testing.T) {
	if schema := tools.VoiceAttachmentSchema("qq", maibotcfg.ModelConfigFile{}); schema != nil {
		t.Fatalf("未配置 TTS 分工时不应暴露 attach_voice")
	}
}