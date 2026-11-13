package config

import _ "embed"

// PersonaPresetFallbackName 是没有实例人格时显示的名字
const PersonaPresetFallbackName = "内置预设"

//go:embed persona_builtin_card.md
var builtinPersonaCard string

//go:embed persona_builtin_simulation.md
var builtinSimulationPrompt string

// BuiltinPersonaTexts 返回代码内置的人格预设内容，供启动时写入数据库
func BuiltinPersonaTexts() (string, string) {
	return builtinPersonaCard, builtinSimulationPrompt
}
