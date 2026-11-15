package personinfo

// 直接人物事实核验，对齐 MaiBot src/services/person_fact_verifier.py
// 该核验用于确认事实值确实来自目标用户原始发言，避免模型编造证据

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"MaiGo/internal/common/database"
)

var factTemporaryMarkers = []string{
	"今天", "昨天", "明天", "刚才", "暂时", "可能", "似乎", "也许", "打算", "计划",
}

var factOtherSubjectPrefixes = []string{
	"我们", "我朋友", "我的朋友", "我同事", "我的同事", "我妈", "我爸", "我家人",
}

var factSelfPrefixes = []string{"我", "本人", "自己"}

var factThirdPersonPrefixes = []string{"他", "她", "用户", "该用户"}

// DirectFactVerification 待核验的直接人物事实
type DirectFactVerification struct {
	PersonID      string
	PersonName    string
	SessionID     string
	MessageID     string
	Fact          string
	EvidenceQuote string
}

// FactVerificationResult 核验结果
type FactVerificationResult struct {
	Verified  bool   `json:"verified"`
	Reason    string `json:"reason,omitempty"`
	PersonID  string `json:"person_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// VerifyDirectPersonFact 核验事实是否由目标用户唯一一条原始消息直接支持
func (r *Registry) VerifyDirectPersonFact(ctx context.Context, in DirectFactVerification) (FactVerificationResult, error) {
	fact := strings.TrimSpace(in.Fact)
	quote := strings.TrimSpace(in.EvidenceQuote)
	personID := strings.TrimSpace(in.PersonID)
	if fact == "" || quote == "" || personID == "" {
		return FactVerificationResult{Verified: false, Reason: "fact、evidence_quote、person_id 均为必填"}, nil
	}
	message, err := r.findEvidenceMessage(ctx, in.SessionID, in.MessageID)
	if err != nil {
		return FactVerificationResult{}, err
	}
	if message == nil {
		return FactVerificationResult{Verified: false, Reason: "证据消息不存在或不唯一"}, nil
	}
	platform := normalizePlatform(message.Platform)
	userID := strings.TrimSpace(message.ExternalUserID)
	if userID == "" && message.UserID > 0 {
		userID = strconv.FormatInt(message.UserID, 10)
	}
	if platform == "" || userID == "" || userID == "0" {
		return FactVerificationResult{Verified: false, Reason: "证据消息缺少真实平台身份"}, nil
	}
	if PersonID(platform, userID) != personID {
		return FactVerificationResult{Verified: false, Reason: "证据消息发送者与人物不匹配"}, nil
	}
	if r.IsBotSelf(platform, userID) {
		return FactVerificationResult{Verified: false, Reason: "证据消息来自机器人自身"}, nil
	}
	messageText := message.TextContent
	if strings.TrimSpace(messageText) == "" {
		messageText = message.ProcessedPlainText
	}
	if ok, reason := verifyDirectFactText(fact, quote, messageText, in.PersonName); !ok {
		return FactVerificationResult{Verified: false, Reason: reason, PersonID: personID, MessageID: in.MessageID}, nil
	}
	return FactVerificationResult{Verified: true, PersonID: personID, MessageID: in.MessageID}, nil
}

func (r *Registry) findEvidenceMessage(ctx context.Context, sessionID, messageID string) (*messageLogRow, error) {
	sessionID = strings.TrimSpace(sessionID)
	messageID = strings.TrimSpace(messageID)
	if sessionID == "" || messageID == "" {
		return nil, nil
	}
	numericID, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || numericID < 0 {
		numericID = 0
	}
	db := r.db(ctx)
	if db == nil {
		return nil, ErrMemoryUnavailable
	}
	if !db.Migrator().HasTable("mai_messages") {
		return nil, nil
	}
	hasExternal := db.Migrator().HasColumn("mai_messages", "external_message_id") &&
		db.Migrator().HasColumn("mai_messages", "external_user_id")
	columns := "platform, user_id, processed_plain_text, processed_plain_text, message_id, session_id"
	if hasExternal {
		columns += ", external_message_id, external_user_id"
	}
	query := db.Table("mai_messages").Select(columns).
		Where(memory.NoticeExternalMessageIDPredicate("external_message_id"))
	if numericID > 0 {
		if hasExternal {
			query = query.Where("session_id = ? AND (message_id = ? OR external_message_id = ?)", sessionID, numericID, messageID)
		} else {
			query = query.Where("session_id = ? AND message_id = ?", sessionID, numericID)
		}
	} else if hasExternal {
		query = query.Where("session_id = ? AND external_message_id = ?", sessionID, messageID)
	} else {
		return nil, nil
	}
	var rows []messageLogRow
	if err := query.Limit(2).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("personinfo: 查询证据消息失败: %w", err)
	}
	if len(rows) != 1 {
		return nil, nil
	}
	return &rows[0], nil
}

type messageLogRow struct {
	Platform           string `gorm:"column:platform"`
	UserID             int64  `gorm:"column:user_id"`
	ExternalUserID     string `gorm:"column:external_user_id"`
	ExternalMessageID  string `gorm:"column:external_message_id"`
	TextContent        string `gorm:"column:processed_plain_text"`
	ProcessedPlainText string `gorm:"column:processed_plain_text"`
	OneBotMessageID    int64  `gorm:"column:message_id"`
	SessionID          string `gorm:"column:session_id"`
}

// verifyDirectFactText 纯文本核验，便于单元测试
func verifyDirectFactText(fact, quote, messageText, personName string) (bool, string) {
	if !verifyFactPredicate(fact, quote, personName) {
		return false, "事实谓语与证据发言不一致或含不确定表述"
	}
	if !verifyQuotePlacement(messageText, quote) {
		return false, "证据引文未逐字出现在原始发言中或上下文不合法"
	}
	return true, ""
}

func verifyFactPredicate(fact, source, personName string) bool {
	normalizedSource := normalizeFactText(source)
	if normalizedSource == "" {
		return false
	}
	for _, marker := range factTemporaryMarkers {
		if strings.Contains(normalizedSource, normalizeFactText(marker)) {
			return false
		}
	}
	factPredicate := factPredicateOf(fact, personName, false)
	sourcePredicate := factPredicateOf(source, personName, true)
	if factPredicate == "" || sourcePredicate == "" {
		return false
	}
	return factPredicate == sourcePredicate
}

// factPredicateOf 抽取主语之后的行为谓语
//
// 对齐 Python _predicate：只有 firstPerson（证据自述）才拒绝「我们/我朋友」等他人主语；
// 事实侧用 (person_name, 第三人称) 前缀，且谓语至少两个字符。
func factPredicateOf(text, personName string, firstPerson bool) string {
	normalized := normalizeFactText(text)
	if normalized == "" {
		return ""
	}
	if firstPerson {
		for _, prefix := range factOtherSubjectPrefixes {
			if strings.HasPrefix(normalized, normalizeFactText(prefix)) {
				return ""
			}
		}
	}
	var prefixes []string
	if firstPerson {
		prefixes = sortedPrefixes(factSelfPrefixes, "")
	} else {
		prefixes = sortedPrefixes(factThirdPersonPrefixes, personName)
	}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(normalized, prefix) {
			continue
		}
		predicate := normalized[len(prefix):]
		if len([]rune(predicate)) >= 2 {
			return predicate
		}
		return ""
	}
	return ""
}

func sortedPrefixes(prefixes []string, personName string) []string {
	values := make([]string, 0, len(prefixes)+1)
	if name := normalizeFactText(personName); name != "" {
		values = append(values, name)
	}
	for _, prefix := range prefixes {
		values = append(values, normalizeFactText(prefix))
	}
	// 按长度降序，避免「他」抢先匹配「他们」
	for i := 0; i < len(values); i++ {
		for j := i + 1; j < len(values); j++ {
			if len(values[j]) > len(values[i]) {
				values[i], values[j] = values[j], values[i]
			}
		}
	}
	return values
}

func normalizeFactText(text string) string {
	normalized := strings.ToLower(norm.NFKC.String(strings.TrimSpace(text)))
	var builder strings.Builder
	for _, r := range normalized {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func normalizeFactMessage(text string) string {
	return strings.ToLower(norm.NFKC.String(strings.TrimSpace(text)))
}

func verifyQuotePlacement(messageText, quote string) bool {
	message := normalizeFactMessage(messageText)
	quoted := normalizeFactMessage(quote)
	if message == "" || quoted == "" {
		return false
	}
	lastRune := []rune(quoted)[len([]rune(quoted))-1]
	if lastRune == '？' || lastRune == '?' {
		return false
	}
	position := strings.Index(message, quoted)
	if position < 0 {
		return false
	}
	prefix := strings.TrimRightFunc(message[:position], unicode.IsSpace)
	if prefix != "" {
		lastPrefix := []rune(prefix)[len([]rune(prefix))-1]
		if !isFactSentenceBoundary(lastPrefix) {
			return false
		}
	}
	suffix := strings.TrimSpace(message[position+len(quoted):])
	return suffix == "" || (len([]rune(suffix)) == 1 && isFactQuoteSuffix([]rune(suffix)[0]))
}

func isFactSentenceBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '\n':
		return true
	default:
		return false
	}
}

func isFactQuoteSuffix(r rune) bool {
	switch r {
	case '。', '！', '；':
		return true
	default:
		return false
	}
}
