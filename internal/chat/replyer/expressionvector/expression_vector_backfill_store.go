package expressionvector

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

const (
	expressionBackfillFailureFileVersion = 1
	expressionBackfillFailureRetry       = 60 * time.Second
	expressionBackfillIsolatedRetry      = 24 * time.Hour
	expressionBackfillIsolationAttempts  = 3
	expressionBackfillErrorMaxLength     = 500
	// 对齐 Python FULL_RECLUSTER_CHANGE_RATIO
	expressionBackfillChangeRatio = 0.05
)

// ExpressionEmbeddingProfile 表达向量 embedding 空间身份，字段对齐 Python ExpressionEmbeddingProfile
type ExpressionEmbeddingProfile struct {
	Marker          string
	ModelName       string
	ModelIdentifier string
	APIProvider     string
	Dimension       int
	Revision        int
	ProbeTexts      []string
	ProbeEmbeddings [][]float32
	Fingerprint     map[string]any
}

// ExpressionBackfillSuccess 一条成功嵌入的表达
type ExpressionBackfillSuccess struct {
	Meta   ExpressionVectorMetadata
	Vector []float32
}

// ExpressionBackfillFailure 一条嵌入失败的表达
type ExpressionBackfillFailure struct {
	Meta ExpressionVectorMetadata
	Err  error
}

// persistentEmbeddingFailure 对齐 Python embedding_failures 记录
type persistentEmbeddingFailure struct {
	ExpressionID           uint    `json:"expression_id"`
	Fingerprint            string  `json:"fingerprint"`
	EmbeddingProfileMarker string  `json:"embedding_profile_marker"`
	Attempts               int     `json:"attempts"`
	FirstFailedAt          string  `json:"first_failed_at"`
	LastFailedAt           string  `json:"last_failed_at"`
	RetryAfterTimestamp    float64 `json:"retry_after_timestamp"`
	Isolated               bool    `json:"isolated"`
	LastErrorType          string  `json:"last_error_type"`
	LastError              string  `json:"last_error"`
}

// persistentClusterMaintenance 对齐 Python cluster_maintenance
type persistentClusterMaintenance struct {
	State                    string  `json:"state"`
	ProfileMarker            string  `json:"profile_marker"`
	ChangesSinceRecluster    int     `json:"changes_since_recluster"`
	ChangedExpressionIDs     []uint  `json:"changed_expression_ids"`
	ChangeRatioThreshold     float64 `json:"change_ratio_threshold"`
	LastReclusterAt          string  `json:"last_recluster_at"`
	LastReclusterSampleCount int     `json:"last_recluster_sample_count"`
	StabilizedAt             string  `json:"stabilized_at,omitempty"`
}

type persistentEmbeddingFailureFile struct {
	Version           int                          `json:"version"`
	UpdatedAt         string                       `json:"updated_at"`
	EmbeddingFailures []persistentEmbeddingFailure `json:"embedding_failures"`
}

// ExpressionBackfillFailurePolicy 控制失败记录的重试与隔离节奏，便于测试注入短间隔
type ExpressionBackfillFailurePolicy struct {
	ItemFailureRetry  time.Duration
	IsolationInterval time.Duration
	IsolationAttempts int
	ErrorMaxLength    int
}

func (p ExpressionEmbeddingProfile) payload() map[string]any {
	probes := make([][]float32, len(p.ProbeEmbeddings))
	for index := range p.ProbeEmbeddings {
		probes[index] = append([]float32(nil), p.ProbeEmbeddings[index]...)
	}
	fingerprint := p.Fingerprint
	if fingerprint == nil {
		fingerprint = map[string]any{}
	}
	return map[string]any{
		"version":               expressionVectorProfileVersion,
		"marker":                p.Marker,
		"model_name":            p.ModelName,
		"model_identifier":      p.ModelIdentifier,
		"api_provider":          p.APIProvider,
		"dimension":             p.Dimension,
		"revision":              p.Revision,
		"probe_texts":           append([]string(nil), p.ProbeTexts...),
		"probe_embeddings":      probes,
		"embedding_fingerprint": fingerprint,
	}
}

// SetActiveEmbeddingProfile 切换当前激活的 embedding profile
// 切换前先保存旧 profile，旧分组的向量文件与表达式原样保留
func (s *PersistentExpressionVectorStore) SetActiveEmbeddingProfile(profile ExpressionEmbeddingProfile) error {
	if s == nil {
		return nil
	}
	if strings.TrimSpace(profile.Marker) == "" {
		return fmt.Errorf("表达向量 embedding profile marker 为空")
	}
	s.mu.Lock()
	oldMarker := s.activeMarker
	oldCount := s.mem.Len()
	s.mu.Unlock()
	if oldMarker != "" && oldMarker != profile.Marker && oldCount > 0 {
		if err := s.Save(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.profiles[profile.Marker]
	if state == nil {
		state = newEmptyExpressionVectorProfile(profile.Marker)
		s.profiles[profile.Marker] = state
	}
	state.metadata.Marker = profile.Marker
	state.metadata.ProfileVersion = expressionVectorProfileVersion
	state.metadata.EmbeddingModel = profile.ModelName
	state.metadata.EmbeddingDimension = profile.Dimension
	if profile.Fingerprint != nil {
		state.metadata.EmbeddingFingerprint = profile.Fingerprint
	}
	if oldMarker != profile.Marker {
		items := append([]persistentExpressionVector(nil), state.expressions...)
		var centers [][]float32
		for index := 0; index < state.clusterCenters.rows; index++ {
			centers = append(centers, state.clusterCenters.row(index))
		}
		s.mem.ReplaceWithClusters(items, centers)
		s.pendingClusterChanges = nil
	}
	s.activeMarker = profile.Marker
	s.embeddingProfile = profile.payload()
	return nil
}

// ApplyExpressionBackfillBatch 应用一批补建结果
// 无成功向量时只更新失败记录，避免为纯失败批次重写全量聚类
func (s *PersistentExpressionVectorStore) ApplyExpressionBackfillBatch(
	profile ExpressionEmbeddingProfile,
	successes []ExpressionBackfillSuccess,
	failures []ExpressionBackfillFailure,
	currentFingerprints map[uint]string,
	now time.Time,
	policies ...ExpressionBackfillFailurePolicy,
) (int, error) {
	if s == nil {
		return 0, nil
	}
	policy := expressionBackfillFailurePolicyOrDefault(policies)
	s.mu.Lock()
	defer s.mu.Unlock()
	successIDs := make(map[uint]struct{}, len(successes))
	applied := 0
	for _, success := range successes {
		if success.Meta.ID == 0 || len(success.Vector) == 0 {
			continue
		}
		// 对齐 _commit_expression_batch：拿到写锁后按当前 DB 指纹复核，
		// 结果过期（表达已被改动）就丢弃，交给下一轮补建重做。
		if len(currentFingerprints) > 0 {
			current, ok := currentFingerprints[success.Meta.ID]
			if !ok || current != expressionVectorFingerprint(success.Meta.ID, success.Meta.Situation, success.Meta.Style) {
				continue
			}
		}
		s.mem.UpsertExpression(success.Meta, success.Vector)
		s.markClusterChangeLocked(success.Meta.ID)
		successIDs[success.Meta.ID] = struct{}{}
		applied++
		delete(s.embeddingFailures, success.Meta.ID)
	}
	isolated := s.mergeEmbeddingFailureRecordsLocked(profile, failures, successIDs, currentFingerprints, now, policy)
	if applied == 0 {
		if len(failures) == 0 {
			return isolated, nil
		}
		return isolated, s.persistEmbeddingFailuresOnlyLocked(now)
	}
	s.embeddingProfile = profile.payload()
	return isolated, s.saveLocked()
}

// FinalizeExpressionBackfill 追平后写入稳定聚类状态并删除独立失败文件
func (s *PersistentExpressionVectorStore) FinalizeExpressionBackfill(profile ExpressionEmbeddingProfile, now time.Time) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mem.Len() == 0 || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if s.clusterMaintenance != nil {
		state, err := resolveExpressionVectorClusterState(s.clusterMaintenance, profile.Marker)
		if err != nil {
			return err
		}
		if state == expressionVectorClusterStateStable {
			if s.clusterMaintenance.State == expressionVectorClusterStateStable {
				return nil
			}
			// 历史小写状态读入后按大写枚举重写一次
			s.clusterMaintenance.State = expressionVectorClusterStateStable
			s.embeddingProfile = profile.payload()
			return s.saveLocked()
		}
	}
	nowText := expressionFailureTimeText(now)
	s.clusterMaintenance = &persistentClusterMaintenance{
		State:                    expressionVectorClusterStateStable,
		ProfileMarker:            profile.Marker,
		ChangesSinceRecluster:    0,
		ChangedExpressionIDs:     []uint{},
		ChangeRatioThreshold:     expressionBackfillChangeRatio,
		LastReclusterAt:          nowText,
		LastReclusterSampleCount: s.mem.Len(),
		StabilizedAt:             nowText,
	}
	s.forceClusterRebuild = true
	s.embeddingProfile = profile.payload()
	return s.saveLocked()
}

// expressionBackfillIndexState 返回已索引条目与失败记录快照
func (s *PersistentExpressionVectorStore) expressionBackfillIndexState() (map[uint]persistentExpressionVector, map[uint]persistentEmbeddingFailure) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	indexed := make(map[uint]persistentExpressionVector)
	for _, profile := range s.profiles {
		for _, item := range profile.expressions {
			indexed[item.ID] = item
		}
	}
	failures := make(map[uint]persistentEmbeddingFailure, len(s.embeddingFailures))
	for id, record := range s.embeddingFailures {
		failures[id] = record
	}
	return indexed, failures
}

func (s *PersistentExpressionVectorStore) embeddingFailureRecords() []persistentEmbeddingFailure {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sortedEmbeddingFailuresLocked()
}

func (s *PersistentExpressionVectorStore) sortedEmbeddingFailuresLocked() []persistentEmbeddingFailure {
	if len(s.embeddingFailures) == 0 {
		return []persistentEmbeddingFailure{}
	}
	records := make([]persistentEmbeddingFailure, 0, len(s.embeddingFailures))
	for _, record := range s.embeddingFailures {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ExpressionID < records[j].ExpressionID })
	return records
}

func (s *PersistentExpressionVectorStore) mergeEmbeddingFailureRecordsLocked(
	profile ExpressionEmbeddingProfile,
	failures []ExpressionBackfillFailure,
	successIDs map[uint]struct{},
	currentFingerprints map[uint]string,
	now time.Time,
	policy ExpressionBackfillFailurePolicy,
) int {
	for id, record := range s.embeddingFailures {
		fingerprint, ok := currentFingerprints[id]
		if !ok || normalizeExpressionVectorText(fingerprint) != normalizeExpressionVectorText(record.Fingerprint) ||
			normalizeExpressionVectorText(record.EmbeddingProfileMarker) != normalizeExpressionVectorText(profile.Marker) {
			delete(s.embeddingFailures, id)
			continue
		}
		if _, success := successIDs[id]; success {
			delete(s.embeddingFailures, id)
		}
	}
	nowText := expressionFailureTimeText(now)
	isolated := 0
	for _, failure := range failures {
		if failure.Meta.ID == 0 {
			continue
		}
		fingerprint := normalizeExpressionVectorText(currentFingerprints[failure.Meta.ID])
		if fingerprint == "" {
			fingerprint = expressionVectorFingerprint(
				failure.Meta.ID,
				normalizeExpressionVectorText(failure.Meta.Situation),
				normalizeExpressionVectorText(failure.Meta.Style),
			)
		}
		previous, exists := s.embeddingFailures[failure.Meta.ID]
		attempts := 1
		firstFailedAt := nowText
		if exists &&
			normalizeExpressionVectorText(previous.Fingerprint) == fingerprint &&
			normalizeExpressionVectorText(previous.EmbeddingProfileMarker) == normalizeExpressionVectorText(profile.Marker) {
			attempts = previous.Attempts + 1
			firstFailedAt = firstNonEmptyExpressionVectorText(previous.FirstFailedAt, nowText)
		}
		retryInterval := policy.ItemFailureRetry
		if attempts >= policy.IsolationAttempts {
			retryInterval = policy.IsolationInterval
			isolated++
		}
		record := persistentEmbeddingFailure{
			ExpressionID:           failure.Meta.ID,
			Fingerprint:            fingerprint,
			EmbeddingProfileMarker: profile.Marker,
			Attempts:               attempts,
			FirstFailedAt:          firstFailedAt,
			LastFailedAt:           nowText,
			RetryAfterTimestamp:    float64(now.Add(retryInterval).UnixNano()) / float64(time.Second),
			Isolated:               attempts >= policy.IsolationAttempts,
			LastErrorType:          expressionFailureErrorType(failure.Err),
			LastError:              expressionFailureErrorText(failure.Err, policy.ErrorMaxLength),
		}
		s.embeddingFailures[failure.Meta.ID] = record
	}
	return isolated
}

func expressionFailureTimeText(now time.Time) string {
	return now.Format("2006-01-02T15:04:05")
}

func expressionFailureErrorType(err error) string {
	if err == nil {
		return ""
	}
	typed := reflect.TypeOf(err)
	for typed.Kind() == reflect.Pointer {
		typed = typed.Elem()
	}
	return typed.Name()
}

func expressionFailureErrorText(err error, maxLength int) string {
	if err == nil {
		return ""
	}
	text := strings.Join(strings.Fields(err.Error()), " ")
	runes := []rune(text)
	if maxLength <= 0 {
		maxLength = expressionBackfillErrorMaxLength
	}
	if len(runes) > maxLength {
		runes = runes[:maxLength]
	}
	return string(runes)
}

func expressionBackfillFailurePolicyOrDefault(policies []ExpressionBackfillFailurePolicy) ExpressionBackfillFailurePolicy {
	policy := ExpressionBackfillFailurePolicy{
		ItemFailureRetry:  expressionBackfillFailureRetry,
		IsolationInterval: expressionBackfillIsolatedRetry,
		IsolationAttempts: expressionBackfillIsolationAttempts,
		ErrorMaxLength:    expressionBackfillErrorMaxLength,
	}
	if len(policies) == 0 {
		return policy
	}
	provided := policies[0]
	if provided.ItemFailureRetry > 0 {
		policy.ItemFailureRetry = provided.ItemFailureRetry
	}
	if provided.IsolationInterval > 0 {
		policy.IsolationInterval = provided.IsolationInterval
	}
	if provided.IsolationAttempts > 0 {
		policy.IsolationAttempts = provided.IsolationAttempts
	}
	if provided.ErrorMaxLength > 0 {
		policy.ErrorMaxLength = provided.ErrorMaxLength
	}
	return policy
}

func (s *PersistentExpressionVectorStore) embeddingFailuresPathLocked() string {
	if strings.TrimSpace(s.path) == "" {
		return ""
	}
	ext := filepath.Ext(s.path)
	stem := strings.TrimSuffix(s.path, ext)
	return stem + ".embedding-failures.json"
}

func (s *PersistentExpressionVectorStore) loadEmbeddingFailuresFileLocked() error {
	path := s.embeddingFailuresPathLocked()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var payload persistentEmbeddingFailureFile
	if err := sonic.ConfigStd.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("表达向量独立失败文件损坏: %w", err)
	}
	if payload.Version > expressionBackfillFailureFileVersion {
		return fmt.Errorf("表达向量独立失败文件版本不匹配: %d", payload.Version)
	}
	if s.embeddingFailures == nil {
		s.embeddingFailures = make(map[uint]persistentEmbeddingFailure)
	}
	for _, record := range payload.EmbeddingFailures {
		if record.ExpressionID == 0 {
			continue
		}
		s.embeddingFailures[record.ExpressionID] = record
	}
	return nil
}

func (s *PersistentExpressionVectorStore) persistEmbeddingFailuresOnlyLocked(now time.Time) error {
	path := s.embeddingFailuresPathLocked()
	if path == "" {
		return nil
	}
	payload := persistentEmbeddingFailureFile{
		Version:           expressionBackfillFailureFileVersion,
		UpdatedAt:         expressionFailureTimeText(now),
		EmbeddingFailures: s.sortedEmbeddingFailuresLocked(),
	}
	encoded, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := writeExpressionVectorJSONAtomic(path, encoded); err != nil {
		return err
	}
	return s.patchMainEmbeddingFailuresLocked(now)
}

func (s *PersistentExpressionVectorStore) patchMainEmbeddingFailuresLocked(now time.Time) error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	payload := make(map[string]any)
	if err := sonic.ConfigStd.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("表达向量索引清单无法补写失败记录: %w", err)
	}
	payload["embedding_failures"] = s.sortedEmbeddingFailuresLocked()
	payload["updated_at"] = expressionFailureTimeText(now)
	encoded, err := sonic.ConfigStd.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return writeExpressionVectorJSONAtomic(s.path, encoded)
}

func (s *PersistentExpressionVectorStore) removeEmbeddingFailuresFileLocked() {
	path := s.embeddingFailuresPathLocked()
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return
	}
}
