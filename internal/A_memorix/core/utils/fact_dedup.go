package utils

// A_memorix 写入前事实去重，对齐 Python core/utils/fact_deduplication.py
// 存储、模型、向量检索都通过注入接口访问，避免 utils 反向依赖 storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// FactDedupFact 一条待去重事实，对齐 Python facts 列表中使用到的字段
type FactDedupFact struct {
	FactID    string   `json:"fact_id"`
	Text      string   `json:"text"`
	PersonIDs []string `json:"person_ids,omitempty"`
}

// FactDedupTarget 去重目标，对齐 Python decision 中的 target
type FactDedupTarget struct {
	// ID 是候选原始标识，提示词 payload 会另用短引用
	ID             string           `json:"id,omitempty"`
	Text           string           `json:"text,omitempty"`
	ClaimID        string           `json:"claim_id,omitempty"`
	ParagraphHash  string           `json:"paragraph_hash,omitempty"`
	InputFactID    string           `json:"input_fact_id,omitempty"`
	ValidFrom      *float64         `json:"valid_from,omitempty"`
	ValidTo        *float64         `json:"valid_to,omitempty"`
	EventTimeStart *float64         `json:"event_time_start,omitempty"`
	EventTimeEnd   *float64         `json:"event_time_end,omitempty"`
	ObservedAt     *float64         `json:"observed_at,omitempty"`
	PersonIDs      []string         `json:"person_ids,omitempty"`
	Observations   []map[string]any `json:"observations,omitempty"`
}

// FactDedupDecision 单条去重决定
type FactDedupDecision struct {
	FactID       string           `json:"fact_id"`
	Action       string           `json:"action"`
	Target       *FactDedupTarget `json:"target,omitempty"`
	CandidateIDs []string         `json:"candidate_ids,omitempty"`
}

// FactDedupPlan 去重计划，对齐 Python plan 返回的字典
type FactDedupPlan struct {
	ScopeType       string              `json:"scope_type"`
	ScopeID         string              `json:"scope_id"`
	Facts           []FactDedupFact     `json:"facts"`
	Source          map[string]any      `json:"source"`
	Decisions       []FactDedupDecision `json:"decisions"`
	RelationSupport map[string][]string `json:"relation_support,omitempty"`
}

// FactDedupClassification 写入分类，nil 字段表示取 Python 侧默认值
type FactDedupClassification struct {
	Authority      *string
	Stability      *string
	ProfileSection *string
}

// FactClaimUpsert 对齐 storage.FactClaimUpsert 的字段子集，由调用方适配
type FactClaimUpsert struct {
	ScopeType          string
	ScopeID            string
	FactKey            string
	ValueText          string
	Polarity           string
	Cardinality        string
	Stability          string
	ProfileSection     string
	Authority          string
	Confidence         *float64
	ValidFrom          *float64
	ValidTo            *float64
	EvidenceType       string
	EvidenceID         string
	EvidenceStance     string
	EvidenceWeight     *float64
	EvidenceMetadata   map[string]any
	SupersedesClaimIDs []string
	Reason             string
	ObservedAt         *float64
}

// FactEvidenceUpsert 对齐 storage.FactEvidenceUpsert 的字段子集
type FactEvidenceUpsert struct {
	EvidenceType string
	EvidenceID   string
	Stance       string
	Weight       *float64
	Metadata     map[string]any
	Reason       string
	ObservedAt   *float64
}

// FactEvidenceResult 证据写入结果，对齐 Python add_fact_evidence 返回值的子集
type FactEvidenceResult struct {
	Created    bool
	ObservedAt *float64
}

// FactDedupTx 事务内执行语句
type FactDedupTx interface {
	Execute(sql string, args ...any) error
}

// FactDedupStore 去重所需存储能力，对齐 Python FactDeduplicator 使用到的 store 方法
type FactDedupStore interface {
	GetDBPath() (string, error)
	Query(sql string, params ...any) ([]map[string]any, error)
	GetParagraph(hashValue string) (map[string]any, error)
	GetFactClaim(claimID string) (map[string]any, error)
	GetFactEvidence(claimID string) ([]map[string]any, error)
	// UpsertFactClaim 对齐 Python upsert_fact_claim，返回 claim_id
	UpsertFactClaim(in FactClaimUpsert) (string, error)
	AddFactEvidence(claimID string, in FactEvidenceUpsert) (FactEvidenceResult, error)
	Transaction(immediate bool, fn func(tx FactDedupTx) error) error
}

// FactDedupLLMRequest 去重模型请求
type FactDedupLLMRequest struct {
	TaskName    string
	RequestType string
	Prompt      string
	Temperature float64
}

// FactDedupLLMResponse 去重模型响应，Completion 对应 Python completion.response
type FactDedupLLMResponse struct {
	Success    bool
	Error      string
	Completion string
}

// FactDedupLLM 去重模型调用
type FactDedupLLM interface {
	Generate(ctx context.Context, req FactDedupLLMRequest) (FactDedupLLMResponse, error)
}

// FactDedupPromptBuilder 由 payload JSON 构建最终提示词，对应 load_prompt("a_memorix_fact_deduplication")
type FactDedupPromptBuilder func(payloadJSON string) (string, error)

// FactDedupEmbedding 文本向量化
type FactDedupEmbedding interface {
	Encode(ctx context.Context, text string) ([]float32, error)
}

// FactDedupVectorStore 段落向量检索，第二个返回值 Python 侧被丢弃
type FactDedupVectorStore interface {
	Search(ctx context.Context, vector []float32, k int, allowedIDs []string) ([]string, error)
}

// FactDeduplicator 事实去重器，对齐 Python FactDeduplicator
type FactDeduplicator struct {
	store         FactDedupStore
	embedding     FactDedupEmbedding
	vectors       FactDedupVectorStore
	llm           FactDedupLLM
	promptBuilder FactDedupPromptBuilder
	taskName      string
}

// NewFactDeduplicator 创建去重器，taskName 为空时对齐 Python 默认值 utils
func NewFactDeduplicator(
	store FactDedupStore,
	embedding FactDedupEmbedding,
	vectors FactDedupVectorStore,
	llm FactDedupLLM,
	promptBuilder FactDedupPromptBuilder,
	taskName string,
) *FactDeduplicator {
	if strings.TrimSpace(taskName) == "" {
		taskName = "utils"
	}
	return &FactDeduplicator{
		store:         store,
		embedding:     embedding,
		vectors:       vectors,
		llm:           llm,
		promptBuilder: promptBuilder,
		taskName:      taskName,
	}
}

// NormalizedFact 对齐 normalized_fact：NFKC 后压缩空白再 casefold
func NormalizedFact(text string) string {
	normalized := norm.NFKC.String(text)
	fields := strings.FieldsFunc(normalized, pyIsSpace)
	return cases.Fold().String(strings.Join(fields, " "))
}

// VisibleFactMetadata 过滤仅内部使用的去重决定与疑似重复关系
func VisibleFactMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if strings.HasPrefix(key, "dedup_") || key == "fact_dedup_decisions" {
			continue
		}
		out[key] = value
	}
	return out
}

// ObservationID 对齐 observation_id：用真实来源集合标识一次观察
func ObservationID(platform string, chatID string, messageIDs []string, windowID string) (string, error) {
	if chatID == "" || (len(messageIDs) == 0 && windowID == "") {
		return "", fmt.Errorf("去重写入缺少真实会话或来源标识")
	}
	seen := make(map[string]struct{}, len(messageIDs))
	unique := make([]string, 0, len(messageIDs))
	for _, item := range messageIDs {
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		unique = append(unique, item)
	}
	sort.Strings(unique)
	payload, err := pyJSONMarshal([]any{platform, chatID, unique, windowID})
	if err != nil {
		return "", err
	}
	return ComputeHash(payload)
}

// AcquireFactScopeLock 对齐 fact_scope_lock：同一数据库与范围共享一把可取消的锁
func AcquireFactScopeLock(ctx context.Context, store FactDedupStore, scopeType string, scopeID string) (func(), error) {
	if store == nil {
		return nil, fmt.Errorf("事实去重缺少存储")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dbPath, err := store.GetDBPath()
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	key := filepath.Clean(absolute) + "\x00" + scopeType + "\x00" + scopeID
	entry := factScopeEntry(key)
	select {
	case <-ctx.Done():
		factScopeDropRef(key, entry)
		return nil, ctx.Err()
	case <-entry.token:
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			entry.token <- struct{}{}
			factScopeDropRef(key, entry)
		})
	}
	return release, nil
}

type factScopeLockEntry struct {
	token chan struct{}
	refs  int
}

var factScopeRegistry = struct {
	sync.Mutex
	entries map[string]*factScopeLockEntry
}{entries: make(map[string]*factScopeLockEntry)}

func factScopeEntry(key string) *factScopeLockEntry {
	factScopeRegistry.Lock()
	defer factScopeRegistry.Unlock()
	entry := factScopeRegistry.entries[key]
	if entry == nil {
		entry = &factScopeLockEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		factScopeRegistry.entries[key] = entry
	}
	entry.refs++
	return entry
}

func factScopeDropRef(key string, entry *factScopeLockEntry) {
	factScopeRegistry.Lock()
	defer factScopeRegistry.Unlock()
	entry.refs--
	if entry.refs <= 0 && factScopeRegistry.entries[key] == entry {
		delete(factScopeRegistry.entries, key)
	}
}

// factDedupString 对齐 str(value or "") 的常见取值
func factDedupString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// factDedupFloat 读取可选浮点列
func factDedupFloat(value any) *float64 {
	var number float64
	switch typed := value.(type) {
	case nil:
		return nil
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case int32:
		number = float64(typed)
	case uint64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	return &number
}

// factDedupTruthy 对齐 Python 真值判断中会出现的标量与容器
func factDedupTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case []byte:
		return len(typed) > 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case int32:
		return typed != 0
	case uint64:
		return typed != 0
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return false
	}
}

// factDedupMetadata 读取对象列，兼容已解码的 map 与 JSON 文本
func factDedupMetadata(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil
		}
		return decoded
	case []byte:
		return factDedupMetadata(string(typed))
	default:
		return nil
	}
}

// factDedupStringList 把证据或段落中的人员列表归一为字符串切片
func factDedupStringList(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, factDedupString(item))
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		var decoded []any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil
		}
		return factDedupStringList(decoded)
	default:
		return nil
	}
}

// factDedupAnyList 对齐 Python isinstance(value, list)
func factDedupAnyList(value any) ([]any, bool) {
	switch typed := value.(type) {
	case nil:
		return nil, false
	case []any:
		return typed, true
	default:
		return nil, false
	}
}

// factDedupSetEqual 比较两个人员集合是否相等（按去重后的集合语义）
func factDedupSetEqual(left []string, right []string) bool {
	leftSet := make(map[string]struct{}, len(left))
	for _, item := range left {
		leftSet[item] = struct{}{}
	}
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for item := range leftSet {
		if _, ok := rightSet[item]; !ok {
			return false
		}
	}
	return true
}
