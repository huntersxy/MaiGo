package webuiapi

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"

	"maibot-go/internal/learners"
)

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func requireExistingSessionIDs(store learners.WebUIStore, w http.ResponseWriter, r *http.Request, raw []string) ([]string, bool) {
	normalized := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		sessionID, ok := requireExistingChatID(store, w, r, item)
		if !ok {
			return nil, false
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		normalized = append(normalized, sessionID)
	}
	if len(normalized) == 0 {
		writeAPIError(w, http.StatusBadRequest, "缺少聊天流 ID")
		return nil, false
	}
	return normalized, true
}

func parsePathID(w http.ResponseWriter, raw string) (uint, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		writeValidationError(w, "ID 必须是正整数")
		return 0, false
	}
	return uint(value), true
}

func formatIDList(ids []uint) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatUint(uint64(id), 10))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func uniqueUintIDs(ids []uint) []uint {
	out := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// readJSONBody 读取原始请求体，空体返回 nil
func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeValidationError(w, "请求体读取失败")
		return nil, false
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, true
	}
	if _, err := decodeRequestBodyJSON(raw); err != nil {
		writeRequestValidationError(w, jsonInvalidBodyError(raw, err))
		return nil, false
	}
	return raw, true
}

// bodyHasKey 判断 JSON 是否显式提供字段（用于区分 unset 与 null）
func bodyHasKey(raw []byte, key string) bool {
	if len(raw) == 0 {
		return false
	}
	var body map[string]any
	if err := sonic.ConfigDefault.Unmarshal(raw, &body); err != nil {
		return false
	}
	_, ok := body[key]
	return ok
}
