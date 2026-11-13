package expressionvector

import (
	"fmt"
	"strings"
)

// ExpressionVectorSpacesOptions spaces 列表需要的运行期状态
type ExpressionVectorSpacesOptions struct {
	Enabled bool
	Sync    ExpressionVectorSyncStatus
	Current *ExpressionVectorRuntimeProfile
}

// BuildExpressionVectorSpaces 生成表达向量模型库列表响应
// 对齐 Python expression_vector_index._list_vector_spaces:693-743
func BuildExpressionVectorSpaces(payload map[string]any, opts ExpressionVectorSpacesOptions) (map[string]any, error) {
	activeMarker := ""
	if opts.Enabled && payload != nil {
		activeMarker = firstNonEmptyExpressionVectorText(
			expressionVectorSpaceText(payload["embedding_profile_marker"]),
			expressionVectorLegacyMarker,
		)
	}
	current := opts.Current
	if !opts.Enabled || current == nil || strings.TrimSpace(current.Marker) == "" {
		current = nil
	}
	targetMarker := activeMarker
	if current != nil {
		targetMarker = strings.TrimSpace(current.Marker)
	}

	items := make([]map[string]any, 0)
	if payload != nil {
		stored, err := expressionVectorSpaceStoredProfile(payload)
		if err != nil {
			return nil, err
		}
		profiles, err := expressionVectorSpaceProfiles(payload)
		if err != nil {
			return nil, err
		}
		for _, profile := range profiles {
			marker := expressionVectorSpaceText(profile["marker"])
			state := "saved"
			if marker == targetMarker && opts.Sync.Running {
				state = "syncing"
			} else if marker == activeMarker || marker == targetMarker {
				state = "active"
			}
			expressionCount := expressionVectorSpaceInt(profile["expression_count"])
			clusterCount := expressionVectorSpaceInt(profile["cluster_count"])
			dimension := expressionVectorSpaceInt(profile["embedding_dimension"])
			fingerprint, err := expressionVectorSpaceFingerprint(profile, stored, marker)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{
				"space_id":              marker,
				"embedding_fingerprint": fingerprint,
				"state":                 state,
				"can_delete":            state == "saved",
				"vector_count":          expressionCount,
				"counts":                map[string]any{"expression": expressionCount},
				"size_bytes":            (expressionCount + clusterCount) * dimension * 4,
				"last_used_at":          profile["last_used_at"],
			})
		}
	}

	// 对齐 Python：current profile 尚未写入清单时排在首位并标记 syncing
	if current != nil && !expressionVectorSpaceHasMarker(items, targetMarker) {
		items = append([]map[string]any{{
			"space_id":              targetMarker,
			"embedding_fingerprint": expressionVectorRuntimeFingerprint(current),
			"state":                 "syncing",
			"can_delete":            false,
			"vector_count":          0,
			"counts":                map[string]any{"expression": 0},
			"size_bytes":            0,
			"last_used_at":          nil,
		}}, items...)
	}

	state := "empty"
	switch {
	case opts.Sync.Running:
		state = "syncing"
	case opts.Sync.LastError != "":
		state = "failed"
	case payload != nil:
		state = "ready"
	}
	return map[string]any{
		"success":         true,
		"items":           items,
		"state":           state,
		"last_error":      opts.Sync.LastError,
		"target_space_id": targetMarker,
	}, nil
}

// expressionVectorSpaceStoredProfile 读取旧版持久化活动身份
// Python 在 embedding_profile 为 null 等非对象值时会因 AttributeError 返回 500
func expressionVectorSpaceStoredProfile(payload map[string]any) (map[string]any, error) {
	raw, exists := payload["embedding_profile"]
	if !exists {
		return nil, nil
	}
	if raw == nil {
		return nil, fmt.Errorf("表达向量索引 embedding_profile 格式异常")
	}
	stored, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("表达向量索引 embedding_profile 格式异常")
	}
	return stored, nil
}

// expressionVectorSpaceFingerprint 对齐 Python 分组指纹回退与持久化身份覆盖
// Python _stored_profile_fingerprint:661-674 在持久化身份字段缺失时抛错并返回 500
func expressionVectorSpaceFingerprint(profile, stored map[string]any, marker string) (map[string]any, error) {
	fingerprint, _ := profile["embedding_fingerprint"].(map[string]any)
	if len(fingerprint) == 0 {
		fingerprint = map[string]any{
			"model":     expressionVectorSpaceText(profile["embedding_model"]),
			"dimension": expressionVectorSpaceInt(profile["embedding_dimension"]),
		}
	}
	if stored == nil || expressionVectorSpaceText(stored["marker"]) != marker {
		return fingerprint, nil
	}
	// 旧清单里的 embedding_profile 优先提供身份
	if raw, ok := stored["embedding_fingerprint"].(map[string]any); ok && len(raw) > 0 {
		return raw, nil
	}
	required := []string{"model_name", "model_identifier", "api_provider", "dimension"}
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if _, ok := stored[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("表达向量索引 embedding profile 持久化身份字段缺失: %s", strings.Join(missing, ", "))
	}
	return map[string]any{
		"model":            stored["model_name"],
		"model_identifier": stored["model_identifier"],
		"provider":         stored["api_provider"],
		"dimension":        stored["dimension"],
	}, nil
}

// expressionVectorRuntimeFingerprint 返回 current profile 的指纹；Go 侧无探针指纹时按模型与维度降级
func expressionVectorRuntimeFingerprint(profile *ExpressionVectorRuntimeProfile) map[string]any {
	out := map[string]any{}
	if profile == nil {
		out["model"] = ""
		out["dimension"] = 0
		return out
	}
	for key, value := range profile.Fingerprint {
		out[key] = value
	}
	if len(out) == 0 {
		out["model"] = ""
		out["dimension"] = 0
	}
	return out
}

func expressionVectorSpaceHasMarker(items []map[string]any, marker string) bool {
	marker = strings.TrimSpace(marker)
	for _, item := range items {
		if expressionVectorSpaceText(item["space_id"]) == marker {
			return true
		}
	}
	return false
}
