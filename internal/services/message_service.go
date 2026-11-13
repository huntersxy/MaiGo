package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"maibot-go/internal/common/database"
	commonutils "maibot-go/internal/common/utils"
)

// MessageQuery 是消息查询条件，字段语义对齐 message_service.find_messages
type MessageQuery struct {
	SessionID            string
	StartTime            *time.Time
	EndTime              *time.Time
	BeforeTime           *time.Time
	MessageID            string
	Limit                int
	FilterBot            bool
	FilterCommand        bool
	FilterInterceptLevel *int
}

// ReadableOptions 对齐 build_readable_messages 参数
type ReadableOptions struct {
	ReplaceBotName         bool
	TimestampMode          string
	ReadMark               time.Time
	Truncate               bool
	GroupDates             bool
	IncludeReplyComponents bool
	ShowActions            bool
}

// MessageService 对齐 MaiBot message_service 的查询与可读渲染
type MessageService struct {
	DB      *gorm.DB
	SelfID  int64
	BotName string
	// SelfIDProvider 在 OneBot 延迟连接时提供实时账号，优先于构造期的 SelfID
	SelfIDProvider func() int64
}

func NewMessageService(db *gorm.DB, selfID int64, botName string) *MessageService {
	return &MessageService{DB: db, SelfID: selfID, BotName: botName}
}

// currentSelfID 返回当前机器人账号，延迟连接后也能正确排除自身消息
func (s *MessageService) currentSelfID() int64 {
	if s == nil {
		return 0
	}
	if s.SelfIDProvider != nil {
		if selfID := s.SelfIDProvider(); selfID > 0 {
			return selfID
		}
	}
	return s.SelfID
}

func (s *MessageService) db() (*gorm.DB, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("消息数据库未接线")
	}
	return s.DB, nil
}

func messageTimeRange(query MessageQuery) (time.Time, time.Time, bool) {
	if query.StartTime == nil || query.EndTime == nil {
		return time.Time{}, time.Time{}, false
	}
	return *query.StartTime, *query.EndTime, true
}

func (s *MessageService) queryMessages(ctx context.Context, query MessageQuery) ([]memory.MessageLog, error) {
	if query.Limit < 0 {
		return nil, errors.New("limit 不能为负数")
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	tx := db.WithContext(ctx).Model(&memory.MessageLog{}).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if start, end, ok := messageTimeRange(query); ok {
		tx = tx.Where("timestamp >= ? AND timestamp <= ?", start, end)
	} else {
		if query.StartTime != nil {
			tx = tx.Where("timestamp >= ?", *query.StartTime)
		}
		if query.EndTime != nil {
			tx = tx.Where("timestamp <= ?", *query.EndTime)
		}
	}
	if query.BeforeTime != nil {
		tx = tx.Where("timestamp < ?", *query.BeforeTime)
	}
	if strings.TrimSpace(query.SessionID) != "" {
		tx = tx.Where("session_id = ?", strings.TrimSpace(query.SessionID))
	}
	if strings.TrimSpace(query.MessageID) != "" {
		target := strings.TrimSpace(query.MessageID)
		if numeric, err := strconv.ParseInt(target, 10, 64); err == nil {
			// 显式括号避免 OR 与通知过滤谓词的 AND 优先级被 GORM 直接拼接后破坏
			tx = tx.Where("(message_id = ? OR external_message_id = ?)", numeric, target)
		} else {
			tx = tx.Where("external_message_id = ?", target)
		}
	}
	if query.FilterBot {
		if selfID := s.currentSelfID(); selfID != 0 {
			tx = tx.Where("user_id <> ?", selfID)
		}
	}
	if query.FilterCommand {
		tx = tx.Where("processed_plain_text NOT LIKE ? AND processed_plain_text NOT LIKE ?", "/%", "／%")
	}
	latest := query.Limit > 0
	if latest {
		tx = tx.Order("timestamp desc, id desc").Limit(query.Limit)
	} else {
		tx = tx.Order("timestamp asc, id asc")
	}
	var rows []memory.MessageLog
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询消息失败: %w", err)
	}
	// 对齐 Python message_repository.py:234-240：按 intercept_message_level 过滤
	rows = filterByInterceptLevel(rows, query.FilterInterceptLevel)
	if latest {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	return rows, nil
}

// GetMessagesByTime 按时间范围查询
func (s *MessageService) GetMessagesByTime(ctx context.Context, start, end time.Time, limit int, filterBot bool) ([]memory.MessageLog, error) {
	return s.queryMessages(ctx, MessageQuery{StartTime: &start, EndTime: &end, Limit: limit, FilterBot: filterBot})
}

// GetMessagesByTimeInChat 按会话与时间范围查询
func (s *MessageService) GetMessagesByTimeInChat(ctx context.Context, sessionID string, start, end time.Time, limit int, filterBot bool) ([]memory.MessageLog, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("chat_id 不能为空")
	}
	return s.queryMessages(ctx, MessageQuery{SessionID: sessionID, StartTime: &start, EndTime: &end, Limit: limit, FilterBot: filterBot})
}

// GetMessageByID 按消息 ID 查询单条消息
func (s *MessageService) GetMessageByID(ctx context.Context, messageID, sessionID string) (*memory.MessageLog, error) {
	if strings.TrimSpace(messageID) == "" {
		return nil, errors.New("message_id 不能为空")
	}
	rows, err := s.queryMessages(ctx, MessageQuery{SessionID: sessionID, MessageID: messageID, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// ListMessagesByID 按平台、会话和消息 ID 查询最多两条消息
// 对齐 MaiBot message.py:454-462 的 limit(2) 查询，用于识别同聊天流内的重复消息 ID
// Python 原查询未声明排序，这里固定按自增主键升序，避免依赖数据库默认返回顺序
func (s *MessageService) ListMessagesByID(ctx context.Context, platform, sessionID, messageID string) ([]memory.MessageLog, error) {
	target := strings.TrimSpace(messageID)
	if target == "" {
		return nil, errors.New("message_id 不能为空")
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	tx := db.WithContext(ctx).Model(&memory.MessageLog{}).
		Where("platform = ? AND session_id = ?", strings.TrimSpace(platform), strings.TrimSpace(sessionID))
	if numeric, err := strconv.ParseInt(target, 10, 64); err == nil {
		tx = tx.Where("(message_id = ? OR external_message_id = ?)", numeric, target)
	} else {
		tx = tx.Where("external_message_id = ?", target)
	}
	var rows []memory.MessageLog
	if err := tx.Order("id asc").Limit(2).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询引用消息失败: %w", err)
	}
	return rows, nil
}

// GetMessagesBeforeTime 查询时间点之前的消息
func (s *MessageService) GetMessagesBeforeTime(ctx context.Context, timestamp time.Time, limit int, filterBot bool) ([]memory.MessageLog, error) {
	return s.queryMessages(ctx, MessageQuery{BeforeTime: &timestamp, Limit: limit, FilterBot: filterBot})
}

// GetMessagesBeforeTimeInChat 查询会话内时间点之前的消息
func (s *MessageService) GetMessagesBeforeTimeInChat(ctx context.Context, sessionID string, timestamp time.Time, limit int, filterBot bool) ([]memory.MessageLog, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("chat_id 不能为空")
	}
	return s.queryMessages(ctx, MessageQuery{SessionID: sessionID, BeforeTime: &timestamp, Limit: limit, FilterBot: filterBot})
}

// CountNewMessages 统计时间范围内的新消息
func (s *MessageService) CountNewMessages(ctx context.Context, sessionID string, start time.Time, end *time.Time) (int64, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, errors.New("chat_id 不能为空")
	}
	db, err := s.db()
	if err != nil {
		return 0, err
	}
	tx := db.WithContext(ctx).Model(&memory.MessageLog{}).Where("session_id = ? AND timestamp >= ?", strings.TrimSpace(sessionID), start).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if end != nil {
		tx = tx.Where("timestamp <= ?", *end)
	}
	var count int64
	if err := tx.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("统计消息失败: %w", err)
	}
	return count, nil
}

func messageDisplayText(row memory.MessageLog) string {
	if strings.TrimSpace(row.ProcessedPlainText) != "" {
		return row.ProcessedPlainText
	}
	return row.ProcessedPlainText
}

func messageUserName(row memory.MessageLog) string {
	if strings.TrimSpace(row.UserNickname) != "" {
		return row.UserNickname
	}
	if strings.TrimSpace(row.ExternalUserID) != "" {
		return row.ExternalUserID
	}
	return strconv.FormatInt(row.UserID, 10)
}

// relativeMessageTime 对齐 math_utils.translate_timestamp_to_human_readable(RELATIVE)
func relativeMessageTime(delta time.Duration, absolute time.Time) string {
	return commonutils.RelativeTimestampText(delta, absolute)
}

// readableTimestamp 按模式格式化时间戳；未知模式按 relative 处理
//
// 模式名大小写不敏感，对齐 Python `mode.upper() in TimestampMode.__members__`。
func readableTimestamp(value time.Time, mode string, now time.Time) string {
	text, err := commonutils.TranslateTimestampToHumanReadableAt(value, mode, now)
	if err != nil {
		return commonutils.RelativeTimestampText(now.Sub(value), value)
	}
	return text
}

func messageExternalID(row memory.MessageLog) string {
	if strings.TrimSpace(row.ExternalMessageID) != "" {
		return row.ExternalMessageID
	}
	return strconv.FormatInt(row.MessageID, 10)
}

func (s *MessageService) buildReadableLine(row memory.MessageLog, options readableOptions, now time.Time) string {
	plain := strings.TrimSpace(messageDisplayText(row))
	if options.ReplaceBotName && s.BotName != "" {
		plain = strings.ReplaceAll(plain, s.BotName, "你")
	}
	prefix := make([]string, 0, 3)
	if options.TimestampMode != "none" {
		prefix = append(prefix, "["+readableTimestamp(row.Timestamp, options.TimestampMode, now)+"]")
	}
	if options.ShowMessageIDPrefix {
		prefix = append(prefix, "[消息ID: "+messageExternalID(row)+"]")
	}
	prefix = append(prefix, messageUserName(row)+"说：")
	return strings.Join(prefix, " ") + plain
}

type readableOptions struct {
	ReadableOptions
	ShowMessageIDPrefix bool
}

// BuildReadableMessages 对齐 message_service.build_readable_messages
func (s *MessageService) BuildReadableMessages(rows []memory.MessageLog, options ReadableOptions) (string, error) {
	return s.buildReadableMessages(rows, readableOptions{ReadableOptions: options})
}

func (s *MessageService) buildReadableMessages(rows []memory.MessageLog, options readableOptions) (string, error) {
	if options.GroupDates && options.TimestampMode != "normal" {
		return "", errors.New("按日期分组时必须使用 normal 固定时间模式")
	}

	now := time.Now()
	var builder strings.Builder
	unreadMarkAdded := false
	currentDate := ""
	for _, row := range rows {
		if !options.ReadMark.IsZero() && !unreadMarkAdded && row.Timestamp.After(options.ReadMark) {
			builder.WriteString("--- 以上消息是你已经看过，请关注以下未读的新消息 ---\n")
			unreadMarkAdded = true
		}
		if options.GroupDates {
			date := row.Timestamp.Format("2006-01-02")
			if date != currentDate {
				builder.WriteString("日期：" + date + "\n")
				currentDate = date
			}
			options.TimestampMode = "normal_no_ymd"
		}
		line := s.buildReadableLine(row, options, now)
		if options.Truncate && utf8.RuneCountInString(line) > 200 {
			runes := []rune(line)
			line = string(runes[:200]) + "......（内容太长了）"
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(line)
	}
	if options.ShowActions && len(rows) > 0 {
		// 对齐 Python message_service.py:272-280 / utils_action.py:9-36
		// 动作时间戳固定 relative；按首末消息的 session+时间窗过滤
		actionLines, actionErr := s.buildReadableActionRecords(
			rows[0].SessionID, rows[0].Timestamp, rows[len(rows)-1].Timestamp, now,
		)
		if actionErr != nil {
			return "", actionErr
		}
		if actionText := strings.TrimSpace(actionLines); actionText != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(actionText)
		}
	}
	return builder.String(), nil
}

// filterByInterceptLevel 按拦截等级过滤消息
// 对齐 Python message_repository.py:234-240：additional_config.intercept_message_level <= 阈值才保留
func filterByInterceptLevel(rows []memory.MessageLog, threshold *int) []memory.MessageLog {
	if threshold == nil || len(rows) == 0 {
		return rows
	}
	limit := *threshold
	filtered := make([]memory.MessageLog, 0, len(rows))
	for _, row := range rows {
		level := 0
		if raw, ok := row.AdditionalConfig["intercept_message_level"]; ok {
			if parsed, ok := numericInterceptLevel(raw); ok {
				level = parsed
			}
		}
		if level <= limit {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

// numericInterceptLevel 把配置值转为整数等级，无法解析时返回 false
func numericInterceptLevel(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// buildReadableActionRecords 把工具动作记录渲染为可读文本
// 对齐 Python get_actions_by_timestamp_with_chat + utils_action.build_readable_action_records
func (s *MessageService) buildReadableActionRecords(sessionID string, start, end, now time.Time) (string, error) {
	if s == nil || s.DB == nil {
		return "", nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	var records []memory.ToolRecord
	if err := s.DB.WithContext(context.Background()).
		Where("session_id = ?", sessionID).
		Where("timestamp >= ? AND timestamp <= ?", start, end).
		Order("timestamp ASC, id ASC").
		Find(&records).Error; err != nil {
		return "", fmt.Errorf("读取工具动作记录失败: %w", err)
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		stamp := readableTimestamp(record.Timestamp, "relative", now)
		lines = append(lines, "在"+stamp+"，你使用了"+record.ToolName)
	}
	return strings.Join(lines, "\n"), nil
}

// BuildReadableMessagesWithID 返回文本与消息 ID 列表
func (s *MessageService) BuildReadableMessagesWithID(rows []memory.MessageLog, options ReadableOptions) (string, []string, error) {
	options.GroupDates = false
	text, err := s.buildReadableMessages(rows, readableOptions{ReadableOptions: options, ShowMessageIDPrefix: true})
	if err != nil {
		return "", nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, messageExternalID(row))
	}
	return text, ids, nil
}

var userReferencePattern = regexp.MustCompile(`(回复|@)?<([^:<>]+):[^<>]+>`)

// ReplaceUserReferences 对齐 replace_user_references
func (s *MessageService) ReplaceUserReferences(text, platform string, replaceBotName bool) string {
	if text == "" {
		return text
	}
	return userReferencePattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := userReferencePattern.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		userName := parts[2]
		if replaceBotName && userName == s.BotName {
			userName = "你"
		}
		return parts[1] + userName
	})
}

// TranslatePIDToDescription 按图片 ID 查询描述
func (s *MessageService) TranslatePIDToDescription(ctx context.Context, pid string) (string, error) {
	db, err := s.db()
	if err != nil {
		return "[图片]", err
	}
	if !db.Migrator().HasTable("images") {
		return "[图片]", nil
	}
	id, parseErr := strconv.ParseInt(strings.TrimSpace(pid), 10, 64)
	if parseErr != nil {
		return "[图片]", nil
	}
	var row struct {
		Description string
	}
	result := db.WithContext(ctx).Table("images").Select("description").Where("id = ?", id).Limit(1).Scan(&row)
	if result.Error != nil {
		return "[图片]", fmt.Errorf("查询图片描述失败: %w", result.Error)
	}
	if strings.TrimSpace(row.Description) == "" {
		return "[图片]", nil
	}
	return strings.TrimSpace(row.Description), nil
}
