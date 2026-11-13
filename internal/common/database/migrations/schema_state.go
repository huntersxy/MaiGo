package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

const (
	baseSchemaVersion   = 17
	latestSchemaVersion = 54
)

// SchemaVersionSource 描述版本号的来源
type SchemaVersionSource string

const (
	SchemaSourceEmpty      SchemaVersionSource = "empty_database"
	SchemaSourceMigrations SchemaVersionSource = "schema_migrations"
	SchemaSourceStructure  SchemaVersionSource = "structure_detector"
	SchemaSourceLegacy     SchemaVersionSource = "legacy_database"
)

// SchemaState 是数据库结构状态快照
type SchemaState struct {
	Version           int
	Latest            int
	Source            SchemaVersionSource
	Empty             bool
	AdoptableV17      bool
	Legacy            bool
	MigrationsTable   bool
	BaseTablesPresent bool
	LegacyTables      []string
	Applied           []int
	Pending           []int
	Message           string
}

// GuardAction 是宿主应当执行的动作
type GuardAction string

const (
	GuardInitialize GuardAction = "initialize"
	GuardAdoptV17   GuardAction = "adopt_v17"
	GuardUpgrade    GuardAction = "upgrade"
	GuardNoop       GuardAction = "noop"
	GuardManual     GuardAction = "manual_upgrade"
)

// GuardDecision 把结构状态翻译成可执行决策
type GuardDecision struct {
	State            SchemaState
	Action           GuardAction
	Allowed          bool
	RequiresManual   bool
	Reason           string
	PendingVersions  []int
	CurrentVersion   int
	SupportedVersion int
}

// ErrSchemaNewer 表示数据库版本高于程序支持版本
var ErrSchemaNewer = errors.New("数据库版本高于程序支持版本")

// ErrLegacySchema 表示数据库无法自动识别或自动升级
var ErrLegacySchema = errors.New("数据库结构无法自动升级")

// LegacySchemaError 携带旧库结构细节，避免只返回不可诊断的字符串
type LegacySchemaError struct {
	State SchemaState
}

func (e *LegacySchemaError) Error() string {
	if e == nil {
		return ErrLegacySchema.Error()
	}
	if e.State.Message != "" {
		return e.State.Message
	}
	return fmt.Sprintf("%s: version=%d source=%s", ErrLegacySchema, e.State.Version, e.State.Source)
}

func (e *LegacySchemaError) Unwrap() error { return ErrLegacySchema }

// NewerSchemaError 表示数据库版本高于当前程序
type NewerSchemaError struct {
	Version int
	Latest  int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("数据库 schema 版本为 v%d，高于当前程序支持的 v%d，请升级 maibot-go", e.Version, e.Latest)
}

func (e *NewerSchemaError) Unwrap() error { return ErrSchemaNewer }

// ResolveSchema 只读探测数据库版本，不修改任何结构
func ResolveSchema(db *gorm.DB) (SchemaState, error) {
	if db == nil {
		return SchemaState{}, fmt.Errorf("数据库未初始化")
	}
	state, err := resolveSchema(context.Background(), gormQuerier{db: db})
	if err != nil {
		return SchemaState{}, err
	}
	return state, nil
}

// GuardSchema 返回宿主可直接消费的迁移决策
func GuardSchema(db *gorm.DB) (GuardDecision, error) {
	state, err := ResolveSchema(db)
	if err != nil {
		return GuardDecision{}, err
	}
	return DecideSchema(state), nil
}

// DecideSchema 是纯函数，便于单测覆盖门禁分支
func DecideSchema(state SchemaState) GuardDecision {
	decision := GuardDecision{
		State:            state,
		CurrentVersion:   state.Version,
		SupportedVersion: latestSchemaVersion,
		PendingVersions:  append([]int(nil), state.Pending...),
	}
	switch {
	case state.Empty:
		decision.Action = GuardInitialize
		decision.Allowed = true
		decision.Reason = "空数据库，可初始化"
	case state.Legacy:
		decision.Action = GuardManual
		decision.RequiresManual = true
		decision.Reason = state.Message
		if decision.Reason == "" {
			decision.Reason = "检测到无法自动识别的旧库，需要导出后重建或手工升级"
		}
	case state.Version > latestSchemaVersion:
		decision.Action = GuardManual
		decision.RequiresManual = true
		decision.Reason = fmt.Sprintf("数据库版本 v%d 高于程序支持版本 v%d", state.Version, latestSchemaVersion)
	case state.Source == SchemaSourceStructure || state.AdoptableV17:
		decision.Action = GuardAdoptV17
		decision.Allowed = true
		decision.Reason = "检测到完整 v17 结构，补写版本记录后升级"
	case state.Version == latestSchemaVersion:
		decision.Action = GuardNoop
		decision.Allowed = true
		decision.Reason = "数据库已是最新版本"
	case state.Version >= baseSchemaVersion:
		decision.Action = GuardUpgrade
		decision.Allowed = true
		decision.Reason = fmt.Sprintf("数据库版本 v%d 可升级到 v%d", state.Version, latestSchemaVersion)
	default:
		decision.Action = GuardManual
		decision.RequiresManual = true
		decision.Reason = fmt.Sprintf("版本记录为 v%d，缺少 v%d 之前的可重建结构", state.Version, baseSchemaVersion)
	}
	return decision
}

// legacyMarkers 是旧 MaiBot SQLite 库的识别表，仅用于诊断提示
var legacyMarkers = []string{
	"mai_messages", "messages", "action_records", "chat_streams",
	"behavior_patterns", "behavior_experience_paths", "thinking_back",
	"image_descriptions", "emoji_description_cache",
}

func resolveSchema(ctx context.Context, q querier) (SchemaState, error) {
	state := SchemaState{Latest: latestSchemaVersion}
	state.Pending = make([]int, 0, latestSchemaVersion-baseSchemaVersion)

	tables, err := listTables(ctx, q)
	if err != nil {
		return state, fmt.Errorf("读取数据库表失败: %w", err)
	}
	state.Empty = len(tables) == 0
	state.BaseTablesPresent = hasAllTables(tables, []string{
		"mai_messages", "chat_sessions", "knowledge_items", "expressions", "jargons",
	})
	state.MigrationsTable = tables["schema_migrations"]
	state.LegacyTables = legacyTables(tables)

	if state.Empty {
		state.Source = SchemaSourceEmpty
		state.Pending = versionsAfter(baseSchemaVersion)
		return state, nil
	}

	if !state.MigrationsTable {
		if state.BaseTablesPresent {
			state.Source = SchemaSourceStructure
			state.Version = baseSchemaVersion
			state.AdoptableV17 = true
			state.Pending = versionsAfter(baseSchemaVersion)
			state.Message = "存在完整 v17 基础表但缺少 schema_migrations，将补写版本记录后升级"
			return state, nil
		}
		state.Source = SchemaSourceLegacy
		state.Legacy = true
		state.Message = legacyMessage(state.LegacyTables)
		return state, nil
	}

	applied, err := appliedVersions(ctx, q)
	if err != nil {
		return state, fmt.Errorf("读取 schema_migrations 失败: %w", err)
	}
	state.Applied = applied
	if len(applied) > 0 {
		state.Version = applied[len(applied)-1]
	}
	switch {
	case state.Version == 0 && state.BaseTablesPresent:
		state.Source = SchemaSourceStructure
		state.Version = baseSchemaVersion
		state.AdoptableV17 = true
		state.Pending = versionsAfter(baseSchemaVersion)
	case state.Version == 0 && len(state.LegacyTables) > 0:
		state.Source = SchemaSourceLegacy
		state.Legacy = true
		state.Message = legacyMessage(state.LegacyTables)
	case state.Version == 0:
		state.Source = SchemaSourceLegacy
		state.Legacy = true
		state.Message = "非空数据库缺少有效版本记录，无法自动升级"
	case state.Version < baseSchemaVersion:
		state.Source = SchemaSourceMigrations
		state.Legacy = true
		state.Message = fmt.Sprintf("schema_migrations 记录为 v%d，低于可自动升级的 v%d", state.Version, baseSchemaVersion)
	case state.Version > latestSchemaVersion:
		state.Source = SchemaSourceMigrations
		state.Pending = nil
	default:
		state.Source = SchemaSourceMigrations
		state.Pending = versionsAfter(state.Version)
	}
	return state, nil
}

func legacyMessage(tables []string) string {
	if len(tables) == 0 {
		return "检测到非空数据库，但既没有 schema_migrations 也不是完整 v17 结构，无法自动升级"
	}
	return "检测到旧 MaiBot 表 " + strings.Join(tables, ",") + "，需要先导出数据再初始化 v17"
}

func legacyTables(tables map[string]bool) []string {
	result := make([]string, 0, len(legacyMarkers))
	for _, name := range legacyMarkers {
		if tables[name] {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func hasAllTables(tables map[string]bool, names []string) bool {
	for _, name := range names {
		if !tables[name] {
			return false
		}
	}
	return true
}

func versionsAfter(version int) []int {
	if version >= latestSchemaVersion {
		return nil
	}
	result := make([]int, 0, latestSchemaVersion-version)
	for v := version + 1; v <= latestSchemaVersion; v++ {
		result = append(result, v)
	}
	return result
}

func appliedVersions(ctx context.Context, q querier) ([]int, error) {
	rows, err := q.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	return result, rows.Err()
}

func listTables(ctx context.Context, q querier) (map[string]bool, error) {
	var query string
	switch q.Dialect() {
	case DialectSQLite:
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
	default:
		query = "SELECT tablename FROM pg_tables WHERE schemaname=current_schema()"
	}
	rows, err := q.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

// migrationMaintenanceJob 表示必须在迁移事务提交后执行的维护动作
type migrationMaintenanceJob func(ctx context.Context, q querier) error

type migrationMaintenanceScheduler interface {
	ScheduleMaintenance(job migrationMaintenanceJob) bool
}

// gormQuerier 把 GORM 适配为迁移引擎使用的最小接口
type gormQuerier struct {
	db          *gorm.DB
	maintenance *[]migrationMaintenanceJob
}

// ScheduleMaintenance 把 VACUUM 等不能进入事务的动作推迟到提交之后
func (q gormQuerier) ScheduleMaintenance(job migrationMaintenanceJob) bool {
	if q.maintenance == nil {
		return false
	}
	*q.maintenance = append(*q.maintenance, job)
	return true
}

func scheduleMigrationMaintenance(ctx context.Context, q querier, job migrationMaintenanceJob) error {
	if scheduler, ok := q.(migrationMaintenanceScheduler); ok && scheduler.ScheduleMaintenance(job) {
		return nil
	}
	return job(ctx, q)
}

func (q gormQuerier) Dialect() dialect {
	name := q.db.Dialector.Name()
	if name == "sqlite" {
		return DialectSQLite
	}
	return DialectPostgres
}

func (q gormQuerier) Exec(ctx context.Context, query string, args ...any) error {
	return q.db.WithContext(ctx).Exec(query, args...).Error
}

func (q gormQuerier) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return q.db.WithContext(ctx).Raw(query, args...).Rows()
}

func (q gormQuerier) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return q.db.WithContext(ctx).Raw(query, args...).Row()
}
