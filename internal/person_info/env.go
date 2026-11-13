package personinfo

import (
	"os"
	"strings"
)

const envPersonLLM = "MAIBOT_PERSON_LLM"

// PersonLLMEnabled MAIBOT_PERSON_LLM=1 时启用人物事实 LLM 提取写回
func PersonLLMEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envPersonLLM))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
