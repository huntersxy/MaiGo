package snowluma

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	observationTTL        = 120 * time.Second
	maxObservations       = 2000
	maxGroupObservations  = 128
	maxObservationGroups  = 128
	identityFingerprintV1 = 1
)

// IdentityObservation 单条指纹观测
type IdentityObservation struct {
	Key         string  `json:"key"`
	AccountID   string  `json:"account_id"`
	GroupID     string  `json:"group_id"`
	UserID      string  `json:"user_id"`
	Timestamp   float64 `json:"timestamp"`
	Fingerprint string  `json:"fingerprint"`
	Nickname    string  `json:"nickname"`
	Cardname    string  `json:"cardname"`
	receivedAt  time.Time
}

// IdentityObservations 短期消息指纹缓存
type IdentityObservations struct {
	mu   sync.Mutex
	rows map[string]*IdentityObservation
	order []string
}

func NewIdentityObservations() *IdentityObservations {
	return &IdentityObservations{rows: make(map[string]*IdentityObservation)}
}

func (o *IdentityObservations) Clear() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rows = make(map[string]*IdentityObservation)
	o.order = nil
}

func (o *IdentityObservations) Observe(message map[string]any) {
	if o == nil || message == nil {
		return
	}
	info := asMap(message["message_info"])
	if info == nil {
		return
	}
	group := asMap(info["group_info"])
	if group == nil {
		return
	}
	account := asString(message["account_id"])
	groupID := asString(group["group_id"])
	user := asMap(info["user_info"])
	if user == nil {
		return
	}
	userID := asString(user["user_id"])
	if !isDecimal(account) || !isDecimal(groupID) || !isDecimal(userID) {
		return
	}
	timestamp := asFloat(message["timestamp"], 0)
	if math.IsInf(timestamp, 0) || math.IsNaN(timestamp) {
		return
	}
	if math.Abs(float64(time.Now().Unix())-timestamp) > observationTTL.Seconds() {
		return
	}
	messageID := asString(message["message_id"])
	if messageID == "" {
		return
	}
	key := account + ":" + groupID + ":" + messageID
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked(now)
	if _, exists := o.rows[key]; exists {
		return
	}
	row := &IdentityObservation{
		Key:         key,
		AccountID:   account,
		GroupID:     groupID,
		UserID:      userID,
		Timestamp:   timestamp,
		Fingerprint: textFingerprint(message["raw_message"]),
		Nickname:    truncateRunes(asString(user["user_nickname"]), 128),
		Cardname:    truncateRunes(asString(user["user_cardname"]), 128),
		receivedAt:  now,
	}
	o.rows[key] = row
	o.order = append(o.order, key)
	groupKeys := make([]string, 0)
	for _, existing := range o.order {
		if item := o.rows[existing]; item != nil && item.AccountID == account && item.GroupID == groupID {
			groupKeys = append(groupKeys, existing)
		}
	}
	if overflow := len(groupKeys) - maxGroupObservations; overflow > 0 {
		for _, oldKey := range groupKeys[:overflow] {
			o.removeLocked(oldKey)
		}
	}
	for len(o.rows) > maxObservations && len(o.order) > 0 {
		o.removeLocked(o.order[0])
	}
}

func (o *IdentityObservations) Snapshot(accountID string, groupIDs []string) (map[string]any, error) {
	if len(groupIDs) > maxObservationGroups {
		return nil, fmt.Errorf("一次最多查询 128 个群")
	}
	if o == nil {
		return map[string]any{"protocol": identityFingerprintV1, "observations": []any{}}, nil
	}
	groups := make(map[string]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		groups[asString(id)] = struct{}{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneLocked(time.Now())
	out := make([]map[string]any, 0)
	for _, key := range o.order {
		row := o.rows[key]
		if row == nil || row.AccountID != accountID {
			continue
		}
		if _, ok := groups[row.GroupID]; !ok {
			continue
		}
		out = append(out, map[string]any{
			"key":         row.Key,
			"account_id":  row.AccountID,
			"group_id":    row.GroupID,
			"user_id":     row.UserID,
			"timestamp":   row.Timestamp,
			"fingerprint": row.Fingerprint,
			"nickname":    row.Nickname,
			"cardname":    row.Cardname,
		})
	}
	return map[string]any{"protocol": identityFingerprintV1, "observations": out}, nil
}

func (o *IdentityObservations) pruneLocked(now time.Time) {
	for _, key := range append([]string{}, o.order...) {
		row := o.rows[key]
		if row == nil || now.Sub(row.receivedAt) >= observationTTL {
			o.removeLocked(key)
		}
	}
}

func (o *IdentityObservations) removeLocked(key string) {
	delete(o.rows, key)
	for index, existing := range o.order {
		if existing == key {
			o.order = append(o.order[:index], o.order[index+1:]...)
			return
		}
	}
}

func textFingerprint(raw any) string {
	segments := segmentList(raw)
	if len(segments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		segType := asString(segment["type"])
		switch segType {
		case "text", "at", "reply":
		default:
			return ""
		}
		if segType == "text" {
			data := segment["data"]
			text, ok := data.(string)
			if !ok {
				return ""
			}
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "")
	if len(text) > 2048 {
		return ""
	}
	text = strings.Join(strings.Fields(normalizeNFKC(text)), " ")
	if len([]rune(text)) < 8 || uniqueRuneCount(text) < 4 || strings.HasPrefix(text, "/") {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func segmentList(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		return nil
	}
}

func isDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func uniqueRuneCount(value string) int {
	seen := map[rune]struct{}{}
	for _, r := range value {
		seen[r] = struct{}{}
	}
	return len(seen)
}

func normalizeNFKC(value string) string {
	// 轻量近似：去掉不可见控制字符，空格折叠由 Fields 完成。
	// 完整 NFKC 留后续；指纹算法核心（长度/字符集/sha256）已对齐。
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
