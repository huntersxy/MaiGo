package services

// 知识包管理服务，对齐 Python core/runtime/services/bundle_admin_service.py 的导出与检查段

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	imagememory "MaiGo/internal/A_memorix/core/image"
	"MaiGo/internal/A_memorix/core/storage"
)

const (
	// BundleFormat 记忆包格式标识
	BundleFormat = "a-memorix-memory-bundle"
	// BundleFormatVersion 当前记忆包格式版本
	BundleFormatVersion = 2
	// BundleLegacyFormatVersion 兼容的旧版记忆包格式版本
	BundleLegacyFormatVersion = 1
	// BundleExtension 记忆包扩展名
	BundleExtension = ".amembundle"
	// MaxBundleMembers 记忆包最大成员数
	MaxBundleMembers = 4096
	// MaxBundleMemberBytes 单个成员解压上限
	MaxBundleMemberBytes = 256 * 1024 * 1024
	// MaxBundleUncompressedBytes 整体解压上限
	MaxBundleUncompressedBytes = 1024 * 1024 * 1024
)

// fullStateTableOrder 完整记忆包的表顺序，与 Python 常量一致
var fullStateTableOrder = []string{
	"paragraphs", "entities", "relations", "deleted_relations", "paragraph_entities",
	"paragraph_relations", "external_memory_refs", "episodes", "episode_paragraphs",
	"fact_claims", "fact_evidence", "fact_transitions", "person_profile_snapshots",
	"person_profile_overrides", "person_profile_alias_overrides",
}

// resourcePrimaryKeys 资源表主键
var resourcePrimaryKeys = map[string][]string{
	"paragraphs":                     {"hash"},
	"entities":                       {"hash"},
	"relations":                      {"hash"},
	"deleted_relations":              {"hash"},
	"paragraph_entities":             {"paragraph_hash", "entity_hash"},
	"paragraph_relations":            {"paragraph_hash", "relation_hash"},
	"external_memory_refs":           {"external_id"},
	"episodes":                       {"episode_id"},
	"episode_paragraphs":             {"episode_id", "paragraph_hash"},
	"fact_claims":                    {"claim_id"},
	"fact_evidence":                  {"claim_id", "evidence_type", "evidence_id", "stance"},
	"fact_transitions":               {"transition_id"},
	"person_profile_snapshots":       {"snapshot_id"},
	"person_profile_overrides":       {"person_id"},
	"person_profile_alias_overrides": {"person_id"},
}

// fullStateExclusions 完整记忆包的排除说明
var fullStateExclusions = []string{
	"FTS/N-gram/关系图快照会在目标实例重建",
	"后台队列、运行时任务、反馈和删除审计不属于可移植语义状态",
	"聊天流开关、活跃人物集合等实例配置不会随包迁移",
}

var bundleUnsafeChars = regexp.MustCompile(`[^0-9A-Za-z._\-\x{4e00}-\x{9fff}]`)

// BundleAdminDeps 知识包服务的可选运行时依赖
type BundleAdminDeps struct {
	// ImageRuntime 为空表示当前内核未启用图片记忆能力
	ImageRuntime *imagememory.ImageMemoryRuntime
	// ParagraphVectorStore 动态返回段落向量库，双池就绪后为段落池
	ParagraphVectorStore func() *storage.VectorStore
	// GraphVectorStore 动态返回图向量库，双池就绪后为图池
	GraphVectorStore func() *storage.VectorStore
	// GraphVectorID 生成图向量 ID，双池模式带类型前缀
	GraphVectorID func(itemType string, hashValue string) string
	// DualVectorPoolsEnabled 动态判断当前是否启用双向量池
	DualVectorPoolsEnabled func() bool
	// StoredEmbeddingFingerprint 读取向量库已落盘的 embedding 指纹
	StoredEmbeddingFingerprint func(store *storage.VectorStore) map[string]any
	// CurrentEmbeddingFingerprint 读取当前 embedding 指纹，用于包兼容判断
	CurrentEmbeddingFingerprint func() map[string]any
	// RelationVectorsEnabled 判断关系向量是否启用
	RelationVectorsEnabled func() bool
	// EnsureParagraphVector 为缺失向量补齐段落向量
	EnsureParagraphVector func(ctx context.Context, payload map[string]any) (bool, error)
	// EnsureEntityVector 为缺失向量补齐实体向量
	EnsureEntityVector func(ctx context.Context, payload map[string]any) (bool, error)
	// EnsureRelationVector 为缺失向量补齐关系向量
	EnsureRelationVector func(ctx context.Context, payload map[string]any) (bool, error)
	// PersistVectors 持久化向量库，对齐 Python _persist(force_vectors=True)
	PersistVectors func()
	// RebuildGraph 卸载后重建关系图与关系别名
	RebuildGraph func(ctx context.Context) (map[string]any, error)
}

// BundleAdminService 对齐 MemoryBundleAdminService
type BundleAdminService struct {
	*Base
	deps BundleAdminDeps
}

// NewBundleAdminService 构造知识包服务
func NewBundleAdminService(base *Base) *BundleAdminService {
	return &BundleAdminService{Base: base}
}

// NewBundleAdminServiceWithDeps 构造带可选依赖的知识包服务
func NewBundleAdminServiceWithDeps(base *Base, deps BundleAdminDeps) *BundleAdminService {
	return &BundleAdminService{Base: base, deps: deps}
}

// currentEmbeddingFingerprint 读取当前 embedding 指纹，用于包兼容判断
func (s *BundleAdminService) currentEmbeddingFingerprint() map[string]any {
	if s == nil || s.deps.CurrentEmbeddingFingerprint == nil {
		return nil
	}
	return s.deps.CurrentEmbeddingFingerprint()
}

// imageRuntime 返回图片记忆运行时，未接入时返回 nil
func (s *BundleAdminService) imageRuntime() *imagememory.ImageMemoryRuntime {
	if s == nil {
		return nil
	}
	return s.deps.ImageRuntime
}

// paragraphVectorStore 返回段落向量库，未接入时返回 nil
func (s *BundleAdminService) paragraphVectorStore() *storage.VectorStore {
	if s == nil || s.deps.ParagraphVectorStore == nil {
		return nil
	}
	return s.deps.ParagraphVectorStore()
}

// graphVectorStore 返回图向量库，未接入时返回 nil
func (s *BundleAdminService) graphVectorStore() *storage.VectorStore {
	if s == nil || s.deps.GraphVectorStore == nil {
		return nil
	}
	return s.deps.GraphVectorStore()
}

// dualVectorPoolsEnabled 判断当前是否启用双向量池
func (s *BundleAdminService) dualVectorPoolsEnabled() bool {
	if s == nil || s.deps.DualVectorPoolsEnabled == nil {
		return false
	}
	return s.deps.DualVectorPoolsEnabled()
}

// relationVectorsEnabled 判断关系向量是否启用
func (s *BundleAdminService) relationVectorsEnabled() bool {
	if s == nil || s.deps.RelationVectorsEnabled == nil {
		return false
	}
	return s.deps.RelationVectorsEnabled()
}

// graphVectorID 生成图向量 ID，未接入时退回裸 hash
func (s *BundleAdminService) graphVectorID(itemType string, hashValue string) string {
	if s == nil || s.deps.GraphVectorID == nil {
		return hashValue
	}
	if typed := s.deps.GraphVectorID(itemType, hashValue); typed != "" {
		return typed
	}
	return hashValue
}

// MemoryBundleAdmin 对齐 memory_bundle_admin
func (s *BundleAdminService) MemoryBundleAdmin(ctx context.Context, action string, payload map[string]any) (any, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	if s.Meta() == nil {
		return map[string]any{"success": false, "error": "metadata store 未初始化"}, nil
	}
	act := strings.ToLower(strings.TrimSpace(action))
	switch act {
	case "export":
		return s.exportBundle(payload), nil
	case "inspect":
		path, err := s.resolveBundlePath(firstNonEmpty(payload["path"], payload["file_name"]), false)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		loaded, err := s.loadBundle(path)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		summary := s.publicBundleSummary(loaded)
		summary["success"] = true
		summary["images"] = s.imageInspectSummary(loaded)
		return summary, nil
	case "import":
		return s.importBundle(payload), nil
	case "list":
		limit := intOr(payload["limit"], 50, 1)
		if limit > 200 {
			limit = 200
		}
		return s.listInstalled(limit), nil
	case "resolve_file":
		path, err := s.resolveBundlePath(firstNonEmpty(payload["file_name"], payload["path"]), true)
		if err != nil {
			return map[string]any{"success": false, "error": err.Error()}, nil
		}
		return map[string]any{"success": true, "path": path, "file_name": filepath.Base(path)}, nil
	case "uninstall":
		return s.uninstallBundle(ctx, firstNonEmpty(payload["installation_id"])), nil
	default:
		return map[string]any{"success": false, "error": fmt.Sprintf("不支持的 bundle action: %s", act)}, nil
	}
}

// bundlesRoot 返回知识包目录
func (s *BundleAdminService) bundlesRoot() string {
	return filepath.Join(s.DataDir(), "imports", "bundles")
}

// importsRoot 返回 imports 目录
func (s *BundleAdminService) importsRoot() string {
	return filepath.Join(s.DataDir(), "imports")
}

// resolveBundlePath 对齐 _resolve_bundle_path
func (s *BundleAdminService) resolveBundlePath(rawPath string, bundlesOnly bool) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("记忆包路径不能为空")
	}
	root := s.importsRoot()
	if bundlesOnly {
		root = s.bundlesRoot()
	}
	resolved := trimmed
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(s.bundlesRoot(), trimmed)
	}
	resolved = filepath.Clean(resolved)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("记忆包路径超出 A_Memorix imports 目录")
	}
	if strings.ToLower(filepath.Ext(resolved)) != BundleExtension {
		return "", fmt.Errorf("记忆包必须使用 %s 扩展名", BundleExtension)
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || info.IsDir() {
		return "", fmt.Errorf("记忆包不存在: %s", filepath.Base(resolved))
	}
	return resolved, nil
}

// selectParagraphs 对齐 _select_paragraphs，返回行与归一化选择器
func (s *BundleAdminService) selectParagraphs(selector map[string]any) ([]map[string]any, map[string]any, error) {
	selectorType := strings.ToLower(strings.TrimSpace(asString(selector["type"])))
	if selectorType == "" {
		selectorType = "all"
	}
	switch selectorType {
	case "all":
		rows, err := s.Meta().Query("SELECT * FROM paragraphs WHERE COALESCE(is_deleted, 0) = 0 ORDER BY created_at ASC, hash ASC")
		return rows, map[string]any{"type": "all", "values": []string{}, "precision": "all"}, err
	case "source":
		values := dedupeStrings(mergeTokens(selector["values"], selector["sources"], selector["value"], selector["source"]))
		if len(values) == 0 {
			return nil, nil, fmt.Errorf("source 选择器至少需要一个来源")
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
		params := make([]any, 0, len(values))
		for _, value := range values {
			params = append(params, value)
		}
		rows, err := s.Meta().Query("SELECT * FROM paragraphs WHERE COALESCE(is_deleted, 0) = 0 AND source IN ("+placeholders+") ORDER BY created_at ASC, hash ASC", params...)
		return rows, map[string]any{"type": "source", "values": values, "precision": "source"}, err
	case "chat":
		chatID := strings.TrimSpace(asString(firstNonEmpty(selector["chat_id"], selector["value"])))
		if chatID == "" {
			return nil, nil, fmt.Errorf("chat 选择器缺少 chat_id")
		}
		rows, err := s.Meta().Query("SELECT * FROM paragraphs WHERE COALESCE(is_deleted, 0) = 0 ORDER BY created_at ASC, hash ASC")
		if err != nil {
			return nil, nil, err
		}
		filtered := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			if paragraphMatchesChat(row, chatID) {
				filtered = append(filtered, row)
			}
		}
		return filtered, map[string]any{"type": "chat", "values": []string{chatID}, "precision": "chat"}, nil
	case "package":
		installationID := strings.TrimSpace(asString(firstNonEmpty(selector["installation_id"], selector["package_id"], selector["value"])))
		if installationID == "" {
			return nil, nil, fmt.Errorf("package 选择器缺少 installation_id 或 package_id")
		}
		rows, err := s.Meta().Query(
			`SELECT p.* FROM paragraphs p JOIN knowledge_package_paragraphs kpp ON kpp.paragraph_hash = p.hash
			 WHERE kpp.installation_id = ? AND COALESCE(p.is_deleted, 0) = 0 ORDER BY p.created_at ASC, p.hash ASC`, installationID)
		return rows, map[string]any{"type": "package", "values": []string{installationID}, "precision": "package"}, err
	case "import_task":
		return nil, nil, fmt.Errorf("import_task 选择器缺少导入任务管理器，当前运行时未接入")
	case "image", "images":
		values := dedupeStrings(mergeTokens(selector["content_hashes"], selector["values"]))
		return []map[string]any{}, map[string]any{"type": "image", "values": values, "precision": "exact"}, nil
	default:
		return nil, nil, fmt.Errorf("不支持的导出选择器: %s", selectorType)
	}
}

// paragraphMatchesChat 判断段落是否属于指定聊天流
func paragraphMatchesChat(row map[string]any, chatID string) bool {
	metadata := jsonObject(rowString(row, "metadata"))
	collect := func(value any) []string {
		switch typed := value.(type) {
		case string:
			return []string{typed}
		case []any:
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				out = append(out, asString(item))
			}
			return out
		default:
			return nil
		}
	}
	for _, key := range []string{"chat_ids", "session_ids", "stream_ids", "chat_id", "session_id", "stream_id"} {
		for _, value := range collect(metadata[key]) {
			if strings.TrimSpace(value) == chatID {
				return true
			}
		}
	}
	source := rowString(row, "source")
	if strings.HasPrefix(source, "chat_summary:") && strings.TrimPrefix(source, "chat_summary:") == chatID {
		return true
	}
	return false
}

// buildKnowledgeDocs 对齐 _build_knowledge_docs
func (s *BundleAdminService) buildKnowledgeDocs(paragraphRows []map[string]any) []any {
	docs := make([]any, 0, len(paragraphRows))
	for _, row := range paragraphRows {
		hashValue := rowString(row, "hash")
		metadata := jsonObject(rowString(row, "metadata"))
		timeMeta := map[string]any{}
		for _, key := range []string{"event_time", "event_time_start", "event_time_end", "time_granularity", "time_confidence"} {
			if value, ok := metadata[key]; ok && value != nil {
				timeMeta[key] = value
			}
		}
		triples := make([]any, 0)
		entities := []string{}
		relationRows, err := s.Meta().GetParagraphRelations(hashValue)
		if err == nil {
			for _, relationRow := range relationRows {
				subject := rowString(relationRow, "subject")
				predicate := rowString(relationRow, "predicate")
				object := rowString(relationRow, "object")
				if pyTruthy(relationRow["is_inactive"]) {
					continue
				}
				if subject != "" && predicate != "" && object != "" {
					triples = append(triples, []any{subject, predicate, object})
				}
			}
		}
		entityRows, err := s.Meta().GetParagraphEntities(hashValue)
		if err == nil {
			for _, entityRow := range entityRows {
				name := rowString(entityRow, "name")
				if name == "" {
					name = rowString(entityRow, "entity_name")
				}
				entities = append(entities, name)
			}
		}
		knowledgeType := "mixed"
		if stored := strings.TrimSpace(rowString(row, "knowledge_type")); stored != "" {
			knowledgeType = stored
		}
		docs = append(docs, map[string]any{
			"id":                 hashValue,
			"passage":            rowString(row, "content"),
			"extracted_triples":  triples,
			"extracted_entities": dedupeStrings(entities),
			"a_memorix": map[string]any{
				"knowledge_type": knowledgeType,
				"source":         rowString(row, "source"),
				"time_meta":      timeMeta,
				"metadata":       metadata,
			},
		})
	}
	return docs
}

// safeFileStem 对齐 _safe_file_stem
func safeFileStem(value string) string {
	stem := bundleUnsafeChars.ReplaceAllString(value, "-")
	stem = strings.Trim(stem, ".-")
	runes := []rune(stem)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	stem = string(runes)
	if stem == "" {
		return "a-memorix-memory"
	}
	return stem
}

// writeBundleZip 写入 deflate 压缩的记忆包
func writeBundleZip(target string, members map[string][]byte) error {
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(members[name]); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return os.WriteFile(target, buffer.Bytes(), 0o644)
}

// publicBundleSummary 对齐 _public_bundle_summary
func (s *BundleAdminService) publicBundleSummary(loaded *loadedBundle) map[string]any {
	manifest := loaded.Manifest
	packageInfo := asMap(manifest["package"])
	if packageInfo == nil {
		packageInfo = map[string]any{}
	}
	return map[string]any{
		"manifest":      manifest,
		"package":       packageInfo,
		"content_level": manifest["content_level"],
		"counts":        manifest["counts"],
		"file_name":     filepath.Base(loaded.Path),
		"size":          loaded.Size,
	}
}

// listInstalled 对齐 _list_installed
func (s *BundleAdminService) listInstalled(limit int) map[string]any {
	rows, err := s.Meta().Query(
		`SELECT kp.*, COUNT(kpp.doc_id) AS paragraph_count
		 FROM knowledge_packages kp LEFT JOIN knowledge_package_paragraphs kpp
		 ON kpp.installation_id = kp.installation_id
		 GROUP BY kp.installation_id ORDER BY kp.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return map[string]any{"success": false, "error": err.Error()}
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{}
		for key, value := range row {
			if key == "manifest_json" {
				continue
			}
			item[key] = value
		}
		counts, runtimeStatus, countErr := s.installationImageCounts(asString(row["installation_id"]))
		if countErr != nil {
			return map[string]any{"success": false, "error": countErr.Error()}
		}
		assets := intOr(counts["assets"], 0, 0)
		ready := intOr(counts["ready"], 0, 0)
		retrievalStatus := runtimeStatus
		if runtimeStatus == "ready" {
			if assets == ready {
				retrievalStatus = "ready"
			} else {
				retrievalStatus = "pending"
			}
		}
		item["images"] = map[string]any{
			"assets": assets, "occurrences": intOr(counts["occurrences"], 0, 0), "ready": ready,
			"content_status": asString(row["status"]), "retrieval_status": retrievalStatus,
		}
		items = append(items, item)
	}
	return map[string]any{"success": true, "items": items, "count": len(items)}
}

// readAllLimited 读取有限长度的成员内容
func readAllLimited(reader io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(reader, limit+1))
}

var _ = json.Marshal
