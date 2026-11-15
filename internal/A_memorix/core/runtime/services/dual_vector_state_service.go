package services

// 双池向量状态服务，逐段对齐 dual_vector_state_service.py 的 MemoryDualVectorStateService

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"log/slog"

	"MaiGo/internal/A_memorix/core/storage"
	memorixutils "MaiGo/internal/A_memorix/core/utils"
)

// DualVectorStateDeps 双池向量状态服务依赖端口
type DualVectorStateDeps struct {
	// ActiveVectorSpaceID 当前活动向量空间 id，空串表示单空间布局
	ActiveVectorSpaceID func() string
	// DualVectorPoolsReady 双池是否已完成就绪校验
	DualVectorPoolsReady func() bool
	// SetDualVectorPoolsReady 写入双池就绪标记
	SetDualVectorPoolsReady func(bool)
	// EmbeddingDimension 当前 embedding 维度
	EmbeddingDimension func() int
	// CurrentEmbeddingStatusDimension 当前 embedding 状态维度
	CurrentEmbeddingStatusDimension func(ctx context.Context) int
	// CurrentEmbeddingFingerprint 当前 embedding 指纹
	CurrentEmbeddingFingerprint func(ctx context.Context, dimension *int) map[string]any
	// CurrentEmbeddingFingerprintForValidation 用于校验的 embedding 指纹
	CurrentEmbeddingFingerprintForValidation func(ctx context.Context, dimension *int) map[string]any
	// NormalizeEmbeddingFingerprint 归一化 embedding 指纹
	NormalizeEmbeddingFingerprint func(value any) map[string]any
	// StoredVectorsCompatibleWithCurrentEmbedding 判断已存向量是否与当前 embedding 兼容
	StoredVectorsCompatibleWithCurrentEmbedding func(ctx context.Context, store *storage.VectorStore) bool
	// MetadataStore 元数据存储，nil 表示尚未打开
	MetadataStore func() *storage.Store
	// CountVectorRebuildTargets 统计向量重建目标数量
	CountVectorRebuildTargets func(ctx context.Context) map[string]int
	// RelationVectorsEnabled 关系向量是否启用
	RelationVectorsEnabled func() bool
	// ReadParagraphVectorStore 段池向量存储
	ReadParagraphVectorStore func() *storage.VectorStore
	// ReadGraphVectorStore 图池向量存储
	ReadGraphVectorStore func() *storage.VectorStore
	// SetParagraphVectorStore 写入段池向量存储
	SetParagraphVectorStore func(*storage.VectorStore)
	// SetGraphVectorStore 写入图池向量存储
	SetGraphVectorStore func(*storage.VectorStore)
	// ReadVectorStore 单池向量存储
	ReadVectorStore func() *storage.VectorStore
	// SetVectorStore 写入单池向量存储
	SetVectorStore func(*storage.VectorStore)
	// RebuildConfigurationKey 重建配置键，nil 表示当前不在重建
	RebuildConfigurationKey func() string
	// EnsureRebuildEmbeddingUnchanged 校验重建期间 embedding 未变化
	EnsureRebuildEmbeddingUnchanged func(ctx context.Context)
	// RebuildFingerprint 重建期指纹
	RebuildFingerprint func() map[string]any
	// ReconcileV1EvidenceRoot V1 对账证据根目录
	ReconcileV1EvidenceRoot func(ctx context.Context) string
	// V1ValidHashesForPool V1 向量池合法 hash 集合
	V1ValidHashesForPool func(ctx context.Context, pool string) []string
}

// DualVectorStateService 双池向量状态服务
type DualVectorStateService struct {
	*Base
	deps DualVectorStateDeps
}

// NewDualVectorStateService 构造双池向量状态服务
func NewDualVectorStateService(base *Base, deps DualVectorStateDeps) *DualVectorStateService {
	return &DualVectorStateService{Base: base, deps: deps}
}

func (s *DualVectorStateService) logger() *slog.Logger {
	if s.Base != nil {
		return s.Logger()
	}
	return slog.Default()
}

// VectorPoolMode 对齐 _vector_pool_mode
func (s *DualVectorStateService) VectorPoolMode(ctx context.Context) string {
	mode := strings.ToLower(strings.TrimSpace(asString(s.Cfg("retrieval.vector_pools.mode", "dual"))))
	if mode == "" {
		mode = "dual"
	}
	if mode == "single" || mode == "dual" {
		return mode
	}
	return "single"
}

// DualVectorPoolsConfigEnabled 对齐 _dual_vector_pools_config_enabled
func (s *DualVectorStateService) DualVectorPoolsConfigEnabled(ctx context.Context) bool {
	return s.VectorPoolMode(ctx) == "dual"
}

// DualVectorPoolsEnabled 对齐 _dual_vector_pools_enabled
func (s *DualVectorStateService) DualVectorPoolsEnabled(ctx context.Context) bool {
	if !s.DualVectorPoolsConfigEnabled(ctx) {
		return false
	}
	if s.deps.DualVectorPoolsReady == nil {
		return false
	}
	return s.deps.DualVectorPoolsReady()
}

// VectorsRoot 对齐 _vectors_root
func (s *DualVectorStateService) VectorsRoot(ctx context.Context) string {
	spaceID := ""
	if s.deps.ActiveVectorSpaceID != nil {
		spaceID = strings.TrimSpace(s.deps.ActiveVectorSpaceID())
	}
	if spaceID != "" {
		return filepath.Join(s.DataDir(), "vectors", "spaces", spaceID)
	}
	return filepath.Join(s.DataDir(), "vectors")
}

// ParagraphVectorDir 对齐 _paragraph_vector_dir
func (s *DualVectorStateService) ParagraphVectorDir(ctx context.Context) string {
	return filepath.Join(s.VectorsRoot(ctx), "paragraph")
}

// GraphVectorDir 对齐 _graph_vector_dir
func (s *DualVectorStateService) GraphVectorDir(ctx context.Context) string {
	return filepath.Join(s.VectorsRoot(ctx), "graph")
}

// DualVectorReadyManifestPath 对齐 _dual_vector_ready_manifest_path
func (s *DualVectorStateService) DualVectorReadyManifestPath(ctx context.Context) string {
	return filepath.Join(s.VectorsRoot(ctx), "dual_ready.json")
}

// ReadDualVectorReadyManifest 对齐 _read_dual_vector_ready_manifest
func (s *DualVectorStateService) ReadDualVectorReadyManifest(ctx context.Context) map[string]any {
	path := s.DualVectorReadyManifestPath(ctx)
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.logger().Warn("读取双池 ready manifest 失败: " + err.Error())
		return nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		s.logger().Warn("读取双池 ready manifest 失败: " + err.Error())
		return nil
	}
	return asMap(payload)
}

// DualVectorReady 对齐 _dual_vector_ready
func (s *DualVectorStateService) DualVectorReady(ctx context.Context, expectedDimension *int) bool {
	manifest := s.ReadDualVectorReadyManifest(ctx)
	if manifest == nil || asString(manifest["status"]) != "ready" {
		return false
	}
	dimension := 0
	if expectedDimension != nil {
		dimension = *expectedDimension
	}
	if dimension == 0 && s.deps.CurrentEmbeddingStatusDimension != nil {
		dimension = s.deps.CurrentEmbeddingStatusDimension(ctx)
	}
	manifestDimension := pyIntOr(manifest["dimension"], 0, -1<<62)
	if dimension > 0 && manifestDimension != 0 && manifestDimension != dimension {
		s.logger().Warn(fmt.Sprintf("双池 ready manifest 维度不匹配: manifest=%d, expected=%d", manifestDimension, dimension))
		return false
	}
	paragraphCount := pyIntOr(manifest["paragraph_vectors"], 0, -1<<62)
	graphCount := pyIntOr(manifest["graph_vectors"], 0, -1<<62)
	if paragraphCount < 0 || graphCount < 0 {
		return false
	}
	var currentFingerprint map[string]any
	if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
		currentFingerprint = s.deps.CurrentEmbeddingFingerprintForValidation(ctx, nil)
	}
	var manifestFingerprint map[string]any
	if s.deps.NormalizeEmbeddingFingerprint != nil {
		manifestFingerprint = s.deps.NormalizeEmbeddingFingerprint(manifest["embedding_fingerprint"])
	}
	if currentFingerprint == nil || manifestFingerprint == nil {
		emptyRecoveryGeneration := paragraphCount == 0 && graphCount == 0 &&
			strings.TrimSpace(asString(manifest["generation_reason"])) == "integrity_recovery"
		if !emptyRecoveryGeneration {
			if currentFingerprint == nil {
				s.logger().Debug("当前 embedding 指纹尚不可用，双池校验等待模型探测")
			} else {
				s.logger().Warn("双池 ready manifest 缺少可校验 embedding 指纹，保持单池降级")
			}
			return false
		}
	}
	if currentFingerprint != nil && manifestFingerprint != nil &&
		asString(currentFingerprint["hash"]) != asString(manifestFingerprint["hash"]) {
		s.logger().Warn(fmt.Sprintf(
			"双池 ready manifest embedding 指纹不匹配，保持单池降级: manifest=%s, current=%s",
			asString(manifestFingerprint["hash"]), asString(currentFingerprint["hash"])))
		return false
	}
	return pathExists(s.ParagraphVectorDir(ctx)) && pathExists(s.GraphVectorDir(ctx))
}

// WriteDualVectorReadyManifest 对齐 _write_dual_vector_ready_manifest
func (s *DualVectorStateService) WriteDualVectorReadyManifest(ctx context.Context, stats map[string]map[string]int, migrationStats map[string]map[string]int, generationReason string, buildFingerprint map[string]any) {
	currentDimension := 0
	if buildFingerprint != nil {
		currentDimension = pyIntOr(buildFingerprint["dimension"], 0, -1<<62)
	} else if s.deps.CurrentEmbeddingStatusDimension != nil {
		currentDimension = s.deps.CurrentEmbeddingStatusDimension(ctx)
	}
	var embeddingFingerprint map[string]any
	if buildFingerprint != nil {
		embeddingFingerprint = buildFingerprint
	} else if s.deps.CurrentEmbeddingFingerprint != nil {
		embeddingFingerprint = s.deps.CurrentEmbeddingFingerprint(ctx, &currentDimension)
	}
	paragraphDone := 0
	if section := stats["paragraphs"]; section != nil {
		paragraphDone = section["done"]
	}
	entityDone, relationDone := 0, 0
	if section := stats["entities"]; section != nil {
		entityDone = section["done"]
	}
	if section := stats["relations"]; section != nil {
		relationDone = section["done"]
	}
	payload := map[string]any{
		"status":            "ready",
		"version":           1,
		"mode":              "dual",
		"dimension":         currentDimension,
		"created_at":        float64(time.Now().UnixNano()) / 1e9,
		"paragraph_vectors": paragraphDone,
		"graph_vectors":     entityDone + relationDone,
		"stats":             stats,
		"migration":         migrationStats,
	}
	if reason := strings.TrimSpace(generationReason); reason != "" {
		payload["generation_reason"] = reason
	}
	if embeddingFingerprint != nil {
		payload["embedding_fingerprint"] = embeddingFingerprint
	}
	path := s.DualVectorReadyManifestPath(ctx)
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		s.logger().Warn("写入双池 ready manifest 失败: " + err.Error())
		return
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.logger().Warn("写入双池 ready manifest 失败: " + err.Error())
		return
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o666); err != nil {
		s.logger().Warn("写入双池 ready manifest 失败: " + err.Error())
		return
	}
	if err := os.Rename(tmpPath, path); err != nil {
		s.logger().Warn("写入双池 ready manifest 失败: " + err.Error())
	}
}

// RemoveDualVectorReadyManifest 对齐 _remove_dual_vector_ready_manifest
func (s *DualVectorStateService) RemoveDualVectorReadyManifest(ctx context.Context) {
	if err := os.Remove(s.DualVectorReadyManifestPath(ctx)); err != nil && !os.IsNotExist(err) {
		s.logger().Warn("删除双池 ready manifest 失败: " + err.Error())
	}
}

// RefreshDualVectorReadyManifestFromStores 对齐 _refresh_dual_vector_ready_manifest_from_stores
func (s *DualVectorStateService) RefreshDualVectorReadyManifestFromStores(ctx context.Context) {
	paragraphCount := 0
	if store := s.paragraphStoreRaw(); store != nil {
		paragraphCount = store.NumVectors()
	}
	graphCount := 0
	if store := s.graphStoreRaw(); store != nil {
		graphCount = store.NumVectors()
	}
	entityCount := graphCount
	relationCount := 0
	if s.deps.MetadataStore != nil && s.deps.MetadataStore() != nil {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					s.logger().Warn(fmt.Sprintf("刷新双池 ready manifest 统计失败，使用向量池计数: %v", recovered))
				}
			}()
			targetCounts := map[string]int{}
			if s.deps.CountVectorRebuildTargets != nil {
				targetCounts = s.deps.CountVectorRebuildTargets(ctx)
			}
			entityCount = min(graphCount, targetCounts["entities"])
			if entityCount < 0 {
				entityCount = 0
			}
			relationCount = graphCount - entityCount
			if relationCount < 0 {
				relationCount = 0
			}
		}()
	}
	stats := map[string]map[string]int{
		"paragraphs": {"done": paragraphCount, "failed": 0},
		"entities":   {"done": entityCount, "failed": 0},
		"relations":  {"done": relationCount, "failed": 0},
	}
	migrationStats := map[string]map[string]int{
		"paragraphs": {"copied": 0, "encoded": 0, "missing": 0},
		"entities":   {"copied": 0, "encoded": 0, "missing": 0},
		"relations":  {"copied": 0, "encoded": 0, "missing": 0},
	}
	s.WriteDualVectorReadyManifest(ctx, stats, migrationStats, "", nil)
}

// ClearLegacySingleVectorFilesAfterDualReady 对齐 _clear_legacy_single_vector_files_after_dual_ready
func (s *DualVectorStateService) ClearLegacySingleVectorFilesAfterDualReady(ctx context.Context) {
	root := s.VectorsRoot(ctx)
	for _, filename := range []string{
		"vectors.bin",
		"vectors_ids.bin",
		"vectors.index",
		"vectors_metadata.json",
		"vectors_metadata.pkl",
	} {
		if err := os.Remove(filepath.Join(root, filename)); err != nil && !os.IsNotExist(err) {
			s.logger().Warn(fmt.Sprintf("清理旧单池向量文件失败: file=%s, error=%v", filename, err))
		}
	}
	if s.deps.ReadVectorStore != nil && s.deps.ReadVectorStore() != nil {
		if store, err := s.MakeVectorStore(ctx, root, nil); err == nil && s.deps.SetVectorStore != nil {
			s.deps.SetVectorStore(store)
		} else if err != nil {
			s.logger().Warn("重建单池向量存储失败: " + err.Error())
		}
	}
}

// PrepareDualVectorBuildDirs 对齐 _prepare_dual_vector_build_dirs
func (s *DualVectorStateService) PrepareDualVectorBuildDirs(ctx context.Context) (string, string, string) {
	buildRoot := filepath.Join(s.VectorsRoot(ctx), fmt.Sprintf("dual_build_%d", time.Now().UnixMilli()))
	if pathExists(buildRoot) {
		_ = os.RemoveAll(buildRoot)
	}
	paragraphDir := filepath.Join(buildRoot, "paragraph")
	graphDir := filepath.Join(buildRoot, "graph")
	if err := os.MkdirAll(paragraphDir, 0o777); err != nil {
		s.logger().Warn("创建双池临时构建目录失败: " + err.Error())
	}
	if err := os.MkdirAll(graphDir, 0o777); err != nil {
		s.logger().Warn("创建双池临时构建目录失败: " + err.Error())
	}
	return buildRoot, paragraphDir, graphDir
}

// ActivateDualVectorBuildDirs 对齐 _activate_dual_vector_build_dirs
func (s *DualVectorStateService) ActivateDualVectorBuildDirs(ctx context.Context, buildRoot string) {
	paragraphSrc := filepath.Join(buildRoot, "paragraph")
	graphSrc := filepath.Join(buildRoot, "graph")
	if !pathExists(paragraphSrc) || !pathExists(graphSrc) {
		s.logger().Error("dual vector build dirs missing")
		return
	}
	backupRoot := filepath.Join(s.VectorsRoot(ctx), fmt.Sprintf("dual_backup_%d", time.Now().UnixMilli()))
	backupParagraph := filepath.Join(backupRoot, "paragraph")
	backupGraph := filepath.Join(backupRoot, "graph")
	if err := os.MkdirAll(backupRoot, 0o777); err != nil {
		s.logger().Error("创建双池备份目录失败: " + err.Error())
		return
	}
	activationJournal := filepath.Join(backupRoot, "activation.json")
	paragraphDst := s.ParagraphVectorDir(ctx)
	graphDst := s.GraphVectorDir(ctx)
	journal := map[string]any{
		"status":        "prepared",
		"build_root":    buildRoot,
		"had_paragraph": pathExists(paragraphDst),
		"had_graph":     pathExists(graphDst),
	}
	if raw, err := json.Marshal(journal); err == nil {
		_ = os.WriteFile(activationJournal, raw, 0o666)
	}
	rollback := func() {
		_ = os.RemoveAll(paragraphDst)
		_ = os.RemoveAll(graphDst)
		if pathExists(backupParagraph) {
			_ = os.Rename(backupParagraph, paragraphDst)
		}
		if pathExists(backupGraph) {
			_ = os.Rename(backupGraph, graphDst)
		}
		_ = os.RemoveAll(backupRoot)
	}
	if pathExists(paragraphDst) {
		if err := os.Rename(paragraphDst, backupParagraph); err != nil {
			rollback()
			return
		}
	}
	if pathExists(graphDst) {
		if err := os.Rename(graphDst, backupGraph); err != nil {
			rollback()
			return
		}
	}
	if err := os.Rename(paragraphSrc, paragraphDst); err != nil {
		rollback()
		return
	}
	if err := os.Rename(graphSrc, graphDst); err != nil {
		rollback()
		return
	}
	_ = os.RemoveAll(buildRoot)
	activated := map[string]any{
		"status":        "activated",
		"had_paragraph": pathExists(backupParagraph),
		"had_graph":     pathExists(backupGraph),
	}
	if raw, err := json.Marshal(activated); err == nil {
		_ = os.WriteFile(activationJournal, raw, 0o666)
	}
}

// CleanupStaleDualVectorBuildDirs 对齐 _cleanup_stale_dual_vector_build_dirs
func (s *DualVectorStateService) CleanupStaleDualVectorBuildDirs(ctx context.Context) {
	vectorsRoot := s.VectorsRoot(ctx)
	if !pathExists(vectorsRoot) {
		return
	}
	entries, err := os.ReadDir(vectorsRoot)
	if err != nil {
		return
	}
	backupDirs := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "dual_backup_") {
			backupDirs = append(backupDirs, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(backupDirs)))
	unresolvedBackup := false
	for _, name := range backupDirs {
		backupRoot := filepath.Join(vectorsRoot, name)
		journalPath := filepath.Join(backupRoot, "activation.json")
		if !pathExists(journalPath) {
			unresolvedBackup = true
			s.logger().Error("发现缺少激活日志的双池备份，已保留供人工恢复: " + backupRoot)
			continue
		}
		if s.DualVectorReady(ctx, nil) {
			_ = os.RemoveAll(backupRoot)
			continue
		}
		journal := map[string]any{}
		raw, readErr := os.ReadFile(journalPath)
		if readErr == nil {
			_ = json.Unmarshal(raw, &journal)
		}
		status := strings.ToLower(strings.TrimSpace(asString(journal["status"])))
		switch status {
		case "activated":
			_ = os.RemoveAll(backupRoot)
			s.logger().Warn("检测到已完成但尚未清理的双池激活，已保留当前目录并清理旧备份")
			continue
		case "prepared":
		default:
			unresolvedBackup = true
			s.logger().Error(fmt.Sprintf("恢复双池激活备份失败，已保留现场: backup=%s, error=%s", backupRoot, fmt.Sprintf("双池激活日志状态无效: %s", statusOrMissing(status))))
			continue
		}
		hadParagraph := pyTruthy(journal["had_paragraph"])
		if _, ok := journal["had_paragraph"]; !ok {
			hadParagraph = true
		}
		hadGraph := pyTruthy(journal["had_graph"])
		if _, ok := journal["had_graph"]; !ok {
			hadGraph = true
		}
		interruptedRoot := filepath.Join(backupRoot, "interrupted_new")
		_ = os.MkdirAll(interruptedRoot, 0o777)
		restoreFailed := false
		for _, item := range []struct {
			name        string
			destination string
			existed     bool
		}{
			{"paragraph", s.ParagraphVectorDir(ctx), hadParagraph},
			{"graph", s.GraphVectorDir(ctx), hadGraph},
		} {
			if pathExists(item.destination) {
				if err := os.Rename(item.destination, filepath.Join(interruptedRoot, item.name)); err != nil {
					restoreFailed = true
					break
				}
			}
			backupPath := filepath.Join(backupRoot, item.name)
			if item.existed {
				if !pathExists(backupPath) {
					restoreFailed = true
					break
				}
				if err := os.Rename(backupPath, item.destination); err != nil {
					restoreFailed = true
					break
				}
			}
		}
		if restoreFailed {
			unresolvedBackup = true
			s.logger().Error("恢复双池激活备份失败，已保留现场: backup=" + backupRoot)
			continue
		}
		_ = os.RemoveAll(backupRoot)
		s.logger().Warn("检测到未完成的双池激活，已恢复切换前目录")
	}
	if unresolvedBackup {
		return
	}
	entries, err = os.ReadDir(vectorsRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "dual_build_") {
			_ = os.RemoveAll(filepath.Join(vectorsRoot, entry.Name()))
		}
	}
}

// MakeVectorStore 对齐 _make_vector_store
func (s *DualVectorStateService) MakeVectorStore(ctx context.Context, dataDir string, dimension *int) (*storage.VectorStore, error) {
	currentDimension := 0
	if dimension != nil {
		currentDimension = *dimension
	}
	if currentDimension == 0 && s.deps.EmbeddingDimension != nil {
		currentDimension = s.deps.EmbeddingDimension()
	}
	if currentDimension < 1 {
		currentDimension = 1
	}
	return storage.NewVectorStore(storage.VectorStoreConfig{
		Dimension:        currentDimension,
		QuantizationType: memorixutils.QuantizationInt8,
		DataDir:          dataDir,
	})
}

// SaveVectorStore 对齐 _save_vector_store
func (s *DualVectorStateService) SaveVectorStore(ctx context.Context, store *storage.VectorStore) {
	if store == nil {
		return
	}
	rebuilding := false
	if s.deps.RebuildConfigurationKey != nil && s.deps.RebuildConfigurationKey() != "" {
		rebuilding = true
		if s.deps.EnsureRebuildEmbeddingUnchanged != nil {
			s.deps.EnsureRebuildEmbeddingUnchanged(ctx)
		}
	}
	var fingerprint map[string]any
	if rebuilding && s.deps.RebuildFingerprint != nil {
		fingerprint = s.deps.RebuildFingerprint()
	}
	if fingerprint == nil && s.deps.CurrentEmbeddingFingerprint != nil {
		fingerprint = s.deps.CurrentEmbeddingFingerprint(ctx, nil)
	}
	if err := store.Save("", fingerprint); err != nil {
		s.logger().Warn("保存向量存储失败: " + err.Error())
	}
}

// ReloadDualVectorStoresFromDisk 对齐 _reload_dual_vector_stores_from_disk
func (s *DualVectorStateService) ReloadDualVectorStoresFromDisk(ctx context.Context) bool {
	currentDimension := 0
	if s.deps.CurrentEmbeddingStatusDimension != nil {
		currentDimension = s.deps.CurrentEmbeddingStatusDimension(ctx)
	}
	if !s.DualVectorReady(ctx, &currentDimension) {
		s.TryRecoverDualReadyManifest(ctx)
	}
	if !s.DualVectorReady(ctx, &currentDimension) {
		if manifest := s.ReadDualVectorReadyManifest(ctx); manifest != nil && asString(manifest["status"]) == "ready" {
			manifestDimension := pyIntOr(manifest["dimension"], 0, -1<<62)
			paragraphCount := pyIntOr(manifest["paragraph_vectors"], 0, -1<<62)
			graphCount := pyIntOr(manifest["graph_vectors"], 0, -1<<62)
			var currentFingerprint map[string]any
			if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
				currentFingerprint = s.deps.CurrentEmbeddingFingerprintForValidation(ctx, nil)
			}
			var manifestFingerprint map[string]any
			if s.deps.NormalizeEmbeddingFingerprint != nil {
				manifestFingerprint = s.deps.NormalizeEmbeddingFingerprint(manifest["embedding_fingerprint"])
			}
			if manifestDimension != 0 && manifestDimension != currentDimension {
				s.logger().Error("双池 ready manifest 维度与当前 Embedding 不一致")
				return false
			}
			emptyRecoveryGeneration := paragraphCount == 0 && graphCount == 0 &&
				strings.TrimSpace(asString(manifest["generation_reason"])) == "integrity_recovery"
			if currentFingerprint == nil {
				s.logger().Error("当前 Embedding 指纹不可用，无法校验双池世代")
				return false
			}
			if manifestFingerprint == nil && !emptyRecoveryGeneration {
				s.logger().Error("双池 ready manifest 缺少 Embedding 指纹")
				return false
			}
			if manifestFingerprint != nil && asString(manifestFingerprint["hash"]) != asString(currentFingerprint["hash"]) {
				s.logger().Error("双池 ready manifest Embedding 指纹不匹配")
				return false
			}
			if !pathExists(s.ParagraphVectorDir(ctx)) || !pathExists(s.GraphVectorDir(ctx)) {
				s.logger().Error("双池 ready manifest 对应的向量目录不完整")
				return false
			}
		}
		if store, err := s.MakeVectorStore(ctx, s.ParagraphVectorDir(ctx), nil); err == nil {
			if s.deps.SetParagraphVectorStore != nil {
				s.deps.SetParagraphVectorStore(store)
			}
		}
		if store, err := s.MakeVectorStore(ctx, s.GraphVectorDir(ctx), nil); err == nil {
			if s.deps.SetGraphVectorStore != nil {
				s.deps.SetGraphVectorStore(store)
			}
		}
		if s.deps.SetDualVectorPoolsReady != nil {
			s.deps.SetDualVectorPoolsReady(false)
		}
		return false
	}
	paragraphStore, paragraphErr := s.MakeVectorStore(ctx, s.ParagraphVectorDir(ctx), nil)
	if paragraphErr != nil {
		if s.deps.SetDualVectorPoolsReady != nil {
			s.deps.SetDualVectorPoolsReady(false)
		}
		s.logger().Warn("加载双池向量存储失败: " + paragraphErr.Error())
		return false
	}
	graphStore, graphErr := s.MakeVectorStore(ctx, s.GraphVectorDir(ctx), nil)
	if graphErr != nil {
		if s.deps.SetDualVectorPoolsReady != nil {
			s.deps.SetDualVectorPoolsReady(false)
		}
		s.logger().Warn("加载双池向量存储失败: " + graphErr.Error())
		return false
	}
	var expectedFingerprint map[string]any
	if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
		expectedFingerprint = s.deps.CurrentEmbeddingFingerprintForValidation(ctx, nil)
	}
	evidenceRoot := ""
	if s.deps.ReconcileV1EvidenceRoot != nil {
		evidenceRoot = s.deps.ReconcileV1EvidenceRoot(ctx)
	}
	if paragraphStore.HasData() {
		var validHashes []string
		if s.deps.V1ValidHashesForPool != nil {
			validHashes = s.deps.V1ValidHashesForPool(ctx, "paragraph")
		}
		if err := paragraphStore.Load(s.ParagraphVectorDir(ctx), expectedFingerprint, validHashes, evidenceRoot); err != nil {
			if s.deps.SetDualVectorPoolsReady != nil {
				s.deps.SetDualVectorPoolsReady(false)
			}
			s.logger().Warn("加载段池向量失败: " + err.Error())
			return false
		}
		paragraphStore.WarmupIndex(true)
	}
	if graphStore.HasData() {
		var validHashes []string
		if s.deps.V1ValidHashesForPool != nil {
			validHashes = s.deps.V1ValidHashesForPool(ctx, "graph")
		}
		if err := graphStore.Load(s.GraphVectorDir(ctx), expectedFingerprint, validHashes, evidenceRoot); err != nil {
			if s.deps.SetDualVectorPoolsReady != nil {
				s.deps.SetDualVectorPoolsReady(false)
			}
			s.logger().Warn("加载图池向量失败: " + err.Error())
			return false
		}
		graphStore.WarmupIndex(true)
	}
	if s.deps.SetParagraphVectorStore != nil {
		s.deps.SetParagraphVectorStore(paragraphStore)
	}
	if s.deps.SetGraphVectorStore != nil {
		s.deps.SetGraphVectorStore(graphStore)
	}
	if s.deps.SetDualVectorPoolsReady != nil {
		s.deps.SetDualVectorPoolsReady(true)
	}
	return true
}

// TryRecoverDualReadyManifest 对齐 _try_recover_dual_ready_manifest
func (s *DualVectorStateService) TryRecoverDualReadyManifest(ctx context.Context) bool {
	if !s.DualVectorPoolsConfigEnabled(ctx) {
		return false
	}
	if s.deps.MetadataStore == nil || s.deps.MetadataStore() == nil {
		return false
	}
	if pathExists(s.DualVectorReadyManifestPath(ctx)) {
		return false
	}
	paragraphDir := s.ParagraphVectorDir(ctx)
	graphDir := s.GraphVectorDir(ctx)
	if !pathExists(paragraphDir) || !pathExists(graphDir) {
		return false
	}
	paragraphStore, err := s.MakeVectorStore(ctx, paragraphDir, nil)
	if err != nil || !paragraphStore.HasData() {
		return false
	}
	graphStore, err := s.MakeVectorStore(ctx, graphDir, nil)
	if err != nil || !graphStore.HasData() {
		return false
	}
	var expectedFingerprint map[string]any
	if s.deps.CurrentEmbeddingFingerprintForValidation != nil {
		expectedFingerprint = s.deps.CurrentEmbeddingFingerprintForValidation(ctx, nil)
	}
	if expectedFingerprint == nil {
		s.logger().Warn("双池 ready manifest 缺失且 Embedding 指纹尚未经过真实请求确认")
		return false
	}
	evidenceRoot := ""
	if s.deps.ReconcileV1EvidenceRoot != nil {
		evidenceRoot = s.deps.ReconcileV1EvidenceRoot(ctx)
	}
	if paragraphStore.HasData() {
		var validHashes []string
		if s.deps.V1ValidHashesForPool != nil {
			validHashes = s.deps.V1ValidHashesForPool(ctx, "paragraph")
		}
		if err := paragraphStore.Load(paragraphDir, expectedFingerprint, validHashes, evidenceRoot); err != nil {
			s.logger().Warn("双池 ready manifest 自愈失败，加载向量池异常: " + err.Error())
			return false
		}
	}
	if graphStore.HasData() {
		var validHashes []string
		if s.deps.V1ValidHashesForPool != nil {
			validHashes = s.deps.V1ValidHashesForPool(ctx, "graph")
		}
		if err := graphStore.Load(graphDir, expectedFingerprint, validHashes, evidenceRoot); err != nil {
			s.logger().Warn("双池 ready manifest 自愈失败，加载向量池异常: " + err.Error())
			return false
		}
	}
	paragraphCompatible := true
	graphCompatible := true
	if s.deps.StoredVectorsCompatibleWithCurrentEmbedding != nil {
		paragraphCompatible = s.deps.StoredVectorsCompatibleWithCurrentEmbedding(ctx, paragraphStore)
		graphCompatible = s.deps.StoredVectorsCompatibleWithCurrentEmbedding(ctx, graphStore)
	}
	if !paragraphCompatible || !graphCompatible {
		s.logger().Warn("双池 ready manifest 缺失且向量池指纹无法确认或不匹配，保持单池降级")
		return false
	}
	counts := map[string]int{}
	if s.deps.CountVectorRebuildTargets != nil {
		counts = s.deps.CountVectorRebuildTargets(ctx)
	}
	expectedParagraphs := counts["paragraphs"]
	expectedGraph := counts["entities"]
	relationEnabled := s.deps.RelationVectorsEnabled != nil && s.deps.RelationVectorsEnabled()
	if relationEnabled {
		expectedGraph += counts["relations"]
	}
	if paragraphStore.NumVectors() != expectedParagraphs || graphStore.NumVectors() != expectedGraph {
		s.logger().Warn(fmt.Sprintf(
			"双池 ready manifest 缺失且向量数量不匹配，保持单池降级: paragraph=%d/%d, graph=%d/%d",
			paragraphStore.NumVectors(), expectedParagraphs, graphStore.NumVectors(), expectedGraph))
		return false
	}
	stats := map[string]map[string]int{
		"paragraphs": {"done": expectedParagraphs, "failed": 0},
		"entities":   {"done": counts["entities"], "failed": 0},
		"relations":  {"done": 0, "failed": 0},
	}
	if relationEnabled {
		stats["relations"] = map[string]int{"done": counts["relations"], "failed": 0}
	}
	migrationStats := map[string]map[string]int{
		"paragraphs": {"copied": 0, "encoded": 0, "missing": 0},
		"entities":   {"copied": 0, "encoded": 0, "missing": 0},
		"relations":  {"copied": 0, "encoded": 0, "missing": 0},
	}
	s.WriteDualVectorReadyManifest(ctx, stats, migrationStats, "", nil)
	s.logger().Warn("检测到双池目录完整但 ready manifest 缺失，已自动重建 manifest")
	return true
}

// DropDualBuildRoot 对齐 _drop_dual_build_root
func (s *DualVectorStateService) DropDualBuildRoot(ctx context.Context, buildRoot string) {
	if buildRoot == "" {
		return
	}
	if err := os.RemoveAll(buildRoot); err != nil {
		s.logger().Warn("清理双池临时构建目录失败: " + err.Error())
	}
}

// GraphVectorID 对齐 _graph_vector_id
func (s *DualVectorStateService) GraphVectorID(itemType string, hashValue string) string {
	return strings.TrimSpace(itemType) + ":" + strings.TrimSpace(hashValue)
}

// ParagraphStore 对齐 _paragraph_store
func (s *DualVectorStateService) ParagraphStore(ctx context.Context) *storage.VectorStore {
	if s.DualVectorPoolsEnabled(ctx) {
		if store := s.paragraphStoreRaw(); store != nil {
			return store
		}
		return s.vectorStoreRaw()
	}
	return s.vectorStoreRaw()
}

// GraphVectorStore 对齐 _graph_vector_store
func (s *DualVectorStateService) GraphVectorStore(ctx context.Context) *storage.VectorStore {
	if s.DualVectorPoolsEnabled(ctx) {
		if store := s.graphStoreRaw(); store != nil {
			return store
		}
		return s.vectorStoreRaw()
	}
	return s.vectorStoreRaw()
}

func (s *DualVectorStateService) paragraphStoreRaw() *storage.VectorStore {
	if s.deps.ReadParagraphVectorStore == nil {
		return nil
	}
	return s.deps.ReadParagraphVectorStore()
}

func (s *DualVectorStateService) graphStoreRaw() *storage.VectorStore {
	if s.deps.ReadGraphVectorStore == nil {
		return nil
	}
	return s.deps.ReadGraphVectorStore()
}

func (s *DualVectorStateService) vectorStoreRaw() *storage.VectorStore {
	if s.deps.ReadVectorStore == nil {
		return nil
	}
	return s.deps.ReadVectorStore()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func statusOrMissing(status string) string {
	if status == "" {
		return "missing"
	}
	return status
}
