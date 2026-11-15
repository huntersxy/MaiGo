package emojisystem

// 表情包注册前内容审核与满额替换，对齐 src/emoji_system/emoji_manager.py
// review_emoji_for_registration 与 replace_an_emoji_by_llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"MaiGo/internal/llm_models"
	"MaiGo/internal/prompt"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

const maxEmojiForPrompt = 20

var emojiReplaceNumberPattern = regexp.MustCompile("(?i)(?:取消注册|删除)编号([0-9]+)|(?:unregister|delete)[[:space:]]+number[[:space:]]+([0-9]+)|番号([0-9]+)を(?:解除|削除)")

// reviewEmojiContent 对齐 review_emoji_for_registration：返回 true 表示审核通过
func (r *Registry) reviewEmojiContent(ctx context.Context, path, format string) (bool, error) {
	r.mu.RLock()
	prompts := r.prompts
	router := r.router
	r.mu.RUnlock()
	if router == nil {
		return false, errVLMUnavailable
	}
	if prompts == nil {
		return false, errPromptUnavailable
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("emojisystem: 读取表情文件失败: %w", err)
	}
	// 对齐 Python：审核使用原图与真实格式，不做 GIF 首帧转换
	b64 := base64.StdEncoding.EncodeToString(raw)
	mime := mimeFromFileName("sticker" + extensionForImageFormat(format))
	response, err := generateEmojiVLM(ctx, router, prompts, "emoji_content_filtration", nil, b64, mime)
	if err != nil {
		return false, err
	}
	return !strings.Contains(response, "否"), nil
}

// describeEmojiFile 在注册前生成描述，供满额替换决策使用
func (r *Registry) describeEmojiFile(ctx context.Context, path, format, hash, fileName string) (string, error) {
	r.mu.RLock()
	mgr := r.manager
	prompts := r.prompts
	router := r.router
	r.mu.RUnlock()
	if mgr == nil {
		return "", errMemoryUnavailable
	}
	if router == nil {
		return "", errVLMUnavailable
	}
	if prompts == nil {
		return "", errPromptUnavailable
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("emojisystem: 读取表情文件失败: %w", err)
	}
	b64, mime, imageType, err := prepareVLMPayload(raw, "sticker"+extensionForImageFormat(format))
	if err != nil {
		return "", err
	}
	description, err := generateEmojiDescription(ctx, router, prompts, b64, mime, imageType)
	if err != nil {
		return "", err
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return "", errEmptyVLMDescription
	}
	description, err = applyRegisterDescriptionHook(ctx, mgr, &Emoji{FileHash: hash, FileName: fileName, Description: description}, description, format)
	if err != nil {
		return "", err
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return "", errHookEmptyDescription
	}
	tags := NormalizeEmotionTags(description)
	if len(tags) == 0 {
		return "", errEmptyVLMDescription
	}
	return strings.Join(tags, ","), nil
}

// chooseReplacement 按权重抽样旧表情并让 LLM 决定取消注册哪一个
func (r *Registry) chooseReplacement(ctx context.Context, description string, opts MaintenanceOptions) (*Emoji, error) {
	r.mu.RLock()
	mgr := r.manager
	prompts := r.prompts
	router := r.router
	r.mu.RUnlock()
	if mgr == nil {
		return nil, errMemoryUnavailable
	}
	if router == nil {
		return nil, errVLMUnavailable
	}
	if prompts == nil {
		return nil, errPromptUnavailable
	}
	pool := mgr.pool()
	if len(pool) == 0 {
		return nil, nil
	}
	candidates := weightedEmojiSample(pool, maxEmojiForPrompt)
	if len(candidates) == 0 {
		return nil, nil
	}
	lines := make([]string, 0, len(candidates))
	for i, item := range candidates {
		text := strings.TrimSpace(item.Description)
		if text == "" {
			text = "无描述"
		}
		lines = append(lines, fmt.Sprintf("编号: %d\n描述: %s\n使用次数: %d\n添加时间: %s\n", i+1, text, item.QueryCount, formatEmojiRegisterTime(item.RegisterTime)))
	}
	nickname := strings.TrimSpace(opts.BotName)
	if nickname == "" {
		nickname = "麦麦"
	}
	if strings.TrimSpace(description) == "" {
		description = "无描述"
	}
	vars := map[string]string{
		"nickname":      nickname,
		"emoji_num":     strconv.Itoa(mgr.Count()),
		"emoji_num_max": strconv.Itoa(opts.MaxRegNum),
		"emoji_list":    strings.Join(lines, "\n"),
		"description":   description,
	}
	decision, err := generateEmojiTaskText(ctx, router, prompts, "emoji_replace", vars)
	if err != nil {
		return nil, err
	}
	zap.L().Info("表情包替换决策", zap.String("decision", decision))
	lowerDecision := strings.ToLower(decision)
	for _, marker := range []string{"不取消注册", "不删除", "do not unregister", "do not delete", "解除しない", "削除しない"} {
		if strings.Contains(lowerDecision, strings.ToLower(marker)) {
			return nil, nil
		}
	}
	match := emojiReplaceNumberPattern.FindStringSubmatch(decision)
	if match == nil {
		zap.L().Warn("表情包替换决策无法解析编号", zap.String("decision", decision))
		return nil, nil
	}
	number := ""
	for _, group := range match[1:] {
		if group != "" {
			number = group
			break
		}
	}
	index, convErr := strconv.Atoi(number)
	if convErr != nil || index < 1 || index > len(candidates) {
		zap.L().Warn("表情包替换决策编号无效", zap.Int("number", index), zap.Int("candidates", len(candidates)))
		return nil, nil
	}
	selected := candidates[index-1]
	return &selected, nil
}

// generateEmojiTaskText 优先使用 emoji 任务，未配置时回退 planner，对齐 Python
func generateEmojiTaskText(ctx context.Context, router *llm.Router, prompts *prompt.Manager, promptName string, vars map[string]string) (string, error) {
	if router == nil {
		return "", errVLMUnavailable
	}
	if prompts == nil {
		return "", errPromptUnavailable
	}
	task := llm.TaskEmoji
	if !router.HasTaskModels(task) {
		task = llm.TaskPlanner
	}
	if !router.HasTaskModels(task) {
		return "", errEmojiTaskNotConfigured
	}
	_, client, err := router.NewClientForTaskWithFailover(ctx, task)
	if err != nil {
		return "", fmt.Errorf("emojisystem: 表情包任务路由失败: %w", err)
	}
	promptText, err := prompts.Render(promptName, vars)
	if err != nil {
		return "", err
	}
	msg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: promptText}),
		},
	}
	out, err := client.Generate(ctx, []*schema.AgenticMessage{msg})
	if err != nil {
		return "", fmt.Errorf("emojisystem: 表情包替换决策生成失败: %w", err)
	}
	return strings.TrimSpace(agenticPlainText(out)), nil
}

// weightedEmojiSample 对齐 random.choices(weights=1/(query_count+1), k=limit)
func weightedEmojiSample(pool []Emoji, limit int) []Emoji {
	if len(pool) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(pool) {
		limit = len(pool)
	}
	out := make([]Emoji, 0, limit)
	for len(out) < limit {
		total := 0.0
		for _, item := range pool {
			total += 1.0 / float64(item.QueryCount+1)
		}
		if total <= 0 {
			break
		}
		target := rand.Float64() * total
		picked := len(pool) - 1
		for i, item := range pool {
			target -= 1.0 / float64(item.QueryCount+1)
			if target <= 0 {
				picked = i
				break
			}
		}
		out = append(out, pool[picked])
	}
	return out
}

// formatEmojiRegisterTime 对齐 emoji.register_time.strftime，为空时输出「未知时间」
func formatEmojiRegisterTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "未知时间"
	}
	return t.Format("2006-01-02 15:04:05")
}
