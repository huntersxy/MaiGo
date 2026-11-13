package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/config/model_profiles"
	"maibot-go/internal/llm_models"

	"github.com/cloudwego/eino/schema"
)

// Maisaka LLM 路由（迁移期）：
//   - 不打开实例数据库；用 modelprofiles.StaticStore + 可选实例 model_config.toml
//   - 环境变量：MAIBOT_MAISAKA_API_KEY（或 OPENAI_API_KEY）、MAIBOT_MAISAKA_BASE_URL、
//     MAIBOT_MAISAKA_MODEL、MAIBOT_MAISAKA_PROVIDER（client_type，默认 openai_responses）
//   - 若指定 -d 实例目录且 model_config 中 api_providers 含 api_key，优先用 TOML 首 provider 作回退档案

type maisakaLLM struct {
	router     *llm.Router
	modelLabel string
}

func tryMaisakaLLM(instanceDir string) (*maisakaLLM, error) {
	var modelFile *maibotcfg.ModelConfigFile
	var plannerLabel string
	if strings.TrimSpace(instanceDir) != "" {
		inst, err := maibotcfg.LoadFromInstanceDir(instanceDir)
		if err == nil {
			modelFile = &inst.Model
			plannerLabel = firstPlannerModelName(inst.Model)
			SetCLIMessageNickname(inst.Bot.Bot.Nickname)
		}
	}

	store, envLabel, err := staticStoreForMaisaka(modelFile)
	if err != nil {
		return nil, err
	}
	if store == nil {
		label := plannerLabel
		if label == "" {
			label = "未配置"
		}
		return &maisakaLLM{modelLabel: label}, nil
	}

	label := plannerLabel
	if label == "" {
		label = envLabel
	}
	return &maisakaLLM{
		router:     llm.NewRouter(store, modelFile),
		modelLabel: label,
	}, nil
}

// FirstPlannerModelName 取 model_config 中 planner 任务的首个模型名，供 BufferCLI banner 使用
func FirstPlannerModelName(file maibotcfg.ModelConfigFile) string {
	list := file.ModelTaskConfig.Planner.ModelList
	if len(list) == 0 {
		return ""
	}
	return strings.TrimSpace(list[0])
}

func firstPlannerModelName(file maibotcfg.ModelConfigFile) string {
	return FirstPlannerModelName(file)
}

func staticStoreForMaisaka(file *maibotcfg.ModelConfigFile) (modelprofiles.Source, string, error) {
	if file != nil {
		if store, label, ok := staticFromModelConfigFile(*file); ok {
			return store, label, nil
		}
	}
	apiKey := strings.TrimSpace(os.Getenv("MAIBOT_MAISAKA_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if apiKey == "" {
		return nil, "", nil
	}
	baseURL := strings.TrimSpace(os.Getenv("MAIBOT_MAISAKA_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	modelName := strings.TrimSpace(os.Getenv("MAIBOT_MAISAKA_MODEL"))
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}
	provider := strings.TrimSpace(os.Getenv("MAIBOT_MAISAKA_PROVIDER"))
	if provider == "" {
		provider = "openai_responses"
	}
	store, err := modelprofiles.NewStaticStore("maisaka-env", provider, baseURL, modelName, apiKey)
	if err != nil {
		return nil, "", err
	}
	return store, modelName, nil
}

func staticFromModelConfigFile(file maibotcfg.ModelConfigFile) (*modelprofiles.StaticStore, string, bool) {
	for _, p := range file.ApiProviders {
		key := strings.TrimSpace(p.ApiKey)
		base := strings.TrimSpace(p.BaseUrl)
		if key == "" || base == "" {
			continue
		}
		provider := strings.TrimSpace(p.ClientType)
		if provider == "" {
			provider = "openai_responses"
		}
		modelID := firstModelForProvider(file, strings.TrimSpace(p.Name))
		if modelID == "" {
			modelID = "gpt-4o-mini"
		}
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = "maisaka-toml"
		}
		store, err := modelprofiles.NewStaticStore(name, provider, base, modelID, key)
		if err != nil {
			continue
		}
		return store, modelID, true
	}
	return nil, "", false
}

func firstModelForProvider(file maibotcfg.ModelConfigFile, providerName string) string {
	for _, m := range file.Models {
		if strings.TrimSpace(m.ApiProvider) == providerName {
			if id := strings.TrimSpace(m.ModelIdentifier); id != "" {
				return id
			}
		}
	}
	return ""
}

func (m *maisakaLLM) available() bool {
	return m != nil && m.router != nil
}

func (m *maisakaLLM) oneShot(ctx context.Context, systemPrompt, userText string) (string, error) {
	if m == nil || m.router == nil {
		return "", fmt.Errorf("LLM 未配置：设置 MAIBOT_MAISAKA_API_KEY 或实例 model_config.toml 中的 api_key")
	}
	_, client, err := m.router.NewClientForTaskWithFailover(ctx, llm.TaskPlanner)
	if err != nil {
		_, client, err = m.router.NewClientForTaskWithFailover(ctx, llm.TaskReplyer)
		if err != nil {
			return "", fmt.Errorf("模型路由失败: %w", err)
		}
	}
	msg, err := client.Generate(ctx, []*schema.AgenticMessage{
		schema.SystemAgenticMessage(strings.TrimSpace(systemPrompt)),
		schema.UserAgenticMessage(strings.TrimSpace(userText)),
	})
	if err != nil {
		return "", err
	}
	return agenticPlainText(msg), nil
}

func agenticPlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				b.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				b.WriteString(block.UserInputText.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
