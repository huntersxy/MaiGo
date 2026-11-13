package focus

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"maibot-go/internal/config/maibot"
)

// GetFocusScopeKey 共享组身份；无 focus_groups 时为 global 或单会话
func GetFocusScopeKey(settings Settings, session SessionInfo) (string, error) {
	if session.SessionID == "" {
		return "", fmt.Errorf("Focus 需要非空 session_id")
	}
	groups := settings.FocusGroups
	if len(groups) == 0 {
		return "global", nil
	}
	for _, group := range groups {
		if !groupMatchesSession(group, session) {
			continue
		}
		rules := make([]map[string]string, 0, len(group.Targets))
		for _, target := range group.Targets {
			rules = append(rules, map[string]string{
				"platform":  target.Platform,
				"item_id":   target.ItemId,
				"rule_type": target.RuleType,
			})
		}
		sort.Slice(rules, func(i, j int) bool {
			a, _ := json.Marshal(rules[i])
			b, _ := json.Marshal(rules[j])
			return string(a) < string(b)
		})
		canonical, err := json.Marshal(rules)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(canonical)
		return "group:" + fmt.Sprintf("%x", sum)[:24], nil
	}
	return "session:" + session.SessionID, nil
}

func groupMatchesSession(group maibot.ChatStreamGroup, session SessionInfo) bool {
	for _, target := range group.Targets {
		if TargetMatchesSessionWithWildcards(target, session) {
			return true
		}
	}
	return false
}

// SeenMessageKey 对齐 Focus 组内去重键
func SeenMessageKey(sessionID, messageID string) string {
	return sessionID + "\x00" + messageID
}

// SeenMessageKeyPrefix 清空上下文时过滤 seen 列表
func SeenMessageKeyPrefix(sessionID string) string {
	return sessionID + "\x00"
}

// ValidateWaitSeconds Focus wait 工具参数校验
func ValidateWaitSeconds(seconds int) error {
	if seconds < 0 {
		return fmt.Errorf("seconds 必须是非负整数")
	}
	return nil
}

// FormatWaitSuccess 登记整组等待的返回文案
func FormatWaitSuccess(seconds int) string {
	return fmt.Sprintf("已为整个 Focus 共享组登记等待 %d 秒；@ 可以提前唤醒。", seconds)
}

// FormatWaitPreempted 等待登记前已有 @ 的文案
func FormatWaitPreempted() string {
	return "已登记整组等待，但已有尚未提交的 @ 消息，立即继续下一次思考。"
}

// FormatStopSuccess 结束整组思考的文案
func FormatStopSuccess() string {
	return "已结束整个 Focus 共享组的本轮思考。"
}

// FormatWaitFinished 等待结束参考消息正文
func FormatWaitFinished(sessionID, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "未知原因"
	}
	return fmt.Sprintf("session_id=%s 发起的整组等待已结束：%s。请结合所有聊天的新消息继续判断。", sessionID, reason)
}
