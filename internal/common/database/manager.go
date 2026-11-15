package memory

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"

	"MaiGo/internal/common/logger"
)

type Manager struct {
	db       *gorm.DB
	mediaDir string
}

// Dialect 数据库方言
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// DefaultDatabaseFileName 默认库文件名，对齐 Python src/common/database/database.py:29
// （`_DB_FILE = _DB_DIR / "MaiBot.db"`，_DB_DIR = 实例根目录/data）
const DefaultDatabaseFileName = "MaiBot.db"

// InstanceRoot 返回实例根目录，对齐 Python src/common/paths.py get_instance_root
//
// 优先 MAIBOT_INSTANCE_DIR，否则取进程工作目录。
func InstanceRoot() string {
	if value := strings.TrimSpace(os.Getenv("MAIBOT_INSTANCE_DIR")); value != "" {
		return value
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// DefaultSQLiteDSN 返回默认 SQLite DSN，文件落在 <实例根>/data/MaiBot.db
func DefaultSQLiteDSN() string {
	return filepath.ToSlash(filepath.Join(InstanceRoot(), "data", DefaultDatabaseFileName))
}

// ResolveDialect 按 DSN 前缀判定方言；空串与裸路径都视为 SQLite
func ResolveDialect(dsn string) Dialect {
	value := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(value, "postgres://") || strings.HasPrefix(value, "postgresql://") {
		return DialectPostgres
	}
	return DialectSQLite
}

// OpenDB 打开 SQLite 数据库连接
//
// 对齐 Python src/common/database/database.py:33 —— `sqlite:///<实例根>/data/MaiBot.db` 是
// 唯一的数据库实现，没有 PostgreSQL 分支，因此这里也只用 SQLite。
// gorm 日志级别由 logger 包的库日志配置决定，对齐 Python logger.py:467-480
func OpenDB(dsn string) (*gorm.DB, error) {
	if ResolveDialect(dsn) == DialectPostgres {
		return nil, fmt.Errorf("不再支持 PostgreSQL：MaiBot 只用 SQLite（data/MaiBot.db），" +
			"请把 MAIBOT_DATABASE_DSN 设为空或 SQLite 文件路径")
	}
	path, err := sqliteDSNPath(dsn)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}
	// 用包装驱动把时间写成 Python 的朴素格式，保证库文件与 MaiBot 互通
	registerSQLiteDriver()
	db, err := gorm.Open(sqlite.Dialector{DriverName: maibotSQLiteDriverName, DSN: path}, &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			IgnoreRecordNotFoundError: true,
			LogLevel:                  logger.CurrentGormLogLevel(),
		}),
	})
	if err != nil {
		return nil, fmt.Errorf("连接 SQLite 数据库失败: %w", err)
	}
	if err := applySQLitePragmas(db); err != nil {
		return nil, err
	}
	return db, nil
}

// sqliteDSNPath 把各种写法的 SQLite DSN 归一成文件路径
//
// 支持 `sqlite:///abs/path.db`、`sqlite:/abs/path.db`、`/abs/path.db`、相对路径与空串（取默认）。
func sqliteDSNPath(dsn string) (string, error) {
	value := strings.TrimSpace(dsn)
	if value == "" {
		return DefaultSQLiteDSN(), nil
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "sqlite:///"):
		// 三斜杠表示绝对路径：sqlite:///app/x.db -> /app/x.db
		// （Python 的 sqlite:///<path> 语义，斜杠本身属于路径）
		rest := value[len("sqlite://"):]
		if strings.HasPrefix(rest, "/") {
			return rest, nil
		}
		return "/" + rest, nil
	case strings.HasPrefix(lower, "sqlite://"):
		return value[len("sqlite://"):], nil
	case strings.HasPrefix(lower, "sqlite:"):
		return value[len("sqlite:"):], nil
	case strings.HasPrefix(lower, "file:"):
		return value[len("file:"):], nil
	}
	return value, nil
}

// sqlitePragmas 对齐 Python database.py:36-47 的连接级 PRAGMA
//
// journal_mode=WAL 让读写并行（SQLite 仍是单写者，但不会读写互斥）；
// cache_size 负值表示 KB，-64000 即 64MB；
// busy_timeout=1000 是 Python 的 1 秒，这里放宽到 5 秒以适配 Go 侧更高的协程并发。
var sqlitePragmas = []string{
	"PRAGMA journal_mode=WAL",
	"PRAGMA cache_size=-64000",
	"PRAGMA foreign_keys=ON",
	"PRAGMA synchronous=NORMAL",
	"PRAGMA busy_timeout=5000",
}

// applySQLitePragmas 为新连接设置 PRAGMA，并限制写连接数避免 SQLITE_BUSY
//
// SQLite 是单写者模型：把连接池压到 1 个连接即可把并发写串行化，
// 避免多协程同时写时频繁撞 busy_timeout。
func applySQLitePragmas(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("读取底层连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	for _, pragma := range sqlitePragmas {
		if err := db.Exec(pragma).Error; err != nil {
			return fmt.Errorf("设置 SQLite PRAGMA 失败(%s): %w", pragma, err)
		}
	}
	return nil
}

func NewManager(db *gorm.DB, mediaDir string) (*Manager, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	if err := EnsurePlatformIdentitySchema(db); err != nil {
		return nil, err
	}
	return &Manager{db: db, mediaDir: mediaDir}, nil
}

func (m *Manager) GetRecentMessagesScope(ctx context.Context, kind string, targetID, throughOneBotMessageID int64, limit, offset int) ([]MessageLog, error) {
	var items []MessageLog
	q := m.db.WithContext(ctx).Where("conversation_kind=? AND target_id=? AND is_command=?", kind, targetID, false).
		Where(NoticeExternalMessageIDPredicate("external_message_id")).Order("id DESC").Limit(limit)
	if throughOneBotMessageID != 0 {
		upperBound := m.db.Model(&MessageLog{}).Select("id").Where("conversation_kind=? AND target_id=? AND message_id=?", kind, targetID, throughOneBotMessageID).
			Where(NoticeExternalMessageIDPredicate("external_message_id"))
		q = q.Where("mai_messages.id<=(?)", upperBound)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	if err := q.Find(&items).Error; err != nil {
		return nil, err
	}
	slices.Reverse(items)
	return items, nil
}

func (m *Manager) GetMessageCountByTime(groupID, userID int64, start time.Time) (int64, error) {
	var count int64
	err := m.db.Model(&MessageLog{}).Where("conversation_kind=? AND target_id=? AND user_id=? AND timestamp>=?", ConversationKindGroup, groupID, userID, start).
		Where(NoticeExternalMessageIDPredicate("external_message_id")).Count(&count).Error
	return count, err
}

func requireAffected(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (m *Manager) GetMemberProfile(userID int64) (*MemberProfile, error) {
	var profile MemberProfile
	if err := m.db.First(&profile, "user_id=?", userID).Error; err != nil {
		return nil, err
	}
	return &profile, nil
}

func (m *Manager) GetOrCreateMemberProfile(userID int64, nickname string, seenAt time.Time) (*MemberProfile, error) {
	profile := MemberProfile{UserID: userID, Nickname: nickname, UpdatedAt: seenAt}
	err := m.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"nickname":   gorm.Expr("CASE WHEN EXCLUDED.updated_at>=member_profiles.updated_at THEN EXCLUDED.nickname ELSE member_profiles.nickname END"),
			"updated_at": gorm.Expr("max(member_profiles.updated_at, excluded.updated_at)"),
		}),
	}, clause.Returning{}).Create(&profile).Error
	if err != nil {
		return nil, err
	}
	return &profile, nil
}

func (m *Manager) GetMessageLogByScope(kind string, targetID, messageID int64) (*MessageLog, error) {
	var item MessageLog
	if kind == "" {
		kind = ConversationKindGroup
	}
	if err := m.db.Where("conversation_kind=? AND target_id=? AND message_id=?", kind, targetID, messageID).
		Where(NoticeExternalMessageIDPredicate("external_message_id")).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// GetMessageLogByScopeExternalID 按会话作用域与平台外部消息编号单查
//
// 非 QQ 平台的 message_id 在内部是合成数字键，适配器给的是原始字符串 ID；
// 引用还原必须能按外部 ID 命中，否则会把已入库的消息误判为无法访问。
func (m *Manager) GetMessageLogByScopeExternalID(kind string, targetID int64, externalMessageID string) (*MessageLog, error) {
	externalMessageID = strings.TrimSpace(externalMessageID)
	if externalMessageID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	if kind == "" {
		kind = ConversationKindGroup
	}
	var item MessageLog
	query := m.db.Where("external_message_id=?", externalMessageID).
		Where(NoticeExternalMessageIDPredicate("external_message_id"))
	if targetID > 0 {
		query = query.Where("conversation_kind=? AND target_id=?", kind, targetID)
	}
	if err := query.First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// GetMessageLogBySessionAndMessageID 按 session_id 与平台消息编号单查，供 WebUI 回填 reply 段
// sessionID 为空时退化为全局按消息编号查询
func (m *Manager) GetMessageLogBySessionAndMessageID(ctx context.Context, sessionID string, messageID int64) (*MessageLog, error) {
	if messageID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var item MessageLog
	query := m.db.WithContext(ctx).Where("message_id=?", messageID).
		Where(NoticeExternalMessageIDPredicate("external_message_id"))
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		query = query.Where("session_id=?", sessionID)
	}
	if err := query.First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (m *Manager) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := m.db.WithContext(ctx).Model(&SchemaMigration{}).Select("COALESCE(max(version),0)").Scan(&version).Error
	return version, err
}

// MarkMessageRecalledScope 只标记撤回，不改写已经进入上下文的历史原文
func (m *Manager) MarkMessageRecalledScope(kind string, targetID, messageID int64) (*MessageLog, bool, error) {
	if kind == "" {
		kind = ConversationKindGroup
	}
	if targetID <= 0 || messageID == 0 {
		return nil, false, nil
	}
	var item MessageLog
	changed := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("conversation_kind=? AND target_id=? AND message_id=?", kind, targetID, messageID).Take(&item)
		if result.Error == gorm.ErrRecordNotFound {
			return nil
		}
		if result.Error != nil {
			return result.Error
		}
		if item.ProcessedPlainText == RecalledMessageProcessedPlainText {
			return nil
		}
		if err := tx.Model(&item).Update("processed_plain_text", RecalledMessageProcessedPlainText).Error; err != nil {
			return err
		}
		item.ProcessedPlainText = RecalledMessageProcessedPlainText
		changed = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if item.ID == 0 {
		return nil, false, nil
	}
	return &item, changed, nil
}

func (m *Manager) Close() error {
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (m *Manager) GetDB() *gorm.DB { return m.db }

func (m *Manager) GetMessageLogByInternalID(id uint) (*MessageLog, error) {
	var item MessageLog
	if err := m.db.Where("id=?", id).Where(NoticeExternalMessageIDPredicate("external_message_id")).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// WithContext 返回绑定请求上下文的轻量管理器
func (m *Manager) WithContext(ctx context.Context) *Manager {
	return &Manager{db: m.db.WithContext(ctx)}
}
