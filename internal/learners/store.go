package learners

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("learners: 记录不存在")

// ExpressionFilter 列表查询条件，语义对齐 webui/routers/expression.py
type ExpressionFilter struct {
	SessionIDs      []string
	IncludeGlobal   bool
	CheckedOnly     bool
	UserCheckedOnly bool
	Page            int
	PageSize        int
}

// JargonFilter 列表查询条件，语义对齐 webui/routers/jargon.py
type JargonFilter struct {
	SessionIDs []string
	IsJargon   *bool
	Page       int
	PageSize   int
}

// Store 持久化接口；默认 NoopStore，启动后可注入 GORMStore
type Store interface {
	UpsertExpression(ctx context.Context, rec ExpressionRecord) (*ExpressionRecord, error)
	GetExpression(ctx context.Context, id uint) (*ExpressionRecord, error)
	ListExpressions(ctx context.Context, filter ExpressionFilter) ([]ExpressionRecord, int, error)
	SetExpressionChecked(ctx context.Context, id uint, checked bool, modifiedBy *ModifiedBy) error
	TouchExpressionLastActive(ctx context.Context, ids []uint) error
	DeleteExpression(ctx context.Context, id uint) error

	CreateJargon(ctx context.Context, rec JargonRecord) (*JargonRecord, error)
	// UpdateJargon 只更新 count/evidence/session_id_dict/is_global/updated_timestamp
	UpdateJargon(ctx context.Context, rec JargonRecord) (*JargonRecord, error)
	// UpdateJargonInference 只更新推断结果字段，避免覆盖并发更新
	UpdateJargonInference(ctx context.Context, rec JargonRecord) error
	GetJargon(ctx context.Context, id uint) (*JargonRecord, error)
	ListJargons(ctx context.Context, filter JargonFilter) ([]JargonRecord, int, error)
	SetJargonIsJargon(ctx context.Context, id uint, isJargon bool) error
	DeleteJargon(ctx context.Context, id uint) error
	// UpsertJargonCandidate 机械学习写入：同词条在会话范围内累加 count，新建时 is_jargon=false
	UpsertJargonCandidate(ctx context.Context, sessionID, content string) (*JargonRecord, bool, error)
	// UpsertJargonEntry 学习批次写入：带证据消息组，语义对齐 JargonMiner.process_extracted_entries
	UpsertJargonEntry(ctx context.Context, sessionID, content string, evidence []JargonEvidenceGroup) (*JargonRecord, bool, error)

	ListJargonCandidates(ctx context.Context, relatedSessionIDs []string, includeGlobalShare bool) ([]JargonCandidate, error)
	ListHighFrequencyTerms(ctx context.Context, chatID string) (map[string]HighFrequencyTermSummary, error)
	// MergeHighFrequencyTerms 增量合并高频词并重排；返回合并条数
	MergeHighFrequencyTerms(ctx context.Context, chatID string, terms []HighFrequencyTermInput, generatedAt time.Time, maxTerms int) (int, error)
}

// ExpressionRecord 领域视图，content 对应 content_list JSON 数组
type ExpressionRecord struct {
	ID             uint
	Situation      string
	Style          string
	Content        []string
	Count          int
	LastActiveTime time.Time
	CreateTime     time.Time
	SessionID      string
	Checked        bool
	ModifiedBy     *ModifiedBy
}

// JargonRecord 领域视图
type JargonRecord struct {
	ID                 uint
	Content            string
	Meaning            string
	EvidenceMessages   string
	SessionCounts      map[string]int
	Count              int
	IsJargon           bool
	IsComplete         bool
	IsGlobal           bool
	LastInferenceCount int
	CreatedBy          JargonCreatedBy
	CreatedTimestamp   time.Time
	UpdatedTimestamp   time.Time
}

// JargonEvidenceRef 黑话证据消息引用，对齐 evidence_messages 内的 {platform,message_id}
type JargonEvidenceRef struct {
	Platform  string
	MessageID string
}

// JargonEvidenceGroup 一段连续上下文的消息引用组
type JargonEvidenceGroup []JargonEvidenceRef

// JargonCandidate 机械匹配候选
type JargonCandidate struct {
	Content       string
	Meaning       string
	Count         int
	IsGlobal      bool
	SessionIDDict string
}

// HighFrequencyTermSummary 黑话打分用高频词摘要
type HighFrequencyTermSummary struct {
	Term            string
	Rank            int
	OccurrenceCount int
}

// HighFrequencyTermInput 一次词频统计结果，对应 WordFrequencyItem
type HighFrequencyTermInput struct {
	Term         string
	Count        int
	MessageCount int
}

// NoopStore 无持久化时的安全默认实现
type NoopStore struct{}

func (NoopStore) UpsertExpression(context.Context, ExpressionRecord) (*ExpressionRecord, error) {
	return nil, nil
}
func (NoopStore) GetExpression(context.Context, uint) (*ExpressionRecord, error) {
	return nil, ErrNotFound
}
func (NoopStore) ListExpressions(context.Context, ExpressionFilter) ([]ExpressionRecord, int, error) {
	return nil, 0, nil
}
func (NoopStore) SetExpressionChecked(context.Context, uint, bool, *ModifiedBy) error { return nil }
func (NoopStore) TouchExpressionLastActive(context.Context, []uint) error             { return nil }
func (NoopStore) DeleteExpression(context.Context, uint) error                        { return nil }

func (NoopStore) CreateJargon(context.Context, JargonRecord) (*JargonRecord, error) { return nil, nil }
func (NoopStore) UpdateJargon(context.Context, JargonRecord) (*JargonRecord, error) { return nil, nil }
func (NoopStore) UpdateJargonInference(context.Context, JargonRecord) error         { return nil }
func (NoopStore) GetJargon(context.Context, uint) (*JargonRecord, error)            { return nil, ErrNotFound }
func (NoopStore) ListJargons(context.Context, JargonFilter) ([]JargonRecord, int, error) {
	return nil, 0, nil
}
func (NoopStore) SetJargonIsJargon(context.Context, uint, bool) error { return nil }
func (NoopStore) DeleteJargon(context.Context, uint) error            { return nil }

func (NoopStore) UpsertJargonCandidate(context.Context, string, string) (*JargonRecord, bool, error) {
	return nil, false, nil
}

func (NoopStore) UpsertJargonEntry(context.Context, string, string, []JargonEvidenceGroup) (*JargonRecord, bool, error) {
	return nil, false, nil
}

func (NoopStore) ListJargonCandidates(context.Context, []string, bool) ([]JargonCandidate, error) {
	return nil, nil
}
func (NoopStore) ListHighFrequencyTerms(context.Context, string) (map[string]HighFrequencyTermSummary, error) {
	return map[string]HighFrequencyTermSummary{}, nil
}
func (NoopStore) MergeHighFrequencyTerms(context.Context, string, []HighFrequencyTermInput, time.Time, int) (int, error) {
	return 0, nil
}
