package maisaka

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// pluginProactiveTaskSourcePrefix 插件主动任务内部消息的 source 前缀
// 对齐 Python src/maisaka/runtime.py:706 的 source_kind=f"plugin_proactive:{plugin_id}"
const pluginProactiveTaskSourcePrefix = "plugin_proactive:"

// ProactiveContextRestoreLimit 主动回合最多回填的真实消息数量
// 对齐 Python src/maisaka/runtime.py:370-373 的 _get_context_restore_limit
func (r *Runtime) ProactiveContextRestoreLimit() int {
	if r == nil {
		return 1
	}
	size := r.MaxContextSize()
	if size < 0 {
		size = 0
	}
	return max(1, int(math.Ceil(float64(size)*ContextRestoreFillRatio)))
}

// RestoreProactiveUserContext 主动回合前回填真实用户消息，返回回填条数
// 对齐 Python src/maisaka/runtime.py:375-446 restore_proactive_user_context：
// 上下文已含可回复的真实用户消息时不回填；否则按上限取最近消息，
// 跳过通知消息与已存在的消息后插入到首个会话消息之前，作为可回复引用目标
func (r *Runtime) RestoreProactiveUserContext(ctx context.Context) int {
	if r == nil || r.opts.ProactiveContextRestore == nil {
		return 0
	}
	if r.hasReplyableUserMessage() {
		return 0
	}
	restored, err := r.opts.ProactiveContextRestore(ctx, r.ProactiveContextRestoreLimit())
	if err != nil {
		zap.L().Warn(r.logPrefix+" 主动回合回填用户消息失败", zap.Error(err))
		return 0
	}
	if len(restored) == 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing := make(map[string]struct{}, len(r.chatHistory))
	for _, message := range r.chatHistory {
		if !isSessionBackedMessage(message) {
			continue
		}
		existing[message.MessageID] = struct{}{}
	}
	pending := make([]ContextMessage, 0, len(restored))
	for _, message := range restored {
		if message.MessageID == "" || message.MetadataBool("is_notify") {
			continue
		}
		if _, ok := existing[message.MessageID]; ok {
			continue
		}
		existing[message.MessageID] = struct{}{}
		pending = append(pending, message)
	}
	if len(pending) == 0 {
		return 0
	}
	insertIndex := len(r.chatHistory)
	for index, message := range r.chatHistory {
		if isSessionBackedMessage(message) {
			insertIndex = index
			break
		}
	}
	merged := make([]ContextMessage, 0, len(r.chatHistory)+len(pending))
	merged = append(merged, r.chatHistory[:insertIndex]...)
	merged = append(merged, pending...)
	merged = append(merged, r.chatHistory[insertIndex:]...)
	r.chatHistory = merged
	r.historyRevision++
	zap.L().Info(r.logPrefix+fmt.Sprintf(" 主动回合已回填用户消息 %d 条，作为可回复引用目标", len(pending)),
		zap.Int("插入位置", insertIndex))
	return len(pending)
}

// hasReplyableUserMessage 上下文是否已含可回复的真实用户消息
// 对齐 Python 对 SessionBackedMessage 且 source_kind == "user" 且 message_id 非空的判定
func (r *Runtime) hasReplyableUserMessage() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, message := range r.chatHistory {
		if !isSessionBackedMessage(message) {
			continue
		}
		if message.Source != SourceUser {
			continue
		}
		return true
	}
	return false
}

// isSessionBackedMessage 判断历史条目是否对应真实会话消息
// 对齐 Python SessionBackedMessage：带消息编号且计入上下文条数
func isSessionBackedMessage(message ContextMessage) bool {
	return message.MessageID != "" && message.CountInContext
}

// ArmForcedTurn 启用一次强制触发并返回启用前是否已存在
// 对齐 Python src/maisaka/runtime.py:1239-1246 的 _arm_forced_turn_state（不重置 idle backoff）
func (r *Runtime) ArmForcedTurn(messageID, reason string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	wasArmed := r.forcedTurnEnabled
	r.forcedTurnEnabled = true
	r.forcedTurnMessageID = messageID
	r.forcedTurnReason = reason
	r.mu.Unlock()
	return wasArmed
}

// EnqueueProactiveTask 追加插件主动聊天任务并唤醒内部循环，返回任务编号
// 对齐 Python src/maisaka/runtime.py:673-724 enqueue_proactive_task
func (r *Runtime) EnqueueProactiveTask(pluginID, intent, reason, priority string, metadata map[string]any) (string, error) {
	if r == nil {
		return "", fmt.Errorf("运行时未初始化")
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		pluginID = "unknown"
	}
	intent = strings.TrimSpace(intent)
	if intent == "" {
		return "", fmt.Errorf("主动聊天任务缺少 intent")
	}
	now := time.Now()
	taskID := fmt.Sprintf("proactive:%s:%d", pluginID, now.UnixMilli())
	visible := pluginProactiveTaskText(taskID, pluginID, intent, reason, priority, metadata)
	r.AppendHistory(ContextMessage{
		Role:       RoleUser,
		Content:    visible,
		Source:     pluginProactiveTaskSourcePrefix + pluginID,
		Timestamp:  now,
		ReceivedAt: &now,
		MessageID:  taskID,
		SessionID:  r.SessionID(),
	})
	r.ArmForcedTurn(taskID, "插件主动聊天任务")
	r.QueueProactiveTurn(ContextMessage{
		Role:       RoleUser,
		Content:    "插件主动聊天任务",
		Source:     SourceUser,
		Timestamp:  now,
		ReceivedAt: &now,
		MessageID:  taskID,
		SessionID:  r.SessionID(),
	}, "")
	zap.L().Info(r.logPrefix+" 已接收插件主动聊天任务",
		zap.String("plugin_id", pluginID), zap.String("task_id", taskID))
	return taskID, nil
}

// pluginProactiveTaskText 构造插件主动任务内部消息正文
// 对齐 Python src/maisaka/runtime.py:689-704 的 detail_lines 拼接
func pluginProactiveTaskText(taskID, pluginID, intent, reason, priority string, metadata map[string]any) string {
	lines := []string{
		fmt.Sprintf(`<plugin_proactive_task id="%s" plugin_id="%s">`, taskID, pluginID),
		"插件请求你主动处理一轮聊天：" + intent,
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, "触发原因："+reason)
	}
	if priority = strings.TrimSpace(priority); priority != "" {
		lines = append(lines, "优先级："+priority)
	}
	if len(metadata) > 0 {
		if encoded, err := marshalProactiveMetadata(metadata); err == nil {
			lines = append(lines, "附加信息："+encoded)
		}
	}
	lines = append(lines,
		"请结合当前聊天关系、记忆和上下文，自行决定是否回复以及如何表达。",
		"</plugin_proactive_task>",
	)
	return strings.Join(lines, "\n")
}

// marshalProactiveMetadata 按稳定键序序列化附加信息，分隔符与 Python json.dumps 默认一致
func marshalProactiveMetadata(metadata map[string]any) (string, error) {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("{")
	for index, key := range keys {
		if index > 0 {
			builder.WriteString(", ")
		}
		encodedKey, err := sonic.MarshalString(key)
		if err != nil {
			return "", err
		}
		encodedValue, err := sonic.MarshalString(metadata[key])
		if err != nil {
			return "", err
		}
		builder.WriteString(encodedKey)
		builder.WriteString(": ")
		builder.WriteString(encodedValue)
	}
	builder.WriteString("}")
	return builder.String(), nil
}
