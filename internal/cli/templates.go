package cli

import (
	"fmt"

	maibotcfg "maibot-go/internal/config/maibot"
)

// 占位模板：完整默认项生成见 MaiBot src/config/config.py generate_new_config_file
// Go 侧 init 只保证 inner.version 与目录骨架，便于 maibot run 校验与后续 TOML 加载

const botConfigTemplate = `# MaiBot 实例 bot 配置（占位模板，请按官方文档补全）
# 完整默认配置请使用 Python 版 maibot init 或 WebUI 导出后合并

[inner]
version = %q

[bot]
qq_account = ""

[personality]
personality_core = "在这里填写人格核心描述"

[webui]
enabled = true
`

const modelConfigTemplate = `# MaiBot 实例模型配置（占位模板，运行前必须填写 API 与模型）
# 完整默认配置见 MaiBot config/default_model_config.py

[inner]
version = %q

[[models]]
model_identifier = "example"
name = "example-model"
model_type = "llm"
api_provider = "Example"

[[api_providers]]
name = "Example"
base_url = "https://api.example.com/v1"
api_key = "your-api-key"

[model_task_config.planner]
model_list = ["example-model"]
`

func botConfigPlaceholder() string {
	return fmt.Sprintf(botConfigTemplate, maibotcfg.BotConfigTargetVersion)
}

func modelConfigPlaceholder() string {
	return fmt.Sprintf(modelConfigTemplate, maibotcfg.ModelConfigTargetVersion)
}
