package expressionvector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bytedance/sonic"
	"go.uber.org/zap"
)

// persistentExpressionVector 表达向量索引落盘条目，字段对齐 expression_vector_index 的 expressions
// Vector 仅用于承载旧版 Go JSON 内联向量，新格式把向量写入同目录 npz
type persistentExpressionVector struct {
	ID                     uint      `json:"id"`
	SessionID              string    `json:"session_id"`
	Situation              string    `json:"situation"`
	Style                  string    `json:"style"`
	Count                  int       `json:"count"`
	Checked                bool      `json:"checked"`
	ModifiedBy             string    `json:"modified_by"`
	Fingerprint            string    `json:"fingerprint"`
	EmbeddingProfileMarker string    `json:"embedding_profile_marker"`
	EmbeddingModel         string    `json:"embedding_model"`
	EmbeddingDimension     int       `json:"embedding_dimension"`
	VectorIndex            int       `json:"vector_index"`
	ClusterID              int       `json:"cluster_id"`
	Vector                 []float32 `json:"vector,omitempty"`
}

type persistentExpressionClusterMember struct {
	ID         uint   `json:"id"`
	Situation  string `json:"situation"`
	Style      string `json:"style"`
	Count      int    `json:"count"`
	SessionID  string `json:"session_id,omitempty"`
	Checked    bool   `json:"checked,omitempty"`
	ModifiedBy string `json:"modified_by,omitempty"`
}

type persistentExpressionCluster struct {
	EmbeddingProfileMarker string                              `json:"embedding_profile_marker"`
	ClusterID              int                                 `json:"cluster_id"`
	Size                   int                                 `json:"size"`
	Members                []persistentExpressionClusterMember `json:"members"`
}

// persistentEmbeddingProfile 对齐 Python embedding_profiles 的清单字段
type persistentEmbeddingProfile struct {
	Marker               string         `json:"marker"`
	ProfileVersion       int            `json:"profile_version,omitempty"`
	EmbeddingModel       string         `json:"embedding_model,omitempty"`
	EmbeddingDimension   int            `json:"embedding_dimension,omitempty"`
	ExpressionCount      int            `json:"expression_count"`
	ClusterCount         int            `json:"cluster_count"`
	VectorsKey           string         `json:"vectors_key,omitempty"`
	ClusterCentersKey    string         `json:"cluster_centers_key,omitempty"`
	UpdatedAt            string         `json:"updated_at,omitempty"`
	LastUsedAt           string         `json:"last_used_at,omitempty"`
	EmbeddingFingerprint map[string]any `json:"embedding_fingerprint,omitempty"`
}

type persistentExpressionVectorFile struct {
	Version                 int                           `json:"version"`
	GeneratedAt             string                        `json:"generated_at,omitempty"`
	UpdatedAt               string                        `json:"updated_at,omitempty"`
	EmbeddingModel          string                        `json:"embedding_model,omitempty"`
	EmbeddingDimension      int                           `json:"embedding_dimension,omitempty"`
	EmbeddingProfileMarker  string                        `json:"embedding_profile_marker,omitempty"`
	EmbeddingProfileVersion int                           `json:"embedding_profile_version,omitempty"`
	VectorsFile             string                        `json:"vectors_file,omitempty"`
	SampleCount             int                           `json:"sample_count"`
	Expressions             []persistentExpressionVector  `json:"expressions"`
	Clusters                []persistentExpressionCluster `json:"clusters,omitempty"`
	EmbeddingProfiles       []persistentEmbeddingProfile  `json:"embedding_profiles,omitempty"`
	EmbeddingFailures       []persistentEmbeddingFailure  `json:"embedding_failures"`
	ClusterMaintenance      *persistentClusterMaintenance `json:"cluster_maintenance,omitempty"`
	EmbeddingProfile        map[string]any                `json:"embedding_profile,omitempty"`
	// Items 兼容旧版 Go 索引文件
	Items []persistentExpressionVector `json:"items,omitempty"`
}

// expressionVectorProfileState 保存一个模型分组的数组与原始表达式，未激活分组在保存时原样保留
type expressionVectorProfileState struct {
	metadata       persistentEmbeddingProfile
	vectors        expressionVectorArray
	clusterCenters expressionVectorArray
	expressions    []persistentExpressionVector
}

const (
	expressionVectorIndexFileVersion = 2
	expressionVectorEmbeddingModel   = "go-cosine"
	expressionVectorProfileMarker    = "go-cosine-v1"
	expressionVectorLegacyMarker     = "__legacy_unmarked__"
	expressionVectorProfileVersion   = 3
)

// PersistentExpressionVectorStore 带 JSON 清单 + npz 向量文件的表达向量索引
//
// MaiBot 使用 src/chat/replyer/expression_vector_index.py 的 FAISS 索引；
// Go 侧以 Python 兼容的 npz 数据层提供同一检索语义，索引文件路径来自
// expression.expression_vector_index_path
type PersistentExpressionVectorStore struct {
	mem                *InMemoryExpressionVectorStore
	path               string
	generatedAt        string
	mu                 sync.Mutex
	activeMarker       string
	profiles           map[string]*expressionVectorProfileState
	vectorsFile        string
	embeddingFailures  map[uint]persistentEmbeddingFailure
	clusterMaintenance *persistentClusterMaintenance
	embeddingProfile   map[string]any

	// pendingClusterChanges 记录本批显式写入或删除的表达，累计到 cluster_maintenance
	pendingClusterChanges map[uint]struct{}
	// forceClusterRebuild 由稳定化流程置位，下一次落盘强制全量重聚类
	forceClusterRebuild bool
}

// NewPersistentExpressionVectorStore 创建并从 path 载入已有索引
func NewPersistentExpressionVectorStore(path string) (*PersistentExpressionVectorStore, error) {
	path = strings.TrimSpace(path)
	store := &PersistentExpressionVectorStore{
		mem:               NewInMemoryExpressionVectorStore(),
		path:              path,
		profiles:          make(map[string]*expressionVectorProfileState),
		embeddingFailures: make(map[uint]persistentEmbeddingFailure),
	}
	if path != "" {
		if err := store.Load(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Path 返回索引文件路径
func (s *PersistentExpressionVectorStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// SetPath 切换索引文件路径并重新载入
func (s *PersistentExpressionVectorStore) SetPath(path string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.path = strings.TrimSpace(path)
	s.mu.Unlock()
	return s.Load()
}

// Load 从磁盘载入索引，文件不存在时视为空索引
func (s *PersistentExpressionVectorStore) Load() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Save 把当前索引原子写入磁盘
func (s *PersistentExpressionVectorStore) Save() error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// Upsert 写入向量并立即落盘
func (s *PersistentExpressionVectorStore) Upsert(id uint, sessionID string, vector []float32) {
	if s == nil {
		return
	}
	s.mem.Upsert(id, sessionID, vector)
	s.mu.Lock()
	s.markClusterChangeLocked(id)
	s.mu.Unlock()
	s.persistNow()
}

// UpsertExpression 写入带表达元数据的向量并立即落盘
func (s *PersistentExpressionVectorStore) UpsertExpression(meta ExpressionVectorMetadata, vector []float32) {
	if s == nil {
		return
	}
	s.mem.UpsertExpression(meta, vector)
	s.mu.Lock()
	s.markClusterChangeLocked(meta.ID)
	s.mu.Unlock()
	s.persistNow()
}

func (s *PersistentExpressionVectorStore) persistNow() {
	s.mu.Lock()
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		zap.L().Warn("表达向量索引落盘失败",
			zap.String("path", s.path),
			zap.Error(err))
	}
}

// Remove 删除向量并立即落盘
func (s *PersistentExpressionVectorStore) Remove(id uint) {
	if s == nil {
		return
	}
	s.mem.Remove(id)
	s.mu.Lock()
	s.markClusterChangeLocked(id)
	s.mu.Unlock()
	s.persistNow()
}

// Len 返回索引条目数量
func (s *PersistentExpressionVectorStore) Len() int {
	if s == nil {
		return 0
	}
	return s.mem.Len()
}

// TopK 委托内存余弦检索
func (s *PersistentExpressionVectorStore) TopK(query []float32, filter ExpressionVectorFilter, k int) []uint {
	if s == nil {
		return nil
	}
	return s.mem.TopK(query, filter, k)
}

// Scored 委托内存索引返回带向量的候选命中
func (s *PersistentExpressionVectorStore) Scored(query []float32, filter ExpressionVectorFilter) []ExpressionVectorHit {
	if s == nil {
		return nil
	}
	return s.mem.Scored(query, filter)
}

// ClusterScored 委托内存索引的聚类召回
func (s *PersistentExpressionVectorStore) ClusterScored(query []float32, filter ExpressionVectorFilter, clusterPoolSize, limit int) []ExpressionVectorHit {
	if s == nil {
		return nil
	}
	return s.mem.ClusterScored(query, filter, clusterPoolSize, limit)
}

// expressionVectorFingerprint 对齐 expression_vector_index.expression_fingerprint
func expressionVectorFingerprint(id uint, situation, style string) string {
	raw := strconv.FormatUint(uint64(id), 10) + "\n" + normalizeExpressionVectorText(situation) + "\n" + normalizeExpressionVectorText(style)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func normalizeExpressionVectorText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// persistentExpressionClusters 生成 WebUI 聚类页需要的轻量摘要，成员最多 8 条
func persistentExpressionClusters(items []persistentExpressionVector) []persistentExpressionCluster {
	type clusterKey struct {
		marker  string
		cluster int
	}
	membersByKey := map[clusterKey][]persistentExpressionClusterMember{}
	keys := make([]clusterKey, 0)
	for _, item := range items {
		marker := strings.TrimSpace(item.EmbeddingProfileMarker)
		key := clusterKey{marker: marker, cluster: item.ClusterID}
		if _, exists := membersByKey[key]; !exists {
			keys = append(keys, key)
		}
		membersByKey[key] = append(membersByKey[key], persistentExpressionClusterMember{
			ID:         item.ID,
			Situation:  item.Situation,
			Style:      item.Style,
			Count:      item.Count,
			SessionID:  item.SessionID,
			Checked:    item.Checked,
			ModifiedBy: item.ModifiedBy,
		})
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].marker != keys[j].marker {
			return keys[i].marker < keys[j].marker
		}
		return keys[i].cluster < keys[j].cluster
	})
	clusters := make([]persistentExpressionCluster, 0, len(keys))
	for _, key := range keys {
		members := membersByKey[key]
		if len(members) > 8 {
			members = members[:8]
		}
		clusters = append(clusters, persistentExpressionCluster{
			EmbeddingProfileMarker: key.marker,
			ClusterID:              key.cluster,
			Size:                   len(membersByKey[key]),
			Members:                members,
		})
	}
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].Size != clusters[j].Size {
			return clusters[i].Size > clusters[j].Size
		}
		if clusters[i].EmbeddingProfileMarker != clusters[j].EmbeddingProfileMarker {
			return clusters[i].EmbeddingProfileMarker < clusters[j].EmbeddingProfileMarker
		}
		return clusters[i].ClusterID < clusters[j].ClusterID
	})
	return clusters
}

var _ ExpressionVectorStore = (*PersistentExpressionVectorStore)(nil)
var _ expressionVectorMetadataStore = (*PersistentExpressionVectorStore)(nil)

// ExpressionVectorIndexStatus WebUI 需要的索引状态快照
type ExpressionVectorIndexStatus struct {
	Enabled bool
	Path    string
	Count   int
}

// ExpressionVectorIndexStatusSnapshot 返回当前表达向量索引状态
func ExpressionVectorIndexStatusSnapshot(reg Port) ExpressionVectorIndexStatus {
	if reg == nil {
		reg = nil
	}
	store := reg.ExpressionVectors()
	status := ExpressionVectorIndexStatus{Count: store.Len()}
	if persistent, ok := store.(*PersistentExpressionVectorStore); ok {
		status.Enabled = true
		status.Path = persistent.Path()
	}
	return status
}

// expressionVectorBackfillMu 串行化历史回填，避免并发 Sync 重复嵌入同一批表达
var expressionVectorBackfillMu sync.Mutex

// BackfillExpressionVectorIndex 为库内表达补齐向量，返回本次写入数量
// 对齐 Python expression_vector_index._load_history_backfill_items：未审核与 prompt 示例风格也会入索引，
// 检索侧再由 checked 过滤控制候选
func BackfillExpressionVectorIndex(ctx context.Context, reg Port) (written int, err error) {
	expressionVectorBackfillMu.Lock()
	defer expressionVectorBackfillMu.Unlock()
	if reg == nil {
		reg = nil
	}
	reg.BeginExpressionVectorSync()
	defer func() { reg.FinishExpressionVectorSync(err) }()
	embedder := reg.Embedder()
	if embedder == nil {
		return 0, nil
	}
	const pageSize = 100
	written = 0
	page := 1
	for {
		rows, total, err := reg.ListExpressions(ctx, (page-1)*pageSize, pageSize)
		if err != nil {
			return written, err
		}
		for _, rec := range rows {
			if rec.ID == 0 {
				continue
			}
			situation := strings.TrimSpace(rec.Situation)
			style := NormalizeExpressionStyleForLearning(rec.Style)
			if situation == "" || style == "" {
				continue
			}
			text := expressionEmbeddingText(situation, style)
			if text == "" {
				continue
			}
			vector, err := embedder.EmbedText(ctx, text)
			if err != nil || len(vector) == 0 {
				continue
			}
			modifiedBy := rec.ModifiedBy
			UpsertExpressionVector(reg.ExpressionVectors(), ExpressionVectorMetadata{
				ID:         rec.ID,
				SessionID:  rec.SessionID,
				Situation:  situation,
				Style:      style,
				Count:      rec.Count,
				Checked:    rec.Checked,
				ModifiedBy: modifiedBy,
			}, vector)
			written++
		}
		if page*pageSize >= total || len(rows) == 0 {
			break
		}
		page++
	}
	if written > 0 {
		zap.L().Info("表达向量索引回填完成", zap.Int("count", written))
	}
	return written, nil
}

// SyncExpressionVectorIndex 按配置路径启用持久化索引并回填缺失向量
func SyncExpressionVectorIndex(ctx context.Context, reg Port) (int, error) {
	if reg == nil {
		reg = nil
	}
	settings := reg.Settings()
	if !settings.UseVectorExpression {
		// 关闭向量模式时清空历史失败，避免 spaces 列表残留 failed
		reg.ClearExpressionVectorSyncError()
		return 0, nil
	}
	if settings.ExpressionVectorIndexPath == "" {
		err := fmt.Errorf("表达向量索引路径为空")
		reg.RecordExpressionVectorSyncError(err)
		return 0, err
	}
	if _, err := reg.EnablePersistentExpressionVectors(settings.ExpressionVectorIndexPath); err != nil {
		reg.RecordExpressionVectorSyncError(err)
		return 0, err
	}
	return BackfillExpressionVectorIndex(ctx, reg)
}

var _ = sonic.Marshal

// ExpressionFingerprint 暴露索引指纹算法（id + 归一化 situation + 归一化 style）
func ExpressionFingerprint(id uint, situation, style string) string {
	return expressionVectorFingerprint(id, situation, style)
}
