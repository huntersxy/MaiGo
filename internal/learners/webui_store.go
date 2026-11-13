package learners

import (
	"context"
	"time"
)

// WebUIStore Dashboard 学习/资源台查询与写入入口；GORM 实现见 webui_gorm.go，测试可注入内存实现
//
// 该接口独立于 Store：Store 只服务 Agent 学习链路，Dashboard 需要搜索、排序、
// 统计与批量写等额外查询，合入 Store 会强迫所有学习侧实现提供无关方法
type WebUIStore interface {
	// ListExpressionRows 表达方式列表，对齐 get_expression_list
	ListExpressionRows(ctx context.Context, q ExpressionWebUIQuery) ([]ExpressionWebUIRow, int, error)
	// ExpressionChats 表达方式页面可见聊天流，对齐 get_chat_list
	ExpressionChats(ctx context.Context, includeLegacy bool) ([]ChatInfo, error)
	// ExpressionChatTargets 可作为导入目标的聊天流，对齐 get_chat_targets
	ExpressionChatTargets(ctx context.Context, includeLegacy bool) ([]ChatInfo, error)
	// ExpressionStats 表达方式统计，对齐 get_expression_stats
	ExpressionStats(ctx context.Context, includeLegacy bool) (ExpressionStats, error)
	// ExpressionReviewStats 审核统计，对齐 get_review_stats
	ExpressionReviewStats(ctx context.Context) (ReviewStats, error)
	// ExpressionReviewRows 审核列表，对齐 get_review_list
	ExpressionReviewRows(ctx context.Context, q ReviewListQuery) ([]ExpressionWebUIRow, int, error)
	// GetExpressionRow 表达方式详情
	GetExpressionRow(ctx context.Context, id uint) (*ExpressionWebUIRow, error)
	// CreateExpressionRow 新建表达方式
	CreateExpressionRow(ctx context.Context, in ExpressionCreateInput) (*ExpressionWebUIRow, error)
	// UpdateExpressionRow 增量更新表达方式
	UpdateExpressionRow(ctx context.Context, id uint, in ExpressionUpdateInput) (*ExpressionWebUIRow, error)
	// SetExpressionReviewStatus 人工审核状态切换
	SetExpressionReviewStatus(ctx context.Context, id uint, approved bool) (*ExpressionWebUIRow, error)
	// DeleteExpressionRows 删除表达方式，返回实际删除行数
	DeleteExpressionRows(ctx context.Context, ids []uint) (int, error)
	// ClearExpressionsBySession 清除聊天流下全部表达方式
	ClearExpressionsBySession(ctx context.Context, sessionID string) (int, error)
	// ImportExpressions 导入表达方式，sessionID 为 nil 表示全局
	ImportExpressions(ctx context.Context, sessionID *string, items []ExpressionImportItem) (ImportResult, error)
	// ExportExpressions 导出表达方式，不包含 session_id
	ExportExpressions(ctx context.Context, q ExpressionExportQuery) ([]ExpressionExportRow, error)
	// MissingExpressionIDs 导出请求中不存在或不在范围内的 ID
	MissingExpressionIDs(ctx context.Context, q ExpressionExportQuery) ([]uint, error)
	// BatchReviewExpressions 批量审核，逐条返回结果
	BatchReviewExpressions(ctx context.Context, items []ReviewBatchItem) []ReviewBatchResult

	// ListJargonRows 黑话列表，对齐 get_jargon_list
	ListJargonRows(ctx context.Context, q JargonWebUIQuery) ([]JargonWebUIRow, int, error)
	// GetJargonRow 黑话详情
	GetJargonRow(ctx context.Context, id uint) (*JargonWebUIRow, error)
	// JargonStats 黑话统计，对齐 get_jargon_stats
	JargonStats(ctx context.Context) (JargonStats, error)
	// JargonChats 黑话聊天流列表，对齐 get_chat_list
	JargonChats(ctx context.Context, includeEmpty bool) ([]JargonChat, error)
	// CreateJargonRow 新建手动黑话
	CreateJargonRow(ctx context.Context, in JargonCreateInput) (*JargonWebUIRow, error)
	// UpdateJargonRow 增量更新黑话
	UpdateJargonRow(ctx context.Context, id uint, in JargonUpdateInput) (*JargonWebUIRow, error)
	// DeleteJargonRows 删除黑话，返回实际删除行数
	DeleteJargonRows(ctx context.Context, ids []uint) (int, error)
	// SetJargonStatus 批量设置是否黑话，返回更新行数
	SetJargonStatus(ctx context.Context, ids []uint, isJargon bool) (int, error)
	// ImportJargons 导入黑话
	ImportJargons(ctx context.Context, in JargonImportInput) (ImportResult, error)
	// ExportJargons 导出黑话
	ExportJargons(ctx context.Context, q JargonExportQuery) ([]JargonExportRow, error)
	// MissingJargonIDs 导出请求中不存在的 ID
	MissingJargonIDs(ctx context.Context, q JargonExportQuery) ([]uint, error)

	// ChatName 聊天流显示名称，找不到时返回 session_id 截断值
	ChatName(ctx context.Context, sessionID string) string
	// ChatNames 批量显示名称
	ChatNames(ctx context.Context, sessionIDs []string) map[string]string
	// ExistingSession 校验聊天流是否为 db 中真实会话
	ExistingSession(ctx context.Context, sessionID string) bool
}

// ExpressionWebUIQuery 表达方式列表查询条件
type ExpressionWebUIQuery struct {
	Page          int
	PageSize      int
	Search        string
	ChatID        string
	ChatIDs       []string
	ReviewFilter  string // all / user_checked / unchecked
	SortBy        string // time / time_asc / created / created_asc
	IncludeLegacy bool
}

// ExpressionWebUIRow 表达方式 WebUI 行，chat_name 已在存储层解析
type ExpressionWebUIRow struct {
	ID             uint
	Situation      string
	Style          string
	ContentList    string
	Count          int
	LastActiveTime time.Time
	CreateTime     time.Time
	SessionID      string
	ChatName       string
	Checked        bool
	ModifiedBy     *ModifiedBy
}

// ChatInfo 聊天流展示信息，对齐 routers/expression.py ChatInfo
type ChatInfo struct {
	SessionID       string
	ChatName        string
	Platform        string
	AccountID       string
	TargetID        string
	IsGroup         bool
	IsLegacy        bool
	IsOtherAccount  bool
	UseExpression   bool
	EnableLearning  bool
	ExpressionCount *int
	JargonCount     int
}

// ExpressionStats 表达方式统计
type ExpressionStats struct {
	Total       int
	Recent7Days int
	ChatCount   int
	TopChats    map[string]int
}

// ReviewStats 表达方式审核统计
type ReviewStats struct {
	Total       int
	Unchecked   int
	Passed      int
	AIChecked   int
	UserChecked int
}

// ReviewListQuery 审核列表查询条件
type ReviewListQuery struct {
	Page       int
	PageSize   int
	FilterType string // unchecked / passed / all
	Order      string // latest / random
	Search     string
	ChatID     string
	ExcludeIDs []uint
}

// ExpressionCreateInput 新建表达方式
type ExpressionCreateInput struct {
	Situation   string
	Style       string
	SessionID   *string
	IsGlobal    bool
	ContentList string
	Count       int
	Checked     bool
	ModifiedBy  *ModifiedBy
}

// ExpressionUpdateInput 增量更新表达方式
type ExpressionUpdateInput struct {
	Situation *string
	Style     *string
	ChatID    *string
	HasChatID bool
}

// ExpressionImportItem 导入条目，字段与 Python ExpressionExportItem 一致
type ExpressionImportItem struct {
	Situation      string
	Style          string
	ContentList    string
	Count          int
	LastActiveTime *time.Time
	CreateTime     *time.Time
	Checked        bool
	ModifiedBy     *ModifiedBy
}

// ImportResult 导入结果统计
type ImportResult struct {
	Imported int
	Skipped  int
	Failed   int
}

// ExpressionExportQuery 导出条件；HasIDs/HasChatIDs 区分“未提供”和“空列表”
type ExpressionExportQuery struct {
	ChatID     string
	HasChatID  bool
	ChatIDs    []string
	HasChatIDs bool
	IDs        []uint
	HasIDs     bool
}

// ExpressionExportRow 导出条目
type ExpressionExportRow struct {
	Situation      string
	Style          string
	ContentList    string
	Count          int
	LastActiveTime *time.Time
	CreateTime     *time.Time
	Checked        bool
	ModifiedBy     *ModifiedBy
}

// ReviewBatchItem 批量审核项
type ReviewBatchItem struct {
	ID       uint
	Approved bool
}

// ReviewBatchResult 批量审核单项结果
type ReviewBatchResult struct {
	ID      uint
	Success bool
	Message string
}

// JargonWebUIQuery 黑话列表查询条件
type JargonWebUIQuery struct {
	Page         int
	PageSize     int
	Search       string
	SessionID    string
	SessionIDs   []string
	JargonStatus string // confirmed_jargon / confirmed_not_jargon / manual_jargon / pending / ""
	IsJargon     *bool
	IsComplete   *bool
	IsGlobal     *bool
	SortBy       string // time / time_asc / created / created_asc
}

// JargonWebUIRow 黑话 WebUI 行
type JargonWebUIRow struct {
	ID                   uint
	Content              string
	Meaning              string
	SessionIDs           []string
	ChatNames            []string
	ChatName             string
	Count                int
	IsJargon             bool
	IsLegacyEmptyMeaning bool
	IsComplete           bool
	IsGlobal             bool
	CreatedBy            JargonCreatedBy
	CreatedTimestamp     time.Time
	UpdatedTimestamp     time.Time
}

// JargonStats 黑话统计
type JargonStats struct {
	Total              int
	ConfirmedJargon    int
	ConfirmedNotJargon int
	Pending            int
	ManualJargon       int
	GlobalCount        int
	CompleteCount      int
	ChatCount          int
	TopChats           map[string]int
}

// JargonChat 黑话聊天流
type JargonChat struct {
	SessionID   string
	ChatName    string
	Platform    string
	AccountID   string
	TargetID    string
	IsGroup     bool
	JargonCount int
}

// JargonCreateInput 新建黑话
type JargonCreateInput struct {
	Content    string
	Meaning    string
	SessionIDs []string
	IsGlobal   bool
}

// JargonUpdateInput 增量更新黑话
type JargonUpdateInput struct {
	Content       *string
	Meaning       *string
	HasMeaning    bool
	SessionIDs    []string
	HasSessionIDs bool
	SessionID     *string
	HasSessionID  bool
	IsGlobal      *bool
	IsJargon      *bool
	HasIsJargon   bool
	CreatedBy     *JargonCreatedBy
}

// JargonImportItem 黑话导入条目
type JargonImportItem struct {
	Content    string
	Meaning    string
	Count      int
	IsJargon   bool
	IsComplete bool
	IsGlobal   bool
}

// JargonImportInput 黑话导入请求
type JargonImportInput struct {
	TargetSessionIDs []string
	IsGlobal         bool
	Items            []JargonImportItem
	ConflictStrategy string // skip / overwrite
}

// JargonExportQuery 黑话导出条件
type JargonExportQuery struct {
	IDs             []uint
	HasIDs          bool
	SessionIDs      []string
	HasSessionIDs   bool
	IncludeChatInfo bool
}

// JargonExportRow 黑话导出条目
type JargonExportRow struct {
	Content    string
	Meaning    string
	Count      int
	IsJargon   bool
	IsComplete bool
	IsGlobal   bool
	CreatedBy  JargonCreatedBy
	Targets    []JargonTarget
}

// JargonTarget 导出聊天目标，对齐 JargonTargetInfo
type JargonTarget struct {
	Platform  string
	ID        string
	Type      string // group / private
	AccountID string
	Scope     string
	Count     int
}

// NoopWebUIStore 无持久化时的安全默认实现，返回空结果
type NoopWebUIStore struct{}

func (NoopWebUIStore) ListExpressionRows(context.Context, ExpressionWebUIQuery) ([]ExpressionWebUIRow, int, error) {
	return nil, 0, nil
}
func (NoopWebUIStore) ExpressionChats(context.Context, bool) ([]ChatInfo, error) { return nil, nil }
func (NoopWebUIStore) ExpressionChatTargets(context.Context, bool) ([]ChatInfo, error) {
	return nil, nil
}
func (NoopWebUIStore) ExpressionStats(context.Context, bool) (ExpressionStats, error) {
	return ExpressionStats{TopChats: map[string]int{}}, nil
}
func (NoopWebUIStore) ExpressionReviewStats(context.Context) (ReviewStats, error) {
	return ReviewStats{}, nil
}
func (NoopWebUIStore) ExpressionReviewRows(context.Context, ReviewListQuery) ([]ExpressionWebUIRow, int, error) {
	return nil, 0, nil
}
func (NoopWebUIStore) GetExpressionRow(context.Context, uint) (*ExpressionWebUIRow, error) {
	return nil, ErrNotFound
}
func (NoopWebUIStore) CreateExpressionRow(_ context.Context, in ExpressionCreateInput) (*ExpressionWebUIRow, error) {
	return &ExpressionWebUIRow{Situation: in.Situation, Style: in.Style}, nil
}
func (NoopWebUIStore) UpdateExpressionRow(_ context.Context, id uint, _ ExpressionUpdateInput) (*ExpressionWebUIRow, error) {
	return &ExpressionWebUIRow{ID: id}, nil
}
func (NoopWebUIStore) SetExpressionReviewStatus(_ context.Context, id uint, _ bool) (*ExpressionWebUIRow, error) {
	return &ExpressionWebUIRow{ID: id}, nil
}
func (NoopWebUIStore) DeleteExpressionRows(context.Context, []uint) (int, error) { return 0, nil }
func (NoopWebUIStore) ClearExpressionsBySession(context.Context, string) (int, error) {
	return 0, nil
}
func (NoopWebUIStore) ImportExpressions(context.Context, *string, []ExpressionImportItem) (ImportResult, error) {
	return ImportResult{}, nil
}
func (NoopWebUIStore) ExportExpressions(context.Context, ExpressionExportQuery) ([]ExpressionExportRow, error) {
	return nil, nil
}
func (NoopWebUIStore) MissingExpressionIDs(context.Context, ExpressionExportQuery) ([]uint, error) {
	return nil, nil
}
func (NoopWebUIStore) BatchReviewExpressions(_ context.Context, items []ReviewBatchItem) []ReviewBatchResult {
	return nil
}

func (NoopWebUIStore) ListJargonRows(context.Context, JargonWebUIQuery) ([]JargonWebUIRow, int, error) {
	return nil, 0, nil
}
func (NoopWebUIStore) GetJargonRow(context.Context, uint) (*JargonWebUIRow, error) {
	return nil, ErrNotFound
}
func (NoopWebUIStore) JargonStats(context.Context) (JargonStats, error) {
	return JargonStats{TopChats: map[string]int{}}, nil
}
func (NoopWebUIStore) JargonChats(context.Context, bool) ([]JargonChat, error) { return nil, nil }
func (NoopWebUIStore) CreateJargonRow(_ context.Context, in JargonCreateInput) (*JargonWebUIRow, error) {
	return &JargonWebUIRow{Content: in.Content, Meaning: in.Meaning}, nil
}
func (NoopWebUIStore) UpdateJargonRow(_ context.Context, id uint, _ JargonUpdateInput) (*JargonWebUIRow, error) {
	return &JargonWebUIRow{ID: id}, nil
}
func (NoopWebUIStore) DeleteJargonRows(context.Context, []uint) (int, error) { return 0, nil }
func (NoopWebUIStore) SetJargonStatus(context.Context, []uint, bool) (int, error) {
	return 0, nil
}
func (NoopWebUIStore) ImportJargons(context.Context, JargonImportInput) (ImportResult, error) {
	return ImportResult{}, nil
}
func (NoopWebUIStore) ExportJargons(context.Context, JargonExportQuery) ([]JargonExportRow, error) {
	return nil, nil
}
func (NoopWebUIStore) MissingJargonIDs(context.Context, JargonExportQuery) ([]uint, error) {
	return nil, nil
}

func (NoopWebUIStore) ChatName(_ context.Context, sessionID string) string { return sessionID }
func (NoopWebUIStore) ChatNames(_ context.Context, sessionIDs []string) map[string]string {
	out := make(map[string]string, len(sessionIDs))
	for _, id := range sessionIDs {
		out[id] = id
	}
	return out
}
func (NoopWebUIStore) ExistingSession(context.Context, string) bool { return false }

// MissingIDs 返回 want 中不在 found 里的元素（升序）
func MissingIDs(want []uint, found map[uint]struct{}) []uint {
	missing := make([]uint, 0, len(want))
	for _, id := range want {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}
