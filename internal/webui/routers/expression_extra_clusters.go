package webuiapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"maibot-go/internal/learners"
)

func expressionClusterMemberPayload(raw map[string]any, chatByID map[string]learners.ChatInfo) map[string]any {
	id := intValue(raw["id"], 0)
	chatID := strings.TrimSpace(expressionAnyText(raw["session_id"]))
	var chatName any
	var chatIDValue any
	if chatID != "" {
		chatIDValue = chatID
		if chat, exists := chatByID[chatID]; exists && chat.ChatName != "" {
			chatName = chat.ChatName
		} else {
			chatName = chatID
		}
	}
	var modifiedBy any
	if text := strings.ToLower(strings.TrimSpace(expressionAnyText(raw["modified_by"]))); text != "" {
		modifiedBy = text
	}
	return map[string]any{
		"id":          id,
		"situation":   strings.TrimSpace(expressionAnyText(raw["situation"])),
		"style":       strings.TrimSpace(expressionAnyText(raw["style"])),
		"count":       intValue(raw["count"], 0),
		"chat_id":     chatIDValue,
		"chat_name":   chatName,
		"checked":     boolValue(raw["checked"]),
		"modified_by": modifiedBy,
	}
}

func expressionClusterSummaries(payload map[string]any, chatByID map[string]learners.ChatInfo) []map[string]any {
	out := make([]map[string]any, 0)
	for _, item := range listValue(payload["clusters"]) {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		members := make([]map[string]any, 0)
		for _, member := range listValue(raw["members"]) {
			memberMap, ok := member.(map[string]any)
			if !ok {
				continue
			}
			members = append(members, expressionClusterMemberPayload(memberMap, chatByID))
		}
		out = append(out, map[string]any{
			"embedding_profile_marker": strings.TrimSpace(expressionAnyText(raw["embedding_profile_marker"])),
			"cluster_id":               intValue(raw["cluster_id"], 0),
			"size":                     intValue(raw["size"], 0),
			"members":                  members,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		si := intValue(out[i]["size"], 0)
		sj := intValue(out[j]["size"], 0)
		if si != sj {
			return si > sj
		}
		mi := out[i]["embedding_profile_marker"].(string)
		mj := out[j]["embedding_profile_marker"].(string)
		if mi != mj {
			return mi < mj
		}
		return intValue(out[i]["cluster_id"], 0) < intValue(out[j]["cluster_id"], 0)
	})
	return out
}

func expressionIndexSampleCount(payload map[string]any) int {
	if count := intValue(payload["sample_count"], -1); count >= 0 {
		return count
	}
	if expressions, ok := payload["expressions"].([]any); ok {
		return len(expressions)
	}
	if items, ok := payload["items"].([]any); ok {
		return len(items)
	}
	return 0
}

func (d Deps) handleExpressionClusters(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	settings := d.expressionVectorSettings()
	indexPath, payload, exists, err := d.loadExpressionVectorPayload(settings.ExpressionVectorIndexPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达聚类失败: "+err.Error())
		return
	}
	store := learnerStore()
	chatByID := d.expressionChatMap(r, store, true)
	if !exists {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":      true,
			"index_exists": false,
			"index_path":   indexPath,
			"clusters":     []map[string]any{},
		})
		return
	}
	clusters := expressionClusterSummaries(payload, chatByID)
	embeddingDimension := intValue(payload["embedding_dimension"], 0)
	var embeddingDimensionValue any
	if embeddingDimension != 0 {
		embeddingDimensionValue = embeddingDimension
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":             true,
		"index_exists":        true,
		"index_path":          indexPath,
		"generated_at":        nullableString(strings.TrimSpace(expressionAnyText(payload["generated_at"]))),
		"updated_at":          nullableString(strings.TrimSpace(expressionAnyText(payload["updated_at"]))),
		"embedding_model":     nullableString(strings.TrimSpace(expressionAnyText(payload["embedding_model"]))),
		"embedding_dimension": embeddingDimensionValue,
		"sample_count":        expressionIndexSampleCount(payload),
		"clusters":            clusters,
	})
}

func (d Deps) handleExpressionClusterMembers(w http.ResponseWriter, r *http.Request) {
	if !d.requireAuth(w, r) {
		return
	}
	clusterID, err := strconv.Atoi(strings.TrimSpace(chi.URLParam(r, "cluster_id")))
	if err != nil {
		writeValidationError(w, "cluster_id 必须是整数")
		return
	}
	profileMarker := strings.TrimSpace(r.URL.Query().Get("profile_marker"))
	settings := d.expressionVectorSettings()
	_, payload, exists, err := d.loadExpressionVectorPayload(settings.ExpressionVectorIndexPath)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "获取表达聚类成员失败: "+err.Error())
		return
	}
	if !exists {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "cluster": nil, "data": []map[string]any{}})
		return
	}
	store := learnerStore()
	chatByID := d.expressionChatMap(r, store, true)
	clusters := expressionClusterSummaries(payload, chatByID)
	var cluster map[string]any
	for _, item := range clusters {
		if intValue(item["cluster_id"], 0) != clusterID {
			continue
		}
		if profileMarker != "" && item["embedding_profile_marker"].(string) != profileMarker {
			continue
		}
		cluster = item
		break
	}
	if cluster == nil {
		writeAPIError(w, http.StatusNotFound, fmt.Sprintf("未找到表达聚类: %d", clusterID))
		return
	}
	marker := cluster["embedding_profile_marker"].(string)
	members := make([]map[string]any, 0)
	for _, item := range listValue(payload["expressions"]) {
		raw, ok := item.(map[string]any)
		if !ok || intValue(raw["cluster_id"], 0) != clusterID {
			continue
		}
		rawMarker := strings.TrimSpace(expressionAnyText(raw["embedding_profile_marker"]))
		if marker != "" && rawMarker != marker {
			continue
		}
		members = append(members, expressionClusterMemberPayload(raw, chatByID))
	}
	sort.SliceStable(members, func(i, j int) bool {
		ci := intValue(members[i]["count"], 0)
		cj := intValue(members[j]["count"], 0)
		if ci != cj {
			return ci > cj
		}
		return intValue(members[i]["id"], 0) < intValue(members[j]["id"], 0)
	})
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cluster": cluster, "data": members})
}
