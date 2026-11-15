package services

// A_memorix 管理服务基座，对齐 Python core/runtime/services/base.py
// Python 侧 KernelServiceBase 用 __getattr__ 把属性访问代理到内核，Go 侧改为显式注入依赖端口

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"MaiGo/internal/A_memorix/core/storage"
	"MaiGo/internal/A_memorix/core/utils"
)

// Base 是所有管理服务共享的依赖容器
// 字段为 nil 时表示对应运行时能力未启用，服务必须显式失败而不是伪造成功
type Base struct {
	config  map[string]any
	dataDir string
	meta    Metadata
	graph   GraphStore
	logger  *slog.Logger
}

// NewBase 构造服务基座
func NewBase(config map[string]any, dataDir string, meta Metadata, graphStore GraphStore, logger *slog.Logger) *Base {
	if logger == nil {
		logger = slog.Default()
	}
	return &Base{config: config, dataDir: dataDir, meta: meta, graph: graphStore, logger: logger}
}

// Config 返回完整配置快照
func (b *Base) Config() map[string]any {
	if b == nil {
		return nil
	}
	return b.config
}

// DataDir 返回数据目录
func (b *Base) DataDir() string {
	if b == nil {
		return ""
	}
	return b.dataDir
}

// SetMeta 在内核初始化完成后回填元数据端口
func (b *Base) SetMeta(meta Metadata) {
	if b == nil {
		return
	}
	b.meta = meta
}

// SetGraph 在内核初始化完成后回填图存储端口
func (b *Base) SetGraph(graph GraphStore) {
	if b == nil {
		return
	}
	b.graph = graph
}

// Meta 返回元数据存储端口
func (b *Base) Meta() Metadata {
	if b == nil {
		return nil
	}
	return b.meta
}

// Graph 返回图存储端口
func (b *Base) Graph() GraphStore {
	if b == nil {
		return nil
	}
	return b.graph
}

// Logger 返回日志器
func (b *Base) Logger() *slog.Logger {
	if b == nil {
		return slog.Default()
	}
	return b.logger
}

// Cfg 按点分路径读取配置，对齐 Python _cfg 的嵌套取值语义
func (b *Base) Cfg(path string, def any) any {
	var current any = b.Config()
	if current == nil {
		return def
	}
	for _, segment := range strings.Split(path, ".") {
		container, ok := current.(map[string]any)
		if !ok {
			return def
		}
		value, exists := container[segment]
		if !exists {
			return def
		}
		current = value
	}
	if current == nil {
		return def
	}
	return current
}

// CfgMap 读取配置并把非对象值折叠为空对象
func (b *Base) CfgMap(path string) map[string]any {
	if value, ok := b.Cfg(path, nil).(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// LoggerWarn 统一告警输出，消息为中文
func (b *Base) LoggerWarn(message string, args ...any) {
	if b == nil || b.logger == nil {
		return
	}
	b.logger.Warn(fmt.Sprintf(message, args...))
}

// Metadata 是管理服务依赖的元数据存储端口，签名与 storage.Store 保持一致
// 只声明服务层实际调用的方法，便于测试用真实 SQLite 存储或精简替身
type Metadata interface {
	DataDir() string
	Query(sql string, params ...any) ([]map[string]any, error)
	Transaction(immediate bool, fn func(tx *storage.Tx) error) error

	GetParagraph(hashValue string) (map[string]any, error)
	GetParagraphsByHashes(hashValues []string) (map[string]map[string]any, error)
	SearchParagraphsByContent(contentQuery string) ([]map[string]any, error)
	GetParagraphEntities(paragraphHash string) ([]map[string]any, error)
	GetParagraphRelations(paragraphHash string) ([]map[string]any, error)
	GetParagraphsByEntity(entityName string) ([]map[string]any, error)

	GetEntity(hashValue string) (map[string]any, error)
	AddEntity(name string, vectorIndex *int, sourceParagraph string, metadata map[string]any) (string, error)
	AddRelation(subject string, predicate string, object string, vectorIndex *int, confidence float64, sourceParagraph string, metadata map[string]any) (string, error)
	ApplyRelationLifecycleEvent(hashes []string, event utils.RelationLifecycleEvent, policy utils.RelationLifecyclePolicy, now float64, strength float64) ([]map[string]any, error)
	GetRelations(subject, predicate, object string, includeInactive bool) ([]map[string]any, error)
	GetRelation(hashValue string, includeInactive bool) (map[string]any, error)
	GetDeletedRelation(hashValue string) (map[string]any, error)
	GetDeletedRelations(limit int) ([]map[string]any, error)
	GetRelationsByHashes(hashValues []string, includeInactive bool) (map[string]map[string]any, error)
	ComputeRelationHash(subject string, predicate string, object string) (string, error)
	GetParagraphHashesByRelationHashes(relationHashes []string) (map[string][]string, error)

	SearchRelationHashesByText(query string, limit int) ([]string, error)
	SearchDeletedRelationHashesByText(query string, limit int) ([]string, error)

	MarkAsDeleted(hashes []string, typeName string, reason string) (int, error)
	BackupAndDeleteRelations(hashes []string) (int, error)
	DeleteExternalMemoryRefsByParagraphs(paragraphHashes []string) ([]map[string]any, error)
	ListExternalMemoryRefsByParagraphs(paragraphHashes []string) ([]map[string]any, error)
	RestoreExternalMemoryRefs(refs []map[string]any) (int, error)
	RestoreTableRowFromSnapshot(table string, snapshot map[string]any, conn *storage.Conn) (bool, error)

	CreateDeleteOperation(input storage.DeleteOperationInput) (map[string]any, error)
	GetDeleteOperation(operationID string) (map[string]any, error)
	ListDeleteOperations(limit int, mode string) ([]map[string]any, error)
	UpdateDeleteOperationState(operationID string, status string, summaryPatch map[string]any, conn *storage.Conn) (bool, error)
	CancelDeleteCleanupJobs(operationID string, conn *storage.Conn) (int, error)
	EnqueueStorageCleanupJobs(operationID string, jobs []storage.CleanupJobSpec, conn *storage.Conn) (int, error)
	SupersedeDeleteCleanupResources(resourceType string, resourceIDs []string, conn *storage.Conn) (storage.CleanupSupersedeResult, error)

	SweepDeletedItems(typeName string, gracePeriodSeconds float64) ([]storage.SweepDeletedItem, error)
	PhysicallyDeleteParagraphs(hashes []string) (int, error)
	PhysicallyDeleteEntities(hashes []string) (int, error)
	PurgeDeletedRelations(cutoffTime float64, limit int) ([]string, error)

	RebuildRelationHashAliases(conn *storage.Conn) (any, error)
	RecordV5Operation(action string, target string, resolvedHashes []string, reason string, updatedBy string, result map[string]any, conn *storage.Conn) (map[string]any, error)
	ReenqueueAuthoritativeRelationGraphProjectionJobs(jobs []map[string]any, enqueuedAt *float64) (int, error)
	EnqueueEpisodeSourceRebuilds(sources []string, reason string, opts storage.EpisodeEnqueueOptions) int
	EnqueuePersonProfileRefresh(input storage.PersonProfileRefreshEnqueueInput) (map[string]any, error)

	SetRelationVectorState(hashValue string, state string, errText *string, bumpRetry bool) (bool, error)
	FtsUpsertTokenizedParagraph(paragraphHash string, conn *storage.Conn) bool
	FtsDeleteTokenizedParagraph(paragraphHash string, conn *storage.Conn) bool
	UpsertParagraphNgramIfReady(paragraphHash string, content string, countDelta int, conn *storage.Conn) bool
	DeleteParagraphNgramsIfReady(paragraphHashes []string, countDelta int, conn *storage.Conn) bool
	GetSourcesForParagraphHashes(hashes []string, includeDeleted bool, conn *storage.Conn) []string
	GetParagraphStaleRelationMarksBatch(paragraphHashes []string, conn *storage.Conn) (map[string][]map[string]any, error)

	SnapshotFactEvidenceForParagraphs(paragraphHashes []string, conn *storage.Conn) (map[string]any, error)
	DetachFactEvidenceForParagraphsOnConn(paragraphHashes []string, reason string, detachedAt *float64, conn *storage.Conn) (map[string]any, error)
	RestoreFactEvidenceSnapshot(snapshot map[string]any, reason string, restoredAt *float64, conn *storage.Conn) (map[string]any, error)

	CreateFuzzyModifyPlan(input storage.FuzzyModifyPlanInput) (map[string]any, error)
	GetFuzzyModifyPlan(planID string) (map[string]any, error)
	UpdateFuzzyModifyPlan(planID string, update storage.FuzzyModifyPlanUpdate) (map[string]any, error)
	ClaimFuzzyModifyPlan(planID string, staleAfterSeconds float64) (map[string]any, error)
	ListFuzzyModifyPlans(limit int, statuses []string, scope string) ([]map[string]any, error)
}

// GraphStore 是图管理服务依赖的图存储端口，签名与 storage.GraphStore 对齐
type GraphStore interface {
	GetNodes() []string
	GetNodeAttributes(node string) map[string]any
	GetEdgeWeight(source string, target string) float64
	NumNodes() int
	NumEdges() int
	IterEdgeHashEntries() []storage.EdgeHashEntry
	AddNodes(nodes []string, attributes map[string]map[string]any) int
	AddEdges(edges []storage.Edge, weights []float64, relationHashes []string) (int, error)
	Clear() int
	Save(dataDir string) error
}

// tokens 递归收集字符串 token，对齐 Python _string_tokens / merge_tokens
func tokens(values ...any) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case nil:
			return
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				return
			}
			if _, ok := seen[trimmed]; ok {
				return
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		case []string:
			for _, item := range typed {
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key])
			}
		default:
			walk(fmt.Sprint(typed))
		}
	}
	for _, value := range values {
		walk(value)
	}
	return out
}

// mergeTokens 对齐 Python merge_tokens：按顺序合并并去重去空
func mergeTokens(values ...any) []string {
	return tokens(values...)
}

// asMap 把任意值收敛为对象，类型不符返回 nil
func asMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

// asList 把任意值收敛为列表
func asList(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, item)
		}
		return out
	default:
		return nil
	}
}

// asString 对齐 Python str() 取值，nil 折叠为空串
func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(typed)
	}
}

// asFloat 读取浮点数，无法解析时返回 ok=false
func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// optionalFloat 对齐 Python optional_float：空值或非法值返回 nil
func optionalFloat(value any) *float64 {
	if value == nil {
		return nil
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
		return nil
	}
	parsed, ok := asFloat(value)
	if !ok || math.IsNaN(parsed) {
		return nil
	}
	return &parsed
}

// intOr 读取整数并应用下界，对齐 Python max(1, int(value or default))
func intOr(value any, def int, minimum int) int {
	if value == nil {
		return def
	}
	var parsed int64
	switch typed := value.(type) {
	case int:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case float64:
		parsed = int64(typed)
	case string:
		converted, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			parsed = int64(def)
		} else {
			parsed = converted
		}
	default:
		parsed = int64(def)
	}
	if parsed < int64(minimum) {
		return minimum
	}
	return int(parsed)
}

// canonicalJSON 对齐 Python _canonical_json_bytes：UTF-8、键排序、紧凑分隔符
func canonicalJSON(value any) ([]byte, error) {
	return json.Marshal(canonicalizeJSON(value))
}

// canonicalizeJSON 递归把对象转换为键有序的表示，供 json.Marshal 输出稳定结果
func canonicalizeJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(typed))
		for _, key := range keys {
			out[key] = canonicalizeJSON(typed[key])
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, canonicalizeJSON(item))
		}
		return out
	default:
		return value
	}
}

// sha256Hex 计算十六进制 SHA-256
func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// sha256Text 计算字符串的十六进制 SHA-256
func sha256Text(text string) string {
	return sha256Hex([]byte(text))
}

// jsonObject 把 JSON 文本解析为对象，失败返回空对象
func jsonObject(text string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return map[string]any{}
	}
	return out
}

// trimText 折叠空白并按上限截断，对齐 Python _trim_text
func trimText(value any, limit int) string {
	text := strings.Join(strings.Fields(asString(value)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "..."
}

// pyTruthy 对齐 Python bool() 判定
func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// sortedKeys 返回对象的有序键
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
