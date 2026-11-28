package monitor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vmihailenco/msgpack/v5"
	"gorm.io/gorm"

	memory "MaiGo/internal/common/database"
)

// VoiceMediaSchemaVersion 携带语音媒体引用的事件 schema 版本
const VoiceMediaSchemaVersion = 2

type voiceRestoreKey struct {
	sessionID string
	messageID string
}

// MonitorVoiceItem 生成监控事件使用的语音引用
func MonitorVoiceItem(digest, transcript string, index int) map[string]any {
	return map[string]any{
		"kind":  MonitorMediaVoice,
		"hash":  digest,
		"text":  transcript,
		"url":   fmt.Sprintf("/api/webui/system/maisaka-monitor/media/voice/%s", digest),
		"index": index,
	}
}

// RestoreLegacyVoiceMedia 旧事件只记录语音正文时，按真实会话和消息 ID 补回已保存的音频引用
func RestoreLegacyVoiceMedia(db *gorm.DB, events []Event) {
	if db == nil || len(events) == 0 {
		return
	}
	targets := make(map[voiceRestoreKey][]map[string]any)
	for _, event := range events {
		if event.Event != "message.sent" && event.Event != "message.ingested" && event.Event != "message.updated" {
			continue
		}
		payload := event.Data
		if payload == nil {
			continue
		}
		if schema, ok := payloadInt64Value(payload["schema_version"]); ok && schema >= VoiceMediaSchemaVersion {
			continue
		}
		sessionID := strings.TrimSpace(fmt.Sprint(payload["source_session_id"]))
		if sessionID == "" {
			sessionID = strings.TrimSpace(fmt.Sprint(payload["session_id"]))
		}
		messageID := strings.TrimSpace(fmt.Sprint(payload["message_id"]))
		if sessionID == "" || messageID == "" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(payload["source_session_id"])) != "" {
			messageID = strings.TrimPrefix(messageID, sessionID+":")
		}
		key := voiceRestoreKey{sessionID: sessionID, messageID: messageID}
		targets[key] = append(targets[key], payload)
	}
	if len(targets) == 0 {
		return
	}
	sessionIDs := collectVoiceSessionIDs(targets)
	if len(sessionIDs) == 0 {
		return
	}
	var rows []memory.MessageLog
	if err := db.Where("session_id IN ?", sessionIDs).Find(&rows).Error; err != nil {
		return
	}
	type voiceRef struct {
		hash string
		text string
		idx  int
	}
	found := make(map[voiceRestoreKey][]voiceRef)
	hashes := make(map[string]struct{})
	for _, row := range rows {
		candidates := []voiceRestoreKey{
			{sessionID: row.SessionID, messageID: row.ExternalMessageID},
			{sessionID: row.SessionID, messageID: fmt.Sprint(row.MessageID)},
		}
		var key voiceRestoreKey
		for _, candidate := range candidates {
			if candidate.messageID == "" {
				continue
			}
			if _, ok := targets[candidate]; ok {
				key = candidate
				break
			}
		}
		if key.sessionID == "" {
			continue
		}
		if len(row.RawContent) == 0 {
			continue
		}
		var segments []map[string]any
		if err := msgpack.Unmarshal(row.RawContent, &segments); err != nil {
			continue
		}
		for index, segment := range segments {
			if !strings.EqualFold(fmt.Sprint(segment["type"]), "voice") {
				continue
			}
			hash := strings.TrimSpace(fmt.Sprint(segment["hash"]))
			if hash == "" || hash == "<nil>" {
				continue
			}
			found[key] = append(found[key], voiceRef{
				hash: hash,
				text: strings.TrimSpace(fmt.Sprint(segment["data"])),
				idx:  index,
			})
			hashes[hash] = struct{}{}
		}
	}
	if len(hashes) == 0 {
		return
	}
	hashList := make([]string, 0, len(hashes))
	for hash := range hashes {
		hashList = append(hashList, hash)
	}
	var available []string
	_ = db.Model(&memory.BinaryData{}).Where("data_hash IN ?", hashList).Pluck("data_hash", &available).Error
	allowed := make(map[string]struct{}, len(available))
	for _, hash := range available {
		allowed[hash] = struct{}{}
	}
	for key, voices := range found {
		stored := make([]map[string]any, 0, len(voices))
		for _, ref := range voices {
			if _, ok := allowed[ref.hash]; !ok {
				continue
			}
			stored = append(stored, MonitorVoiceItem(ref.hash, ref.text, ref.idx))
		}
		if len(stored) == 0 {
			continue
		}
		for _, payload := range targets[key] {
			media := cloneMediaSlice(payload["media"])
			media = append(media, stored...)
			payload["media"] = sortMediaByIndex(media)
		}
	}
}

func collectVoiceSessionIDs(targets map[voiceRestoreKey][]map[string]any) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(targets))
	for key := range targets {
		if key.sessionID == "" {
			continue
		}
		if _, ok := seen[key.sessionID]; ok {
			continue
		}
		seen[key.sessionID] = struct{}{}
		out = append(out, key.sessionID)
	}
	return out
}

func cloneMediaSlice(raw any) []map[string]any {
	items, ok := raw.([]map[string]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, len(items))
	copy(out, items)
	return out
}

func sortMediaByIndex(media []map[string]any) []map[string]any {
	if len(media) <= 1 {
		return media
	}
	sort.SliceStable(media, func(i, j int) bool {
		left, _ := payloadInt64Value(media[i]["index"])
		right, _ := payloadInt64Value(media[j]["index"])
		return left < right
	})
	return media
}