package chat

import (
	webuiutils "MaiGo/internal/webui/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"MaiGo/internal/chat/session"
)

func (d Deps) loadChatConfigForWrite(w http.ResponseWriter) (map[string]any, string, bool) {
	path, ok := d.botConfigPath()
	if !ok {
		d.writeInstanceDirMissing(w, "/api/chat")
		return nil, "", false
	}
	doc, err := d.readTOMLDocument(path)
	if err != nil {
		d.writeConfigReadError(w, err)
		return nil, "", false
	}
	return doc, path, true
}

func chatRequireTable(doc map[string]any, names ...string) (map[string]any, string) {
	current := doc
	path := ""
	for _, name := range names {
		if path == "" {
			path = name
		} else {
			path += "." + name
		}
		next, ok := current[name].(map[string]any)
		if !ok {
			return nil, path
		}
		current = next
	}
	return current, path
}

func writeChatConfig(w http.ResponseWriter, path string, doc map[string]any) bool {
	if err := webuiutils.WriteTOMLDocument(path, doc); err != nil {
		writeChatFailure(w, http.StatusInternalServerError, "保存配置文件失败: "+err.Error())
		return false
	}
	return true
}

func normalizeChatTalkTime(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" || normalized == "*" {
		return normalized, nil
	}
	if _, _, ok := parseChatTimeRange(normalized); !ok {
		return "", fmt.Errorf("时间段格式应为 HH:MM-HH:MM、* 或留空")
	}
	return normalized, nil
}

func chatTalkRuleConfig(rule map[string]any) map[string]any {
	platform, itemID, ruleType := chatRuleTargets(rule)
	if ruleType == "" {
		ruleType = "group"
	}
	return map[string]any{
		"platform": platform, "item_id": itemID, "rule_type": ruleType,
		"time": chatConfigString(rule["time"]), "value": chatConfigFloat(rule["value"], 0),
	}
}

func chatTalkRuleMatches(rule map[string]any, rec *chatsession.Record) bool {
	return chatConfigString(rule["platform"]) == strings.TrimSpace(rec.Platform) &&
		chatConfigString(rule["item_id"]) == chatTargetID(rec) &&
		chatConfigString(rule["rule_type"]) == chatTypeOf(rec)
}

func (d Deps) handleChatUpdateTalkFrequency(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	if !record.IsGroup() {
		writeChatFailure(w, http.StatusBadRequest, "私聊不再使用回复频率，请使用暂停回复开关")
		return
	}
	var body struct {
		PreviousTime *string  `json:"previous_time"`
		Time         string   `json:"time"`
		Value        *float64 `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	value := 0.0
	if body.Value != nil {
		value = *body.Value
	}
	if value < 0 || value > 1 {
		writeChatFailure(w, http.StatusUnprocessableEntity, "value 必须在 0 到 1 之间")
		return
	}
	normalizedTime, err := normalizeChatTalkTime(body.Time)
	if err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	var previousTime *string
	if body.PreviousTime != nil {
		normalizedPrevious, err := normalizeChatTalkTime(*body.PreviousTime)
		if err != nil {
			writeChatFailure(w, http.StatusBadRequest, err.Error())
			return
		}
		previousTime = &normalizedPrevious
	}
	platform := strings.TrimSpace(record.Platform)
	itemID := chatTargetID(record)
	if platform == "" || itemID == "" {
		writeChatFailure(w, http.StatusBadRequest, "聊天流缺少平台或目标 ID，无法保存规则")
		return
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	chatConfig, tablePath := chatRequireTable(doc, "chat")
	if chatConfig == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat] 配置节")
		return
	}
	replyTiming, _ := chatRequireTable(chatConfig, "reply_timing")
	if replyTiming == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat.reply_timing] 配置节")
		return
	}
	_ = tablePath
	rules := make([]map[string]any, 0)
	for _, rule := range chatConfigRules(replyTiming["talk_value_rules"]) {
		rules = append(rules, chatTalkRuleConfig(rule))
	}
	nextRule := map[string]any{"platform": platform, "item_id": itemID, "rule_type": chatTypeOf(record), "time": normalizedTime, "value": value}
	replaceIndex, fallbackIndex := -1, -1
	for index, rule := range rules {
		if !chatTalkRuleMatches(rule, record) {
			continue
		}
		if chatConfigString(rule["time"]) == normalizedTime {
			replaceIndex = index
			break
		}
		if previousTime != nil && chatConfigString(rule["time"]) == *previousTime {
			fallbackIndex = index
		}
	}
	targetIndex := replaceIndex
	if targetIndex < 0 {
		targetIndex = fallbackIndex
	}
	if targetIndex < 0 {
		rules = append(rules, nextRule)
	} else {
		rules[targetIndex] = nextRule
	}
	talkRules := make([]any, len(rules))
	for index, rule := range rules {
		talkRules[index] = rule
	}
	replyTiming["talk_value_rules"] = talkRules
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}

func (d Deps) handleChatDeleteTalkFrequency(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	if !record.IsGroup() {
		writeChatFailure(w, http.StatusBadRequest, "私聊不再使用回复频率，请使用暂停回复开关")
		return
	}
	normalizedTime, err := normalizeChatTalkTime(r.URL.Query().Get("time"))
	if err != nil {
		writeChatFailure(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	chatConfig, _ := chatRequireTable(doc, "chat")
	if chatConfig == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat] 配置节")
		return
	}
	replyTiming, _ := chatRequireTable(chatConfig, "reply_timing")
	if replyTiming == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat.reply_timing] 配置节")
		return
	}
	rules := make([]map[string]any, 0)
	removed := false
	for _, rule := range chatConfigRules(replyTiming["talk_value_rules"]) {
		normalized := chatTalkRuleConfig(rule)
		if chatTalkRuleMatches(normalized, record) && chatConfigString(normalized["time"]) == normalizedTime {
			removed = true
			continue
		}
		rules = append(rules, normalized)
	}
	if !removed {
		writeChatFailure(w, http.StatusNotFound, "未找到可删除的当前聊天流发言频率规则")
		return
	}
	nextRules := make([]any, len(rules))
	for index, rule := range rules {
		nextRules[index] = rule
	}
	replyTiming["talk_value_rules"] = nextRules
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}

func (d Deps) handleChatPrivateReplyPause(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	if record.IsGroup() {
		writeChatFailure(w, http.StatusBadRequest, "暂停私聊回复只适用于私聊")
		return
	}
	platform := strings.TrimSpace(record.Platform)
	itemID := chatTargetID(record)
	if platform == "" || itemID == "" {
		writeChatFailure(w, http.StatusBadRequest, "私聊缺少平台或用户 ID")
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	chatConfig, _ := chatRequireTable(doc, "chat")
	if chatConfig == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat] 配置节")
		return
	}
	replyTiming, _ := chatRequireTable(chatConfig, "reply_timing")
	if replyTiming == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat.reply_timing] 配置节")
		return
	}
	rules := make([]any, 0)
	for _, rule := range chatConfigRules(replyTiming["private_reply_pause_rules"]) {
		if chatConfigString(rule["platform"]) == platform && chatConfigString(rule["item_id"]) == itemID {
			continue
		}
		rules = append(rules, rule)
	}
	if body.Paused != nil {
		rules = append(rules, map[string]any{"platform": platform, "item_id": itemID, "paused": *body.Paused})
	}
	replyTiming["private_reply_pause_rules"] = rules
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}

func (d Deps) handleChatUpdateLearning(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	kind := strings.TrimSpace(chi.URLParam(r, "kind"))
	section := ""
	switch kind {
	case "expression":
		section = "expression"
	case "jargon":
		section = "jargon"
	default:
		writeChatFailure(w, http.StatusBadRequest, "未知学习类型")
		return
	}
	var body struct {
		Use   *bool `json:"use"`
		Learn *bool `json:"learn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	use, learn := true, true
	if body.Use != nil {
		use = *body.Use
	}
	if body.Learn != nil {
		learn = *body.Learn
	}
	platform := strings.TrimSpace(record.Platform)
	itemID := chatTargetID(record)
	if platform == "" || itemID == "" {
		writeChatFailure(w, http.StatusBadRequest, "聊天流缺少平台或目标 ID，无法保存学习规则")
		return
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	configSection, _ := chatRequireTable(doc, section)
	if configSection == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 ["+section+"] 配置节")
		return
	}
	nextRule := map[string]any{"platform": platform, "item_id": itemID, "type": chatTypeOf(record), "use": use, "learn": learn}
	rules := make([]any, 0)
	replaced := false
	for _, existing := range chatConfigRules(configSection["learning_list"]) {
		if !replaced && chatConfigString(existing["platform"]) == platform && chatConfigString(existing["item_id"]) == itemID && chatRuleTypeCompat(existing) == chatTypeOf(record) {
			rules = append(rules, nextRule)
			replaced = true
			continue
		}
		rules = append(rules, existing)
	}
	if !replaced {
		rules = append(rules, nextRule)
	}
	configSection["learning_list"] = rules
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}

func chatRuleTypeCompat(rule map[string]any) string {
	ruleType := chatConfigString(rule["type"])
	if ruleType == "" {
		ruleType = chatConfigString(rule["rule_type"])
	}
	return ruleType
}

func (d Deps) handleChatUpsertPrompt(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeChatFailure(w, http.StatusUnprocessableEntity, "请求格式错误")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeChatFailure(w, http.StatusBadRequest, "Prompt 不能为空")
		return
	}
	platform := strings.TrimSpace(record.Platform)
	itemID := chatTargetID(record)
	if platform == "" || itemID == "" {
		writeChatFailure(w, http.StatusBadRequest, "聊天流缺少平台或目标 ID，无法保存 Prompt")
		return
	}
	index := -1
	if rawIndex := strings.TrimSpace(r.URL.Query().Get("index")); rawIndex != "" {
		parsed, err := strconv.Atoi(rawIndex)
		if err != nil || parsed < 0 {
			writeChatFailure(w, http.StatusUnprocessableEntity, "index 必须为非负整数")
			return
		}
		index = parsed
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	chatConfig, _ := chatRequireTable(doc, "chat")
	if chatConfig == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat] 配置节")
		return
	}
	replyStyle, _ := chatRequireTable(chatConfig, "reply_style")
	if replyStyle == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat.reply_style] 配置节")
		return
	}
	nextPrompt := map[string]any{"platform": platform, "item_id": itemID, "rule_type": chatTypeOf(record), "prompt": prompt}
	prompts := make([]map[string]any, 0)
	for _, raw := range chatConfigRules(replyStyle["chat_prompts"]) {
		if item, ok := chatPromptItem(raw); ok {
			prompts = append(prompts, item)
		}
	}
	if index < 0 {
		prompts = append(prompts, nextPrompt)
	} else if index < len(prompts) && chatPromptMatches(prompts[index], record) {
		prompts[index] = nextPrompt
	} else {
		writeChatFailure(w, http.StatusNotFound, "未找到可修改的当前聊天流 Prompt")
		return
	}
	values := make([]any, len(prompts))
	for index, item := range prompts {
		values[index] = item
	}
	replyStyle["chat_prompts"] = values
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}

func chatPromptMatches(item map[string]any, record *chatsession.Record) bool {
	return chatConfigString(item["platform"]) == strings.TrimSpace(record.Platform) &&
		chatConfigString(item["item_id"]) == chatTargetID(record) &&
		chatConfigString(item["rule_type"]) == chatTypeOf(record)
}

func (d Deps) handleChatDeletePrompt(w http.ResponseWriter, r *http.Request) {
	record, ok := d.getChatSettingsSession(w, r)
	if !ok {
		return
	}
	index, err := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "index")))
	if err != nil || index < 0 {
		writeChatFailure(w, http.StatusUnprocessableEntity, "index 必须为非负整数")
		return
	}
	doc, path, ok := d.loadChatConfigForWrite(w)
	if !ok {
		return
	}
	chatConfig, _ := chatRequireTable(doc, "chat")
	if chatConfig == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat] 配置节")
		return
	}
	replyStyle, _ := chatRequireTable(chatConfig, "reply_style")
	if replyStyle == nil {
		writeChatFailure(w, http.StatusBadRequest, "配置文件缺少 [chat.reply_style] 配置节")
		return
	}
	prompts := make([]map[string]any, 0)
	for _, raw := range chatConfigRules(replyStyle["chat_prompts"]) {
		if item, ok := chatPromptItem(raw); ok {
			prompts = append(prompts, item)
		}
	}
	if index >= len(prompts) || !chatPromptMatches(prompts[index], record) {
		writeChatFailure(w, http.StatusNotFound, "未找到可删除的当前聊天流 Prompt")
		return
	}
	prompts = append(prompts[:index], prompts[index+1:]...)
	values := make([]any, len(prompts))
	for index, item := range prompts {
		values[index] = item
	}
	replyStyle["chat_prompts"] = values
	if !writeChatConfig(w, path, doc) {
		return
	}
	d.writeChatSessionDetail(w, record)
}
