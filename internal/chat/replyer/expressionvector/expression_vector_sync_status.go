package expressionvector

import "strings"

// ExpressionVectorSyncStatus 表达向量索引同步任务的运行期状态
// 对齐 Python expression_vector_index 的 _history_backfill_task 与 _history_backfill_error
type ExpressionVectorSyncStatus struct {
	Running   bool
	LastError string
}

// ExpressionVectorRuntimeProfile 运行期持久化索引当前使用的 embedding profile
// 对齐 Python _profile_cache 中的 current profile
type ExpressionVectorRuntimeProfile struct {
	Marker      string
	Fingerprint map[string]any
}

// ExpressionVectorCurrentProfile 返回运行期持久化索引当前使用的 profile
// 只有已载入的持久化索引才存在 current profile；内存索引没有真实来源
func ExpressionVectorCurrentProfile(reg Port) (ExpressionVectorRuntimeProfile, bool) {
	if reg == nil {
		reg = nil
	}
	store, ok := reg.ExpressionVectors().(*PersistentExpressionVectorStore)
	if !ok || store == nil {
		return ExpressionVectorRuntimeProfile{}, false
	}
	return store.currentRuntimeProfile()
}

// currentRuntimeProfile 返回内存索引当前 profile 标识与指纹
func (s *PersistentExpressionVectorStore) currentRuntimeProfile() (ExpressionVectorRuntimeProfile, bool) {
	if s == nil {
		return ExpressionVectorRuntimeProfile{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	marker := strings.TrimSpace(s.activeMarker)
	if marker == "" {
		return ExpressionVectorRuntimeProfile{}, false
	}
	fingerprint := map[string]any{}
	if profile := s.profiles[marker]; profile != nil && len(profile.metadata.EmbeddingFingerprint) > 0 {
		for key, value := range profile.metadata.EmbeddingFingerprint {
			fingerprint[key] = value
		}
	} else if profile != nil {
		fingerprint["model"] = profile.metadata.EmbeddingModel
		fingerprint["dimension"] = profile.metadata.EmbeddingDimension
	} else {
		fingerprint["model"] = ""
		fingerprint["dimension"] = 0
	}
	return ExpressionVectorRuntimeProfile{Marker: marker, Fingerprint: fingerprint}, true
}
