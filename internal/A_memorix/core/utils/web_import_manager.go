package utils

// Web 导入任务管理器，对齐 web_import_manager.py 的依赖与运行时配置

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WebImportVectorStore Web 导入使用的向量存储
type WebImportVectorStore interface {
	Dimension() int
	Contains(id string) bool
	Add(vectors [][]float32, ids []string) (int, error)
	Save(embeddingFingerprint map[string]any) error
	HasData() bool
	Load() error
}

// WebImportGraphStore Web 导入使用的图存储
type WebImportGraphStore interface {
	AddNodes(names []string) error
	AddEdges(relations [][3]string, relationHashes []string) error
	BatchUpdate(fn func() error) error
	HasData() bool
	Load() error
	Save() error
}

// WebImportEmbedding 嵌入接口
type WebImportEmbedding interface {
	Encode(ctx context.Context, text string) ([]float32, error)
	EncodeBatch(ctx context.Context, texts []string) ([][]float32, error)
	DetectDimension(ctx context.Context) (int, error)
}

// WebImportStore Web 导入需要的元数据存储能力
type WebImportStore interface {
	AddParagraph(content string, source string, metadata map[string]any, knowledgeType string, timeMeta map[string]any) (string, error)
	AddEntitiesBatch(names []string, sourceParagraph string) ([]string, error)
	AddRelationsBatch(relations [][3]string, confidence float64, sourceParagraph string) ([]string, error)
	AddRelation(subject string, predicate string, object string, confidence float64, sourceParagraph string) (string, error)
	SetRelationVectorState(hash string, state string, errorText string, bumpRetry bool) error
	GetLiveParagraphsBySource(source string) ([]map[string]any, error)
}

// WebImportCommandRunner 外部迁移/转换命令执行器
type WebImportCommandRunner interface {
	Run(ctx context.Context, command []string, workingDir string) (exitCode int, stdout string, stderr string, err error)
}

// WebImportNativeScriptRunner 标记脚本已由 Go 原生实现，可跳过 Python 脚本文件检查
type WebImportNativeScriptRunner interface {
	WebImportCommandRunner
	// HasScript 判断脚本名（可带 .py）是否有原生实现
	HasScript(name string) bool
}

// WebImportDeps Web 导入管理器依赖，全部由调用方注入
type WebImportDeps struct {
	Config       ConfigGetter
	Store        WebImportStore
	Graph        WebImportGraphStore
	ParagraphVec WebImportVectorStore
	GraphVec     WebImportVectorStore
	Embedding    WebImportEmbedding
	LLM          TextGenerationGateway

	ModelTasks     map[string]TextGenerationTaskConfig
	ModelDict      map[string]any
	Logger         Logger
	DataDir        string
	ImportRoot     string
	RepoRoot       string
	ScriptsRoot    string
	MaibotSourceDB string
	MigrationPath  string
	ConvertPath    string

	RuntimeReady          func() bool
	AllowMetadataOnly     func() bool
	EmbeddingDegraded     func() bool
	DualVectorPools       func() bool
	PersistVectorStore    func(store WebImportVectorStore)
	EnqueueBackfill       func(paragraphHash string, errorText string) error
	RecordPersonEvidence  func(personIDs []string, reason string)
	SelfCheck             func(ctx context.Context) RuntimeSelfCheckReport
	RelationWriter        SummaryRelationWriter
	StrategySelector      func(content string, override string, chatLog bool) (string, error)
	KnowledgeTypeResolver func(value any, content string) (string, error)
	CommandRunner         WebImportCommandRunner
	WriteChanged          func(payload map[string]any)
	Now                   func() float64

	PythonExecutable string
	VerifyConvert    func(ctx context.Context, outputDir string) (map[string]any, error)
	TemporalBackfill func(ctx context.Context, targetDir string, limit int, dryRun bool, noCreatedFallback bool) (map[string]any, error)
}

// WebImportManager Web 导入任务管理器
type WebImportManager struct {
	deps WebImportDeps

	mu           sync.Mutex
	storageMu    sync.Mutex
	tasks        map[string]*WebImportTaskRecord
	taskOrder    []string
	queue        []string
	activeTaskID string
	stopping     bool
	workerActive bool

	importRoot     string
	tempRoot       string
	reportsRoot    string
	manifestPath   string
	manifest       map[string]any
	manifestLoad   bool
	selfCheckCache SelfCheckCache
}

// NewWebImportManager 构造管理器并补齐默认依赖
func NewWebImportManager(deps WebImportDeps) *WebImportManager {
	if deps.Config == nil {
		deps.Config = func(key string, defaultValue any) any { return defaultValue }
	}
	if deps.Logger == nil {
		deps.Logger = NopLogger{}
	}
	if deps.Now == nil {
		deps.Now = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }
	}
	if deps.ModelTasks == nil {
		deps.ModelTasks = map[string]TextGenerationTaskConfig{}
	}
	if deps.ModelDict == nil {
		deps.ModelDict = map[string]any{}
	}
	if deps.AllowMetadataOnly == nil {
		deps.AllowMetadataOnly = func() bool { return true }
	}
	if deps.EmbeddingDegraded == nil {
		deps.EmbeddingDegraded = func() bool { return false }
	}
	if deps.DualVectorPools == nil {
		deps.DualVectorPools = func() bool { return deps.Config("retrieval.vector_pools.mode", "dual") == "dual" }
	}
	if deps.DataDir == "" {
		deps.DataDir = "./data"
	}
	dataDir, err := filepath.Abs(deps.DataDir)
	if err == nil {
		deps.DataDir = dataDir
	}
	if deps.ImportRoot == "" {
		deps.ImportRoot = filepath.Join(deps.DataDir, "imports")
	}
	importRoot, err := filepath.Abs(deps.ImportRoot)
	if err == nil {
		deps.ImportRoot = importRoot
	}
	if deps.RepoRoot == "" {
		deps.RepoRoot = "."
	}
	manager := &WebImportManager{
		deps:         deps,
		tasks:        map[string]*WebImportTaskRecord{},
		importRoot:   deps.ImportRoot,
		tempRoot:     filepath.Join(deps.ImportRoot, "tasks"),
		reportsRoot:  filepath.Join(deps.ImportRoot, "reports"),
		manifestPath: filepath.Join(deps.ImportRoot, "manifest.json"),
		manifest:     map[string]any{},
	}
	manager.webImportMigrateLegacyState()
	_ = os.MkdirAll(manager.tempRoot, 0o755)
	_ = os.MkdirAll(manager.reportsRoot, 0o755)
	for _, dir := range manager.defaultPathAliases() {
		_ = os.MkdirAll(dir, 0o755)
	}
	return manager
}

// now 返回当前时间戳
func (m *WebImportManager) now() float64 { return m.deps.Now() }

// cfg 读取配置
func (m *WebImportManager) cfg(key string, defaultValue any) any {
	if m.deps.Config == nil {
		return defaultValue
	}
	return m.deps.Config(key, defaultValue)
}

// cfgInt 对齐 _cfg_int
func (m *WebImportManager) cfgInt(key string, defaultValue int) int {
	return webImportCoerceInt(m.cfg(key, defaultValue), defaultValue)
}

// cfgFloat 对齐 _cfg_float
func (m *WebImportManager) cfgFloat(key string, defaultValue float64) float64 {
	return webImportCoerceFloat(m.cfg(key, defaultValue), defaultValue)
}

// webImportMigrateLegacyState 对齐 _migrate_legacy_import_state
func (m *WebImportManager) webImportMigrateLegacyState() {
	pairs := [][2]string{
		{filepath.Join(m.deps.DataDir, "import_manifest.json"), m.manifestPath},
		{filepath.Join(m.deps.DataDir, "web_import_reports"), m.reportsRoot},
	}
	for _, pair := range pairs {
		source, target := pair[0], pair[1]
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		if err := os.Rename(source, target); err != nil {
			m.deps.Logger.Warning(fmt.Sprintf("旧导入状态迁移失败，保留原文件继续启动: %s, error=%v", source, err))
			continue
		}
		m.deps.Logger.Info(fmt.Sprintf("已迁移旧导入状态: %s -> %s", source, target))
	}
}

// resolveDataDir 对齐 _resolve_data_dir
func (m *WebImportManager) resolveDataDir() string { return m.deps.DataDir }

// resolveRepoRoot 对齐 _resolve_repo_root
func (m *WebImportManager) resolveRepoRoot() string { return m.deps.RepoRoot }

// hasNativeScript 判断命令执行器是否已用 Go 实现指定脚本
func (m *WebImportManager) hasNativeScript(name string) bool {
	runner, ok := m.deps.CommandRunner.(WebImportNativeScriptRunner)
	return ok && runner.HasScript(name)
}

// resolveMigrationScript 对齐 _resolve_migration_script
func (m *WebImportManager) resolveMigrationScript() string {
	if m.deps.MigrationPath != "" {
		return m.deps.MigrationPath
	}
	return filepath.Join(m.deps.RepoRoot, "scripts", "migrate_maibot_memory.py")
}

// resolveConvertScript 对齐 _resolve_convert_script
func (m *WebImportManager) resolveConvertScript() string {
	if m.deps.ConvertPath != "" {
		return m.deps.ConvertPath
	}
	return filepath.Join(m.deps.RepoRoot, "scripts", "convert_lpmm.py")
}

// defaultMaibotSourceDB 对齐 _default_maibot_source_db
func (m *WebImportManager) defaultMaibotSourceDB() string {
	if m.deps.MaibotSourceDB != "" {
		return m.deps.MaibotSourceDB
	}
	return filepath.Join(m.deps.RepoRoot, "data", "MaiBot.db")
}

// defaultPathAliases 对齐 _default_path_aliases
func (m *WebImportManager) defaultPathAliases() map[string]string {
	root := m.importRoot
	return map[string]string{
		"raw":       filepath.Join(root, "source", "raw"),
		"lpmm":      filepath.Join(root, "source", "lpmm"),
		"maibot":    filepath.Join(root, "source", "maibot"),
		"converted": filepath.Join(root, "converted"),
	}
}

// GetPathAliases 对齐 get_path_aliases
func (m *WebImportManager) GetPathAliases() map[string]string {
	aliases := m.defaultPathAliases()
	out := map[string]string{}
	for key, value := range aliases {
		out[key] = value
	}
	return out
}

// ResolvePathAlias 对齐 resolve_path_alias
func (m *WebImportManager) ResolvePathAlias(alias string, relativePath string, mustExist bool) (string, error) {
	aliasKey := strings.TrimSpace(alias)
	aliases := m.defaultPathAliases()
	root, ok := aliases[aliasKey]
	if !ok {
		return "", fmt.Errorf("未知路径别名: %s", aliasKey)
	}
	root, _ = filepath.Abs(root)
	root = filepath.Clean(root)
	rel := strings.TrimSpace(relativePath)
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "//") {
		return "", fmt.Errorf("relative_path 不能为绝对路径")
	}
	if strings.Contains(rel, ":") {
		return "", fmt.Errorf("relative_path 不允许包含盘符")
	}
	candidate := root
	if rel != "" {
		candidate = filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越界：relative_path 超出白名单目录")
	}
	if mustExist {
		if _, err := os.Stat(candidate); err != nil {
			return "", fmt.Errorf("路径不存在: %s", candidate)
		}
	}
	return candidate, nil
}

// ResolvePathRequest 对齐 resolve_path_request
func (m *WebImportManager) ResolvePathRequest(payload map[string]any) (map[string]any, error) {
	alias := strings.TrimSpace(importValueText(payload["alias"]))
	relativePath := strings.TrimSpace(importValueText(payload["relative_path"]))
	mustExist := webImportCoerceBool(payload["must_exist"], true)
	resolved, err := m.ResolvePathAlias(alias, relativePath, mustExist)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Stat(resolved)
	return map[string]any{
		"alias":         alias,
		"relative_path": relativePath,
		"resolved_path": resolved,
		"exists":        statErr == nil,
		"is_file":       statErr == nil && !info.IsDir(),
		"is_dir":        statErr == nil && info.IsDir(),
	}, nil
}

// allowMetadataOnlyWrite 对齐 _allow_metadata_only_write
func (m *WebImportManager) allowMetadataOnlyWrite() bool {
	if m.deps.AllowMetadataOnly != nil {
		return m.deps.AllowMetadataOnly()
	}
	return webImportCoerceBool(m.cfg("embedding.fallback.allow_metadata_only_write", true), true)
}

// DualVectorPoolsEnabled 对齐 _dual_vector_pools_enabled
func (m *WebImportManager) DualVectorPoolsEnabled() bool {
	if m.deps.DualVectorPools != nil {
		return m.deps.DualVectorPools()
	}
	mode := strings.ToLower(strings.TrimSpace(importValueText(m.cfg("retrieval.vector_pools.mode", "dual"))))
	if mode == "" {
		mode = "dual"
	}
	return mode == "dual"
}

// paragraphVectorStore 对齐 _paragraph_vector_store
func (m *WebImportManager) paragraphVectorStore() WebImportVectorStore {
	if m.DualVectorPoolsEnabled() && m.deps.ParagraphVec != nil {
		return m.deps.ParagraphVec
	}
	return m.deps.ParagraphVec
}

// graphVectorStore 对齐 _graph_vector_store
func (m *WebImportManager) graphVectorStore() WebImportVectorStore {
	if m.DualVectorPoolsEnabled() && m.deps.GraphVec != nil {
		return m.deps.GraphVec
	}
	return m.deps.ParagraphVec
}

// graphVectorID 对齐 _graph_vector_id
func (m *WebImportManager) graphVectorID(kind string, hashValue string) string {
	if !m.DualVectorPoolsEnabled() {
		return ""
	}
	return strings.TrimSpace(kind) + ":" + strings.TrimSpace(hashValue)
}

// embeddingWriteBatchSize 对齐 _embedding_write_batch_size
func (m *WebImportManager) embeddingWriteBatchSize() int {
	batchSize := max(1, m.cfgInt("embedding.batch_size", 32))
	concurrency := max(1, m.cfgInt("embedding.max_concurrent", 1))
	return min(512, batchSize*concurrency)
}

// vectorStoresForPersistence 对齐 _vector_stores_for_persistence
func (m *WebImportManager) vectorStoresForPersistence() []WebImportVectorStore {
	stores := []WebImportVectorStore{}
	seen := map[WebImportVectorStore]struct{}{}
	for _, store := range []WebImportVectorStore{m.paragraphVectorStore(), m.graphVectorStore()} {
		if store == nil {
			continue
		}
		if _, ok := seen[store]; ok {
			continue
		}
		seen[store] = struct{}{}
		stores = append(stores, store)
	}
	return stores
}

// saveRuntimeStoresLocked 对齐 _save_runtime_stores_locked
func (m *WebImportManager) saveRuntimeStoresLocked() {
	for _, store := range m.vectorStoresForPersistence() {
		if m.deps.PersistVectorStore != nil {
			m.deps.PersistVectorStore(store)
			continue
		}
		var fingerprint map[string]any
		if provider, ok := m.deps.Embedding.(interface {
			GetEmbeddingFingerprint(int) map[string]any
		}); ok {
			fingerprint = provider.GetEmbeddingFingerprint(store.Dimension())
		}
		_ = store.Save(fingerprint)
	}
	if m.deps.Graph != nil {
		_ = m.deps.Graph.Save()
	}
}

var _ = context.Background
