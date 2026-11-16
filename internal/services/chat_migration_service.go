package services

// 在线聊天流复制的预览、冲突确认、执行与失败恢复，对齐 services/chat_migration_service.py。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/pelletier/go-toml/v2"

	"MaiGo/internal/A_memorix"
	statistics "MaiGo/internal/services/statistics"
)

var (
	chatMigrationStore   *MigrationStore
	chatMigrationRunning = map[string]bool{}
	chatMigrationMu      sync.Mutex
	// ChatMigrationGateInstance 主循环迁移门
	ChatMigrationGateInstance = NewChatMigrationGate()
	// ChatMigrationConfigPath bot_config.toml 路径
	ChatMigrationConfigPath string
	// ReloadChatMigrationConfig 配置热重载回调
	// ChatMigrationNotifyExpressionIndex 通知表达向量索引刷新
	ChatMigrationNotifyExpressionIndex func()
	ReloadChatMigrationConfig          func(ctx context.Context) error
)

// InitializeChatMigrations 初始化迁移存储并恢复中断任务
func InitializeChatMigrations(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("数据库未接线")
	}
	store := NewMigrationStore(db)
	if err := store.Initialize(ctx); err != nil {
		return err
	}
	chatMigrationStore = store
	items, err := store.List(ctx, true)
	if err != nil {
		return err
	}
	for _, item := range items {
		status := fmt.Sprint(item["status"])
		if status == "preview" || status == "completed" || status == "failed" {
			continue
		}
		job, err := store.Get(ctx, fmt.Sprint(item["id"]))
		if err != nil {
			continue
		}
		source, _ := job["source"].(string)
		target, _ := job["target"].(string)
		ChatMigrationGateInstance.Block([]string{source, target})
		payload, _ := job["payload"].(map[string]any)
		_ = store.Update(ctx, fmt.Sprint(item["id"]), "recovery_required", payload, "上次迁移被中断，请恢复后重新预览")
	}
	return nil
}

// PreviewChatMigration 生成迁移预览
func PreviewChatMigration(ctx context.Context, source, target string) (map[string]any, error) {
	store := chatMigrationStore
	if store == nil {
		return nil, errors.New("聊天迁移服务尚未初始化")
	}
	state := statistics.RefreshUsageHourly(ctx, store.DB, 10*time.Second)
	if !state.Ready {
		return nil, errors.New("模型用量统计仍在汇总，请稍后重新预览迁移")
	}
	snapshot, err := store.Snapshot(ctx, source, target)
	if err != nil {
		return nil, err
	}
	identities, _ := snapshot["identities"].(map[string]map[string]any)
	sourceRow := identities[source]
	targetRow := identities[target]
	config, err := ChatMigrationConfigSnapshotFrom(ChatMigrationConfigPath, sourceRow, targetRow)
	if err != nil {
		return nil, err
	}
	mem, err := chatMigrationMemory(ctx, "snapshot", map[string]any{"source": source, "target": target})
	if err != nil {
		return nil, err
	}
	conflicts, _ := snapshot["conflicts"].([]map[string]any)
	for _, section := range config.Sections {
		if section.Automatic {
			continue
		}
		conflicts = append(conflicts, map[string]any{
			"id": section.ID, "kind": section.Kind, "label": section.Label,
			"source": section.Source, "target": section.Target, "choices": section.Choices,
		})
	}
	sourceGroups := store.SourceRows(snapshot, "sharing_group_members", source)
	targetGroups := store.SourceRows(snapshot, "sharing_group_members", target)
	if len(sourceGroups) > 0 && !sameSharingGroups(sourceGroups, targetGroups) {
		groupNames := map[string]string{}
		for _, group := range ListSharingGroups(ctx) {
			groupNames[group.ID] = group.Name
		}
		conflicts = append(conflicts, map[string]any{
			"id": "sharing_groups", "kind": "config", "label": "相通组成员关系",
			"source":  groupPayload(sourceGroups, groupNames),
			"target":  groupPayload(targetGroups, groupNames),
			"choices": []string{"source", "target", "both"},
		})
	}
	counts := make([]map[string]any, 0, len(snapshot["tables"].(map[string][]map[string]any)))
	tables, _ := snapshot["tables"].(map[string][]map[string]any)
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		pending := 0
		for _, row := range store.SourceRows(snapshot, name, source) {
			if !store.AlreadyCopied(snapshot, name, row, source, target) {
				pending++
			}
		}
		counts = append(counts, map[string]any{
			"key": name, "label": ChatMigrationLabels[name],
			"source":  len(store.SourceRows(snapshot, name, source)),
			"target":  len(store.SourceRows(snapshot, name, target)),
			"pending": pending,
		})
	}
	memoryPayload, _ := mem.(map[string]any)
	memoryFingerprint := ""
	var memoryCounts any = map[string]any{"source": 0, "target": 0}
	if memoryPayload != nil {
		memoryFingerprint = fmt.Sprint(memoryPayload["fingerprint"])
		memoryCounts = memoryPayload["counts"]
	}
	payload := map[string]any{
		"fingerprint": snapshot["fingerprint"],
		"config":      config,
		"memory":      map[string]any{"fingerprint": memoryFingerprint, "counts": memoryCounts},
		"conflicts":   conflicts,
		"counts":      counts,
		"identities":  snapshot["identities"],
	}
	jobID, err := store.Create(ctx, source, target, payload)
	if err != nil {
		return nil, err
	}
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return PublicChatMigrationJob(job), nil
}

func sameSharingGroups(left, right []map[string]any) bool {
	leftIDs := make([]string, 0, len(left))
	rightIDs := make([]string, 0, len(right))
	for _, row := range left {
		leftIDs = append(leftIDs, fmt.Sprint(row["group_id"]))
	}
	for _, row := range right {
		rightIDs = append(rightIDs, fmt.Sprint(row["group_id"]))
	}
	sort.Strings(leftIDs)
	sort.Strings(rightIDs)
	return strings.Join(leftIDs, ",") == strings.Join(rightIDs, ",")
}

func groupPayload(rows []map[string]any, names map[string]string) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		groupID := fmt.Sprint(row["group_id"])
		result = append(result, map[string]any{"group_name": names[groupID]})
	}
	return result
}

// PublicChatMigrationJob 输出给前端的任务视图
func PublicChatMigrationJob(job map[string]any) map[string]any {
	payload, _ := job["payload"].(map[string]any)
	result := make(map[string]any, len(job))
	for key, value := range job {
		if key != "payload" {
			result[key] = value
		}
	}
	conflicts, _ := payload["conflicts"].([]map[string]any)
	if rawConflicts, ok := payload["conflicts"].([]any); ok && conflicts == nil {
		for _, item := range rawConflicts {
			if mapping, ok := item.(map[string]any); ok {
				conflicts = append(conflicts, mapping)
			}
		}
	}
	automatic := make([]map[string]any, 0)
	if config, ok := payload["config"].(map[string]any); ok {
		if sections, ok := config["sections"].([]any); ok {
			for _, item := range sections {
				if section, ok := item.(map[string]any); ok {
					if auto, _ := section["automatic"].(bool); auto {
						automatic = append(automatic, section)
					}
				}
			}
		}
	}
	result["conflicts"] = conflicts
	result["automatic_config"] = automatic
	result["counts"] = payload["counts"]
	if memory, ok := payload["memory"].(map[string]any); ok {
		result["memory"] = memory["counts"]
	}
	result["identities"] = payload["identities"]
	result["result"] = payload["result"]
	return result
}

// GetChatMigrationJob 读取任务视图
func GetChatMigrationJob(ctx context.Context, jobID string) (map[string]any, error) {
	if chatMigrationStore == nil {
		return nil, errors.New("聊天迁移服务尚未初始化")
	}
	job, err := chatMigrationStore.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return PublicChatMigrationJob(job), nil
}

// ListChatMigrationJobs 列出最近任务
func ListChatMigrationJobs(ctx context.Context) ([]map[string]any, error) {
	if chatMigrationStore == nil {
		return nil, errors.New("聊天迁移服务尚未初始化")
	}
	return chatMigrationStore.List(ctx, false)
}

// StartChatMigration 校验冲突选择并异步执行
func StartChatMigration(ctx context.Context, jobID string, decisions map[string]string, confirmMemory bool) (map[string]any, error) {
	store := chatMigrationStore
	if store == nil {
		return nil, errors.New("聊天迁移服务尚未初始化")
	}
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	chatMigrationMu.Lock()
	alreadyRunning := chatMigrationRunning[jobID]
	chatMigrationMu.Unlock()
	if fmt.Sprint(job["status"]) != "preview" || alreadyRunning {
		return nil, errors.New("该迁移已执行或正在执行，请重新预览")
	}
	if !confirmMemory {
		return nil, errors.New("必须确认目标长期记忆将被全量覆盖")
	}
	payload, _ := job["payload"].(map[string]any)
	conflicts := payloadConflicts(payload)
	expectedIDs := make(map[string]struct{}, len(conflicts))
	for _, conflict := range conflicts {
		id := fmt.Sprint(conflict["id"])
		expectedIDs[id] = struct{}{}
		choice := decisions[id]
		valid := false
		if choices, ok := conflict["choices"].([]any); ok {
			for _, candidate := range choices {
				if fmt.Sprint(candidate) == choice {
					valid = true
					break
				}
			}
		} else if choices, ok := conflict["choices"].([]string); ok {
			for _, candidate := range choices {
				if candidate == choice {
					valid = true
					break
				}
			}
		}
		if !valid {
			return nil, errors.New("存在未处理的冲突")
		}
	}
	if len(decisions) != len(expectedIDs) {
		return nil, errors.New("冲突处理项与预览不一致")
	}
	for id := range decisions {
		if _, ok := expectedIDs[id]; !ok {
			return nil, errors.New("冲突处理项与预览不一致")
		}
	}
	if err := store.Claim(ctx, jobID, decisions); err != nil {
		return nil, err
	}
	chatMigrationMu.Lock()
	chatMigrationRunning[jobID] = true
	chatMigrationMu.Unlock()
	go func() {
		defer func() {
			chatMigrationMu.Lock()
			delete(chatMigrationRunning, jobID)
			chatMigrationMu.Unlock()
		}()
		if err := ExecuteChatMigration(context.Background(), jobID); err != nil {
			zap.L().Error("聊天迁移执行失败", zap.String("job_id", jobID), zap.Error(err))
		}
	}()
	return map[string]any{"id": jobID, "status": "queued"}, nil
}

func payloadConflicts(payload map[string]any) []map[string]any {
	if conflicts, ok := payload["conflicts"].([]map[string]any); ok {
		return conflicts
	}
	conflicts := make([]map[string]any, 0)
	if raw, ok := payload["conflicts"].([]any); ok {
		for _, item := range raw {
			if mapping, ok := item.(map[string]any); ok {
				conflicts = append(conflicts, mapping)
			}
		}
	}
	return conflicts
}

// ExecuteChatMigration 执行已领取的迁移任务
func ExecuteChatMigration(ctx context.Context, jobID string) error {
	store := chatMigrationStore
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	payload, _ := job["payload"].(map[string]any)
	source, _ := job["source"].(string)
	target, _ := job["target"].(string)
	sessions := []string{source, target}
	paused := false
	defer func() {
		if paused {
			ChatMigrationGateInstance.Resume(sessions)
		}
	}()
	if err := ChatMigrationGateInstance.Pause(ctx, sessions); err != nil {
		return err
	}
	paused = true
	if err := store.Update(ctx, jobID, "validating", payload, ""); err != nil {
		return err
	}
	current, err := store.Snapshot(ctx, source, target)
	if err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	if fmt.Sprint(current["fingerprint"]) != fmt.Sprint(payload["fingerprint"]) {
		return chatMigrationFail(ctx, jobID, payload, errors.New("数据在预览后发生变化，请重新预览并处理冲突"), &paused, sessions)
	}
	raw, err := os.ReadFile(ChatMigrationConfigPath)
	if err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	configPayload, _ := payload["config"].(map[string]any)
	if fmt.Sprint(configPayload["fingerprint"]) != MigrationDigest(string(raw)) {
		return chatMigrationFail(ctx, jobID, payload, errors.New("配置在预览后发生变化，请重新预览"), &paused, sessions)
	}
	configState, err := chatMigrationConfigFromPayload(configPayload)
	if err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	decisions := payloadDecisions(payload)
	rendered, err := RenderChatMigrationConfig(configState, decisions)
	if err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	payload["rendered_config"] = rendered
	payload["memory_started"] = true
	if err := store.Update(ctx, jobID, "memory", payload, ""); err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	memoryPayload, _ := payload["memory"].(map[string]any)
	if _, err := chatMigrationMemory(ctx, "apply", map[string]any{
		"job_id": jobID, "source": source, "target": target,
		"expected": fmt.Sprint(memoryPayload["fingerprint"]),
	}); err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	if rendered != string(raw) {
		payload["config_started"] = true
		if err := store.Update(ctx, jobID, "configuration", payload, ""); err != nil {
			return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
		}
		if err := ReplaceChatMigrationConfig(ChatMigrationConfigPath, MigrationDigest(string(raw)), rendered); err != nil {
			return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
		}
		if ReloadChatMigrationConfig != nil {
			if err := ReloadChatMigrationConfig(ctx); err != nil {
				return chatMigrationFail(ctx, jobID, payload, fmt.Errorf("迁移配置热重载失败: %w", err), &paused, sessions)
			}
		}
	}
	if err := store.Update(ctx, jobID, "database", payload, ""); err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	if _, err := store.Apply(ctx, jobID, fmt.Sprint(payload["fingerprint"]), decisions); err != nil {
		return chatMigrationFail(ctx, jobID, payload, err, &paused, sessions)
	}
	if err := finalizeChatMigration(ctx, jobID); err != nil {
		return err
	}
	paused = false
	return nil
}

func chatMigrationFail(ctx context.Context, jobID string, payload map[string]any, cause error, paused *bool, sessions []string) error {
	store := chatMigrationStore
	latest, err := store.Get(ctx, jobID)
	if err == nil {
		if latestPayload, ok := latest["payload"].(map[string]any); ok {
			payload = latestPayload
		}
		if committed, _ := payload["database_committed"].(bool); committed {
			_ = store.Update(ctx, jobID, "recovery_required", payload, "数据已提交，需恢复运行状态: "+cause.Error())
			*paused = false
			return cause
		}
	}
	if rollbackErr := rollbackChatMigration(ctx, jobID, payload); rollbackErr != nil {
		_ = store.Update(ctx, jobID, "recovery_required", payload, cause.Error()+"; 恢复失败: "+rollbackErr.Error())
		*paused = false
		return cause
	}
	_ = store.Update(ctx, jobID, "failed", payload, cause.Error())
	return cause
}

func rollbackChatMigration(ctx context.Context, jobID string, payload map[string]any) error {
	if started, _ := payload["config_started"].(bool); started {
		raw, err := os.ReadFile(ChatMigrationConfigPath)
		if err != nil {
			return err
		}
		configPayload, _ := payload["config"].(map[string]any)
		original := fmt.Sprint(configPayload["raw"])
		rendered := fmt.Sprint(payload["rendered_config"])
		if string(raw) != original {
			if err := ReplaceChatMigrationConfig(ChatMigrationConfigPath, MigrationDigest(rendered), original); err != nil {
				return err
			}
			if ReloadChatMigrationConfig != nil {
				if err := ReloadChatMigrationConfig(ctx); err != nil {
					return fmt.Errorf("恢复配置热重载失败: %w", err)
				}
			}
		}
	}
	if started, _ := payload["memory_started"].(bool); started {
		_, err := chatMigrationMemory(ctx, "restore", map[string]any{"job_id": jobID})
		return err
	}
	return nil
}

func finalizeChatMigration(ctx context.Context, jobID string) error {
	store := chatMigrationStore
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if err := RefreshSharingGroups(ctx); err != nil {
		zap.L().Warn("刷新相通组失败", zap.Error(err))
	}
	if ChatMigrationNotifyExpressionIndex != nil {
		ChatMigrationNotifyExpressionIndex()
	}
	payload, _ := job["payload"].(map[string]any)
	return store.Update(ctx, jobID, "completed", payload, "")
}

// RecoverChatMigration 恢复中断任务
func RecoverChatMigration(ctx context.Context, jobID string) (map[string]any, error) {
	store := chatMigrationStore
	if store == nil {
		return nil, errors.New("聊天迁移服务尚未初始化")
	}
	chatMigrationMu.Lock()
	alreadyRunning := chatMigrationRunning[jobID]
	chatMigrationMu.Unlock()
	job, err := store.Get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	status := fmt.Sprint(job["status"])
	if alreadyRunning || status == "preview" || status == "completed" || status == "failed" {
		return nil, errors.New("该任务不需要恢复或仍在执行")
	}
	chatMigrationMu.Lock()
	chatMigrationRunning[jobID] = true
	chatMigrationMu.Unlock()
	defer func() {
		chatMigrationMu.Lock()
		delete(chatMigrationRunning, jobID)
		chatMigrationMu.Unlock()
	}()
	payload, _ := job["payload"].(map[string]any)
	source, _ := job["source"].(string)
	target, _ := job["target"].(string)
	if committed, _ := payload["database_committed"].(bool); committed {
		if err := finalizeChatMigration(ctx, jobID); err != nil {
			return nil, err
		}
	} else {
		if err := rollbackChatMigration(ctx, jobID, payload); err != nil {
			return nil, err
		}
		if err := store.Update(ctx, jobID, "failed", payload, "迁移已恢复，请重新创建预览"); err != nil {
			return nil, err
		}
	}
	ChatMigrationGateInstance.Resume([]string{source, target})
	return map[string]any{"success": true}, nil
}

func chatMigrationMemory(ctx context.Context, action string, args map[string]any) (any, error) {
	host := memorix.DefaultRegistry().Host()
	service, ok := host.(*memorix.AMemorixHostService)
	if !ok || !service.IsEnabled() {
		if metadataExists(host) {
			return nil, errors.New("已有长期记忆，但记忆服务未启用；请启用后迁移，不能跳过现有记忆")
		}
		return map[string]any{"fingerprint": "disabled", "counts": map[string]any{"source": 0, "target": 0}, "success": true}, nil
	}
	payload := map[string]any{"action": action}
	for key, value := range args {
		payload[key] = value
	}
	result, err := service.Invoke(ctx, "memory_chat_migration", payload, nil)
	if err != nil {
		return nil, fmt.Errorf("长期记忆服务暂不可用: %w", err)
	}
	mapped, ok := result.(map[string]any)
	if !ok || (mapped["fingerprint"] == nil && mapped["success"] != true) {
		return nil, fmt.Errorf("长期记忆服务暂不可用: %v", result)
	}
	return mapped, nil
}

func metadataExists(host memorix.Host) bool {
	service, ok := host.(*memorix.AMemorixHostService)
	if !ok {
		return false
	}
	_, err := os.Stat(filepath.Join(service.GetRuntimeDataDir(), "metadata", "metadata.db"))
	return err == nil
}

func chatMigrationConfigFromPayload(payload map[string]any) (ChatMigrationConfigSnapshot, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ChatMigrationConfigSnapshot{}, err
	}
	var state ChatMigrationConfigSnapshot
	if err := json.Unmarshal(encoded, &state); err != nil {
		return ChatMigrationConfigSnapshot{}, err
	}
	for index := range state.Sections {
		state.Sections[index].All = chatMigrationAllRules(state.Raw, state.Sections[index].Path)
	}
	return state, nil
}

func chatMigrationAllRules(raw string, path []any) []map[string]any {
	document := map[string]any{}
	if err := toml.Unmarshal([]byte(raw), &document); err != nil {
		return nil
	}
	var node any = document
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			mapping, ok := node.(map[string]any)
			if !ok {
				return nil
			}
			node = mapping[typed]
		case float64:
			list, ok := node.([]any)
			if !ok || int(typed) >= len(list) {
				return nil
			}
			node = list[int(typed)]
		default:
			return nil
		}
	}
	switch typed := node.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapping, ok := item.(map[string]any); ok {
				result = append(result, mapping)
			}
		}
		return result
	default:
		return nil
	}
}

func payloadDecisions(payload map[string]any) map[string]string {
	result := map[string]string{}
	switch typed := payload["decisions"].(type) {
	case map[string]string:
		return typed
	case map[string]any:
		for key, value := range typed {
			result[key] = fmt.Sprint(value)
		}
	}
	return result
}
