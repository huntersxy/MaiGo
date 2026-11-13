package hostadapter

// A_Memorix 原生内核的宿主能力装配：消息库、模型任务与文本生成网关
// 这里只做真实端口适配，不复制 native 的装配逻辑

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"

	"maibot-go/internal/A_memorix/core/runtime/native"
	"maibot-go/internal/A_memorix/core/utils"
	"maibot-go/internal/A_memorix/scripts"
	"maibot-go/internal/common/database"
	maibotcfg "maibot-go/internal/config/maibot"
	"maibot-go/internal/config/model_profiles"
	"maibot-go/internal/llm_models"
	"maibot-go/internal/services"
)

// NativeRuntimeDeps 原生内核需要的宿主能力
type NativeRuntimeDeps struct {
	DB            *gorm.DB
	ModelStore    modelprofiles.Source
	ModelConfig   *maibotcfg.ModelConfigFile
	BotName       string
	Personality   string
	SessionLookup func(streamID string) (groupID string, userID string)
	// BotSelfIDs 显式配置的机器人自身账号，按平台分组；与 bot_accounts、环境变量合并
	// 提供 BotSelfResolver 时 bot_accounts 只走实时解析，避免禁用后的数据库快照继续命中
	BotSelfIDs map[string][]string
	// BotSelfResolver 运行期账号判定，非 nil 时由 native 优先调用，适合接 BotAccountService
	BotSelfResolver func(platform string, userID string) bool
}

// BuildNativeOptions 把宿主实例配置与消息库适配为 native.Options
func BuildNativeOptions(deps NativeRuntimeDeps) native.Options {
	sessionLookup := deps.SessionLookup
	if sessionLookup == nil && deps.DB != nil {
		sessionLookup = NewChatSessionLookup(deps.DB)
	}
	// 配置/环境账号是显式来源；数据库快照只在没有实时解析器时兜底
	botSelfIDs := mergeNativeBotSelfIDs(deps.BotSelfIDs, botSelfIDsFromEnv())
	botSelfResolver := deps.BotSelfResolver
	if botSelfResolver != nil {
		liveResolver := botSelfResolver
		botSelfResolver = func(platform, userID string) bool {
			if liveResolver(platform, userID) {
				return true
			}
			return hasNativeBotSelfID(botSelfIDs, platform, userID)
		}
	} else {
		botSelfIDs = mergeNativeBotSelfIDs(botSelfIDs, botSelfIDsFromDatabase(deps.DB))
	}
	options := native.Options{
		BotName:                strings.TrimSpace(deps.BotName),
		Personality:            strings.TrimSpace(deps.Personality),
		SessionLookup:          sessionLookup,
		WebImportCommandRunner: scripts.WebImportCommandRunner{},
		BotSelfIDs:             botSelfIDs,
		BotSelfResolver:        botSelfResolver,
	}
	if deps.DB != nil {
		query := NewNativeMessageQuery(deps.DB)
		options.MessageSource = query
		options.FindMessages = query.FindMessages
	}
	if deps.ModelStore != nil && deps.ModelConfig != nil {
		gateway := NewNativeLLMGateway(deps.ModelStore, deps.ModelConfig)
		options.LLM = gateway
		// 复用 LLM 网关的模型配置快照，供 embedding 适配器解析 api_providers
		router := gateway.ModelConfigProvider()
		options.EmbeddingConfigSource = router
		options.ModelTasks = TextGenerationTasksFromModelConfig(deps.ModelConfig)
		options.ModelDict = ModelDictFromModelConfig(deps.ModelConfig)
		// 原生内核可能早于 Agent 启动，图片运行时必须在宿主装配阶段就能解析
		// image_embedding 任务；provider 每次按当前路由重新建客户端，模型热重载后仍有效
		native.SetImageEmbeddingClientProvider(func(ctx context.Context) (llm.EmbeddingClient, error) {
			if router == nil {
				return nil, errors.New("图片嵌入模型路由未初始化")
			}
			_, client, err := router.NewEmbeddingClientForTask(ctx, llm.TaskImageEmbedding)
			if err != nil {
				return nil, fmt.Errorf("图片嵌入任务路由失败: %w", err)
			}
			return client, nil
		})
	}
	return options
}

// NewBotSelfResolver 把 BotAccountService 适配为 native 的实时机器人账号判定
// 每次判定都走服务缓存，适配器禁用/删除账号后立即生效；读取失败按非自身处理
func NewBotSelfResolver(service *services.BotAccountService) func(platform string, userID string) bool {
	if service == nil {
		return nil
	}
	return func(platform, userID string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		isSelf, err := service.IsBotSelf(ctx, platform, userID)
		if err != nil {
			return false
		}
		return isSelf
	}
}

// hasNativeBotSelfID 判断显式账号表是否包含目标账号
func hasNativeBotSelfID(ids map[string][]string, platform, account string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	account = strings.TrimSpace(account)
	if platform == "" || account == "" {
		return false
	}
	for _, value := range ids[platform] {
		if value == account {
			return true
		}
	}
	return false
}

// NativeMessageQuery 用宿主 mai_messages（SQLite）适配摘要消息与引用回查
type NativeMessageQuery struct {
	db *gorm.DB
}

// NewNativeMessageQuery 创建消息查询适配器
func NewNativeMessageQuery(db *gorm.DB) *NativeMessageQuery {
	return &NativeMessageQuery{db: db}
}

// GetMessagesByTimeInChat 对齐 find_messages 的时间窗与 limit_mode
func (q *NativeMessageQuery) GetMessagesByTimeInChat(
	ctx context.Context, chatID string, startTime float64, endTime float64, limit int, limitMode string,
) ([]utils.SessionMessage, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("原生摘要消息库未接入")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := q.db.WithContext(ctx).Where("session_id = ?", chatID).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if startTime > 0 {
		query = query.Where("timestamp >= ?", time.Unix(0, int64(startTime*float64(time.Second))))
	}
	if endTime > 0 {
		query = query.Where("timestamp <= ?", time.Unix(0, int64(endTime*float64(time.Second))))
	}
	earliest := strings.EqualFold(strings.TrimSpace(limitMode), "earliest")
	order := "timestamp DESC, id DESC"
	if earliest {
		order = "timestamp ASC, id ASC"
	}
	var rows []memory.MessageLog
	if err := query.Order(order).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if !earliest {
		reverseMessageLogs(rows)
	}
	return sessionMessagesFromLogs(rows), nil
}

// FindMessages 对齐 find_messages 的 platform/session/message_id 精确回查
func (q *NativeMessageQuery) FindMessages(platform, sessionID, messageID string, limit int) ([]utils.SessionMessage, error) {
	if q == nil || q.db == nil {
		return nil, errors.New("原生摘要消息库未接入")
	}
	platform = strings.TrimSpace(platform)
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if sessionID == "" || messageID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 2
	}
	if limit > 50 {
		limit = 50
	}
	query := q.db.Where("session_id = ?", sessionID).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if platform != "" {
		query = query.Where("platform = ?", platform)
	}
	query = query.Where("(external_message_id = ? OR CAST(message_id AS TEXT) = ?)", messageID, messageID)
	var rows []memory.MessageLog
	if err := query.Order("timestamp ASC, id ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return sessionMessagesFromLogs(rows), nil
}

func reverseMessageLogs(rows []memory.MessageLog) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}

func sessionMessagesFromLogs(rows []memory.MessageLog) []utils.SessionMessage {
	out := make([]utils.SessionMessage, 0, len(rows))
	for _, row := range rows {
		messageID := strings.TrimSpace(row.ExternalMessageID)
		if messageID == "" {
			messageID = strconv.FormatInt(row.MessageID, 10)
		}
		userID := strings.TrimSpace(row.ExternalUserID)
		if userID == "" {
			userID = strconv.FormatInt(row.UserID, 10)
		}
		targetID := strings.TrimSpace(row.ExternalTargetID)
		if targetID == "" {
			targetID = strconv.FormatInt(row.TargetID, 10)
		}
		text := strings.TrimSpace(row.ProcessedPlainText)
		if text == "" {
			text = strings.TrimSpace(row.ProcessedPlainText)
		}
		message := utils.SessionMessage{
			Platform:  strings.TrimSpace(row.Platform),
			SessionID: strings.TrimSpace(row.SessionID),
			MessageID: messageID,
			Timestamp: row.Timestamp,
			MessageInfo: utils.ChatMessageInfo{
				UserInfo: utils.ChatUserInfo{
					UserID:       userID,
					UserNickname: strings.TrimSpace(row.UserNickname),
				},
			},
			RawMessage: utils.ChatRawMessage{Components: []utils.ChatMessageComponent{{
				Kind: utils.ComponentText,
				Text: text,
			}}},
		}
		if strings.EqualFold(strings.TrimSpace(row.ConversationKind), "group") {
			message.MessageInfo.GroupInfo = &utils.ChatGroupInfo{GroupID: targetID}
		}
		out = append(out, message)
	}
	return out
}

// NativeLLMGateway 用 llm.Router 实现 A_Memorix 的文本生成端口
type NativeLLMGateway struct {
	profiles modelprofiles.Source
	router   *llm.Router
	file     *maibotcfg.ModelConfigFile
}

// NewNativeLLMGateway 构造按任务路由的文本生成网关
func NewNativeLLMGateway(profiles modelprofiles.Source, file *maibotcfg.ModelConfigFile) *NativeLLMGateway {
	return &NativeLLMGateway{profiles: profiles, router: llm.NewRouter(profiles, file), file: file}
}

// ModelConfigProvider 暴露网关持有的模型配置快照，供 embedding 适配器读取 api_providers
func (g *NativeLLMGateway) ModelConfigProvider() *llm.Router {
	if g == nil {
		return nil
	}
	return g.router
}

// Generate 按任务名路由并生成
func (g *NativeLLMGateway) Generate(ctx context.Context, req utils.TextGenerationRequest) (utils.TextGenerationResult, error) {
	if g == nil || g.router == nil {
		return utils.TextGenerationResult{Success: false, Error: "LLM 网关未初始化"}, errors.New("LLM 网关未初始化")
	}
	taskName := strings.TrimSpace(req.TaskName)
	if taskName == "" {
		return utils.TextGenerationResult{Success: false, Error: "LLM 任务名为空"}, errors.New("LLM 任务名为空")
	}
	_, client, err := g.router.NewClientForTaskWithFailover(ctx, llm.TaskName(taskName))
	if err != nil {
		return utils.TextGenerationResult{Success: false, Error: err.Error()}, err
	}
	return generateNativeText(ctx, client, req.Prompt, req.Temperature, req.MaxTokens)
}

// GeneratePinned 用指定模型构造单模型任务后生成
func (g *NativeLLMGateway) GeneratePinned(
	ctx context.Context, resolved utils.ResolvedLLMModel, requestType string, prompt string,
	temperature *float64, maxTokens *int,
) (utils.TextGenerationResult, error) {
	if g == nil || g.profiles == nil || g.file == nil {
		return utils.TextGenerationResult{Success: false, Error: "LLM 网关未初始化"}, errors.New("LLM 网关未初始化")
	}
	taskName := strings.TrimSpace(resolved.TaskName)
	modelName := strings.TrimSpace(resolved.SelectedModelName)
	if taskName == "" || modelName == "" {
		return g.Generate(ctx, utils.TextGenerationRequest{
			TaskName: taskName, RequestType: requestType, Prompt: prompt,
			Temperature: temperature, MaxTokens: maxTokens,
		})
	}
	file := *g.file
	config := file.ModelTaskConfig
	switch taskName {
	case "replyer":
		config.Replyer.ModelList = []string{modelName}
	case "planner":
		config.Planner.ModelList = []string{modelName}
	case "memory":
		config.Memory.ModelList = []string{modelName}
	case "utils":
		config.Utils.ModelList = []string{modelName}
	case "learner":
		config.Learner.ModelList = []string{modelName}
	case "emoji":
		config.Emoji.ModelList = []string{modelName}
	default:
		return utils.TextGenerationResult{Success: false, Error: "不支持的固定模型任务: " + taskName}, fmt.Errorf("不支持的固定模型任务: %s", taskName)
	}
	file.ModelTaskConfig = config
	router := llm.NewRouter(g.profiles, &file)
	_, client, err := router.NewClientForTaskWithFailover(ctx, llm.TaskName(taskName))
	if err != nil {
		return utils.TextGenerationResult{Success: false, Error: err.Error()}, err
	}
	return generateNativeText(ctx, client, prompt, temperature, maxTokens)
}

func generateNativeText(
	ctx context.Context, client model.AgenticModel, prompt string, temperature *float64, maxTokens *int,
) (utils.TextGenerationResult, error) {
	if client == nil {
		return utils.TextGenerationResult{Success: false, Error: "LLM 客户端未初始化"}, errors.New("LLM 客户端未初始化")
	}
	options := make([]model.Option, 0, 2)
	if temperature != nil {
		options = append(options, model.WithTemperature(float32(*temperature)))
	}
	if maxTokens != nil && *maxTokens > 0 {
		options = append(options, model.WithMaxTokens(*maxTokens))
	}
	message, err := client.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(prompt)}, options...)
	if err != nil {
		return utils.TextGenerationResult{Success: false, Error: err.Error()}, err
	}
	completion := strings.TrimSpace(agenticMessageText(message))
	if completion == "" {
		return utils.TextGenerationResult{Success: false, Error: "LLM 未返回文本"}, nil
	}
	return utils.TextGenerationResult{Success: true, Completion: completion}, nil
}

func agenticMessageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				builder.WriteString(block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeUserInputText:
			if block.UserInputText != nil {
				builder.WriteString(block.UserInputText.Text)
			}
		}
	}
	return builder.String()
}

// TextGenerationTasksFromModelConfig 把实例 model_config 的文本任务转为 A_Memorix 任务配置
func TextGenerationTasksFromModelConfig(file *maibotcfg.ModelConfigFile) map[string]utils.TextGenerationTaskConfig {
	if file == nil {
		return nil
	}
	tasks := map[string]utils.TextGenerationTaskConfig{}
	add := func(name string, config maibotcfg.TaskConfig) {
		tasks[name] = utils.TextGenerationTaskConfig{
			ModelList:         append([]string{}, config.ModelList...),
			MaxTokens:         config.MaxTokens,
			Temperature:       config.Temperature,
			SelectionStrategy: config.SelectionStrategy,
			HardTimeout:       config.HardTimeout,
		}
	}
	add("replyer", file.ModelTaskConfig.Replyer)
	add("planner", file.ModelTaskConfig.Planner)
	add("memory", file.ModelTaskConfig.Memory)
	add("utils", file.ModelTaskConfig.Utils)
	add("learner", file.ModelTaskConfig.Learner)
	add("fast_model", file.ModelTaskConfig.FastModel)
	add("emoji", file.ModelTaskConfig.Emoji)
	return utils.GetTextGenerationModelTasks(tasks, false)
}

// ModelDictFromModelConfig 构造 models_dict 的存在性字典
func ModelDictFromModelConfig(file *maibotcfg.ModelConfigFile) map[string]any {
	if file == nil {
		return nil
	}
	out := make(map[string]any, len(file.Models))
	for _, item := range file.Models {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.ModelIdentifier)
		}
		if name != "" {
			out[name] = item
		}
	}
	return out
}

// mergeNativeBotSelfIDs 合并显式配置、环境变量与 bot_accounts 的机器人账号
func mergeNativeBotSelfIDs(sources ...map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, source := range sources {
		for platform, accounts := range source {
			for _, account := range accounts {
				addNativeBotSelfID(out, platform, account)
			}
		}
	}
	// 本地平台与 WebUI 的账号由服务层固定，不依赖数据库记录
	var botAccounts services.BotAccountService
	for _, platform := range []string{"internal", "webui", "bot_console", "maisaka_cli"} {
		for _, account := range botAccounts.OnlineBotAccounts(platform) {
			addNativeBotSelfID(out, platform, account)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addNativeBotSelfID 写入单个平台账号并去重
func addNativeBotSelfID(out map[string][]string, platform string, account string) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	account = strings.TrimSpace(account)
	if platform == "" || account == "" {
		return
	}
	for _, existing := range out[platform] {
		if existing == account {
			return
		}
	}
	out[platform] = append(out[platform], account)
}

// botSelfIDsFromEnv 解析 MAIBOT_BOT_ACCOUNTS，格式 platform:account，逗号或分号分隔
func botSelfIDsFromEnv() map[string][]string {
	raw := strings.TrimSpace(os.Getenv("MAIBOT_BOT_ACCOUNTS"))
	if raw == "" {
		return nil
	}
	out := map[string][]string{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' }) {
		platform, account, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if !ok {
			continue
		}
		addNativeBotSelfID(out, platform, account)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// botSelfIDsFromDatabase 读取 bot_platform_accounts 中未禁用的机器人账号，表缺失时静默跳过
func botSelfIDsFromDatabase(db *gorm.DB) map[string][]string {
	if db == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	model := &services.BotPlatformAccount{}
	if !db.WithContext(ctx).Migrator().HasTable(model) {
		return nil
	}
	var rows []services.BotPlatformAccount
	if err := db.WithContext(ctx).Select("platform", "account_id", "disabled").
		Where("disabled = ?", false).Find(&rows).Error; err != nil {
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	out := make(map[string][]string, len(rows))
	for _, row := range rows {
		if !row.Disabled {
			addNativeBotSelfID(out, row.Platform, row.AccountID)
		}
	}
	return out
}
