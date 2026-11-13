package learners

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// 对齐 src/learners/expression_review_store.py

const (
	ReviewEventAI                  = "ai_review"
	ReviewEventManualRescue        = "manual_rescue"
	DefaultExpressionReviewLogPath = "logs/expression_review/review_logs.json"
)

// ExpressionReviewState 对齐 local_storage 中的表达审核状态
type ExpressionReviewState struct {
	Checked       bool
	Rejected      bool
	ModifiedBy    *string
	ModifiedByRaw any
}

// AIReviewLogInput append_ai_review_log 入参
type AIReviewLogInput struct {
	SessionID    string
	Situation    string
	Style        string
	Passed       bool
	Reason       string
	Source       string
	ExpressionID *uint
	Error        string
}

// DefaultLocalStorePath 对齐 local_storage 的默认本地存储文件
const DefaultLocalStorePath = "data/local_store.json"

// ExpressionReviewStore 表达审核日志与审核状态存储
type ExpressionReviewStore struct {
	mu             sync.Mutex
	path           string
	localStorePath string
	states         map[uint]ExpressionReviewState
	localStore     map[string]any
	loaded         bool
	locker         reviewLogLocker
}

func NewExpressionReviewStore(path string) *ExpressionReviewStore {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultExpressionReviewLogPath
	}
	return &ExpressionReviewStore{
		path:           path,
		localStorePath: DefaultLocalStorePath,
		states:         map[uint]ExpressionReviewState{},
	}
}

// SetLocalStorePath 覆盖审核状态持久化文件路径（测试隔离用）
func (s *ExpressionReviewStore) SetLocalStorePath(path string) {
	if s == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultLocalStorePath
	}
	s.mu.Lock()
	s.localStorePath = path
	s.loaded = false
	s.localStore = nil
	s.states = map[uint]ExpressionReviewState{}
	s.mu.Unlock()
}

func reviewStateKey(expressionID uint) string {
	return fmt.Sprintf("expression_review:%d", expressionID)
}

var defaultExpressionReviewStore = NewExpressionReviewStore("")

// DefaultExpressionReviewStore 返回进程级审核日志存储
func DefaultExpressionReviewStore() *ExpressionReviewStore {
	return defaultExpressionReviewStore
}

// SetExpressionReviewLogPath 覆盖默认审核日志路径
func SetExpressionReviewLogPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultExpressionReviewLogPath
	}
	store := DefaultExpressionReviewStore()
	store.mu.Lock()
	store.path = path
	store.locker = nil
	store.mu.Unlock()
}

func (s *ExpressionReviewStore) Path() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *ExpressionReviewStore) GetReviewState(expressionID uint) ExpressionReviewState {
	if s == nil || expressionID == 0 {
		return ExpressionReviewState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocalStoreLocked()
	if value, exists := s.localStore[reviewStateKey(expressionID)]; exists {
		if payload, ok := value.(map[string]any); ok {
			return parseExpressionReviewState(payload)
		}
		return ExpressionReviewState{}
	}
	return s.states[expressionID]
}

func (s *ExpressionReviewStore) SetReviewState(expressionID uint, state ExpressionReviewState) {
	if s == nil || expressionID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLocalStoreLocked()
	if s.localStore == nil {
		s.localStore = map[string]any{}
	}
	var modifiedBy any
	if state.ModifiedByRaw != nil {
		modifiedBy = state.ModifiedByRaw
	} else if state.ModifiedBy != nil {
		modifiedBy = *state.ModifiedBy
	}
	payload := map[string]any{
		"checked":     state.Checked,
		"rejected":    state.Rejected,
		"modified_by": modifiedBy,
	}
	s.localStore[reviewStateKey(expressionID)] = payload
	s.states[expressionID] = state
	if err := s.saveLocalStoreLocked(); err != nil {
		zap.L().Warn("表达审核状态持久化失败",
			zap.String("path", s.localStorePath),
			zap.Error(err))
	}
}

func parseExpressionReviewState(value map[string]any) ExpressionReviewState {
	checked, _ := value["checked"].(bool)
	rejected, _ := value["rejected"].(bool)
	rawModifiedBy := value["modified_by"]
	var modifiedBy *string
	if text, ok := rawModifiedBy.(string); ok && strings.TrimSpace(text) != "" {
		trimmed := strings.TrimSpace(text)
		modifiedBy = &trimmed
	}
	return ExpressionReviewState{Checked: checked, Rejected: rejected, ModifiedBy: modifiedBy, ModifiedByRaw: rawModifiedBy}
}

func (s *ExpressionReviewStore) ensureLocalStoreLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.localStore = map[string]any{}
	raw, err := os.ReadFile(s.localStorePath)
	if err != nil {
		if !os.IsNotExist(err) {
			zap.L().Warn("本地存储读取失败，按空存储处理",
				zap.String("path", s.localStorePath),
				zap.Error(err))
		}
		return
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		backup := s.localStorePath + ".broken"
		_ = os.Rename(s.localStorePath, backup)
		zap.L().Warn("本地存储损坏，已备份并重建",
			zap.String("path", s.localStorePath),
			zap.String("backup", backup),
			zap.Error(err))
		return
	}
	store, ok := parsed.(map[string]any)
	if !ok {
		_ = os.Remove(s.localStorePath)
		zap.L().Warn("本地存储根节点不是 JSON 对象，已重建", zap.String("path", s.localStorePath))
		return
	}
	s.localStore = store
	for key, value := range store {
		if !strings.HasPrefix(key, "expression_review:") {
			continue
		}
		payload, ok := value.(map[string]any)
		if !ok {
			continue
		}
		idText := strings.TrimPrefix(key, "expression_review:")
		id, err := strconv.ParseUint(strings.TrimSpace(idText), 10, 64)
		if err != nil || id == 0 {
			continue
		}
		s.states[uint(id)] = parseExpressionReviewState(payload)
	}
}

func (s *ExpressionReviewStore) saveLocalStoreLocked() error {
	encoded, err := marshalJSONIndentedNoHTMLEscape(s.localStore)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.localStorePath, encoded, 0o644)
}

func (s *ExpressionReviewStore) ReadEntries() ([]map[string]any, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var entries []map[string]any
	err := s.withReviewLogLock(true, func() error {
		var readErr error
		entries, readErr = readReviewLogEntries(s.path)
		return readErr
	})
	if err == nil {
		return entries, nil
	}
	var corrupt *corruptedReviewLogError
	if !errors.As(err, &corrupt) {
		return nil, err
	}
	// 共享读锁发现损坏需升级为独占锁，避免多个读者同时重建
	err = s.withReviewLogLock(false, func() error {
		var readErr error
		entries, readErr = s.readEntriesLocked()
		return readErr
	})
	return entries, err
}

func (s *ExpressionReviewStore) resetCorruptedLog(path string, cause error) error {
	if err := writeFileAtomic(path, []byte("[]"), 0o644); err != nil {
		return err
	}
	zap.L().Warn("表达方式审核日志损坏，已删除并重建",
		zap.String("path", path),
		zap.Error(cause))
	return nil
}

func (s *ExpressionReviewStore) appendEntry(entry map[string]any) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.withReviewLogLock(false, func() error {
		entries, readErr := s.readEntriesLocked()
		if readErr != nil {
			return readErr
		}
		entries = append(entries, entry)
		encoded, encodeErr := marshalJSONIndentedNoHTMLEscape(entries)
		if encodeErr != nil {
			return encodeErr
		}
		return writeFileAtomic(s.path, encoded, 0o644)
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *ExpressionReviewStore) readEntriesLocked() ([]map[string]any, error) {
	entries, err := readReviewLogEntries(s.path)
	if err == nil {
		return entries, nil
	}
	var corrupt *corruptedReviewLogError
	if !errors.As(err, &corrupt) {
		return nil, err
	}
	if resetErr := s.resetCorruptedLog(s.path, corrupt); resetErr != nil {
		return nil, resetErr
	}
	return []map[string]any{}, nil
}

type corruptedReviewLogError struct {
	cause error
}

func (e *corruptedReviewLogError) Error() string {
	return e.cause.Error()
}

func (e *corruptedReviewLogError) Unwrap() error {
	return e.cause
}

// readReviewLogEntries 只读解析文件，不修改损坏日志
func readReviewLogEntries(path string) ([]map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &corruptedReviewLogError{cause: err}
	}
	list, ok := parsed.([]any)
	if !ok {
		return nil, &corruptedReviewLogError{cause: fmt.Errorf("表达方式审核日志格式异常，应为 JSON 数组")}
	}
	out := make([]map[string]any, 0, len(list))
	for _, entry := range list {
		if item, ok := entry.(map[string]any); ok {
			out = append(out, item)
		}
	}
	return out, nil
}

// marshalJSONIndentedNoHTMLEscape 对齐 Python json.dump(ensure_ascii=False, indent=2) 的转义行为
func marshalJSONIndentedNoHTMLEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// writeFileAtomic 同目录临时文件写完并 fsync 后 rename，避免半写 JSON
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func nowReviewTimestamp() string {
	return time.Now().Format("2006-01-02T15:04:05-07:00")
}

// newReviewLogID 对齐 Python uuid.uuid4().hex：32 位小写十六进制，无连字符
func newReviewLogID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

// AppendAIReviewLog 记录一次 AI 审核结果，供 WebUI 回看和人工救回
func (s *ExpressionReviewStore) AppendAIReviewLog(input AIReviewLogInput) (map[string]any, error) {
	var expressionID any
	if input.ExpressionID != nil {
		expressionID = int(*input.ExpressionID)
	}
	entry := map[string]any{
		"id":            newReviewLogID(),
		"event":         ReviewEventAI,
		"created_at":    nowReviewTimestamp(),
		"expression_id": expressionID,
		"session_id":    strings.TrimSpace(input.SessionID),
		"passed":        input.Passed,
		"reason":        strings.TrimSpace(input.Reason),
		"situation":     strings.TrimSpace(input.Situation),
		"style":         strings.TrimSpace(input.Style),
		"source":        input.Source,
	}
	if input.Error != "" {
		entry["error"] = input.Error
	}
	return s.appendEntry(entry)
}

// AppendManualRescueLog 记录一次从 AI 审核日志中人工恢复表达方式的操作
func (s *ExpressionReviewStore) AppendManualRescueLog(reviewLogID string, expressionID uint) (map[string]any, error) {
	entry := map[string]any{
		"id":            newReviewLogID(),
		"event":         ReviewEventManualRescue,
		"created_at":    nowReviewTimestamp(),
		"review_log_id": reviewLogID,
		"expression_id": int(expressionID),
	}
	return s.appendEntry(entry)
}

func parseReviewCreatedAt(value any) time.Time {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return time.Unix(0, 0)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999-07:00"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed
		}
	}
	return time.Unix(0, 0)
}

func rescueByReviewID(entries []map[string]any) map[string]map[string]any {
	var rescues []map[string]any
	for _, entry := range entries {
		event, _ := entry["event"].(string)
		reviewLogID := strings.TrimSpace(stringAny(entry["review_log_id"]))
		if event != ReviewEventManualRescue || reviewLogID == "" {
			continue
		}
		rescues = append(rescues, entry)
	}
	sort.SliceStable(rescues, func(i, j int) bool {
		return parseReviewCreatedAt(rescues[i]["created_at"]).After(parseReviewCreatedAt(rescues[j]["created_at"]))
	})
	out := map[string]map[string]any{}
	for _, rescue := range rescues {
		reviewLogID := strings.TrimSpace(stringAny(rescue["review_log_id"]))
		if reviewLogID == "" {
			continue
		}
		if _, exists := out[reviewLogID]; !exists {
			out[reviewLogID] = rescue
		}
	}
	return out
}

func withRescueState(entry, rescue map[string]any) map[string]any {
	enriched := make(map[string]any, len(entry)+3)
	for key, value := range entry {
		enriched[key] = value
	}
	enriched["rescued"] = rescue != nil
	if rescue != nil {
		enriched["rescued_expression_id"] = rescue["expression_id"]
		enriched["rescued_at"] = rescue["created_at"]
	} else {
		enriched["rescued_expression_id"] = nil
		enriched["rescued_at"] = nil
	}
	return enriched
}

// GetRecentAIReviewLogs 返回最近的 AI 审核记录并按 created_at 倒序
func (s *ExpressionReviewStore) GetRecentAIReviewLogs(limit int, passed *bool, sessionID string) ([]map[string]any, error) {
	entries, err := s.ReadEntries()
	if err != nil {
		return nil, err
	}
	rescues := rescueByReviewID(entries)
	normalizedSessionID := strings.TrimSpace(sessionID)

	reviews := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if !reviewLogEntryIsAI(entry) {
			continue
		}
		if strings.TrimSpace(stringAny(entry["id"])) == "" {
			continue
		}
		reviews = append(reviews, entry)
	}

	if passed != nil {
		filtered := reviews[:0]
		for _, entry := range reviews {
			entryPassed, _ := entry["passed"].(bool)
			if entryPassed == *passed {
				filtered = append(filtered, entry)
			}
		}
		reviews = filtered
	}

	if normalizedSessionID != "" {
		filtered := reviews[:0]
		for _, entry := range reviews {
			if strings.TrimSpace(stringAny(entry["session_id"])) == normalizedSessionID {
				filtered = append(filtered, entry)
			}
		}
		reviews = filtered
	}

	sort.SliceStable(reviews, func(i, j int) bool {
		return parseReviewCreatedAt(reviews[i]["created_at"]).After(parseReviewCreatedAt(reviews[j]["created_at"]))
	})
	if limit < 1 {
		limit = 1
	}
	if len(reviews) > limit {
		reviews = reviews[:limit]
	}
	out := make([]map[string]any, 0, len(reviews))
	for _, entry := range reviews {
		out = append(out, withRescueState(entry, rescues[strings.TrimSpace(stringAny(entry["id"]))]))
	}
	return out, nil
}

// GetAIReviewLog 按日志 ID 获取一条 AI 审核记录
func (s *ExpressionReviewStore) GetAIReviewLog(reviewLogID string) (map[string]any, error) {
	normalizedID := strings.TrimSpace(reviewLogID)
	if normalizedID == "" {
		return nil, nil
	}
	entries, err := s.ReadEntries()
	if err != nil {
		return nil, err
	}
	rescues := rescueByReviewID(entries)
	for _, entry := range entries {
		if !reviewLogEntryIsAI(entry) {
			continue
		}
		if strings.TrimSpace(stringAny(entry["id"])) == normalizedID {
			return withRescueState(entry, rescues[normalizedID]), nil
		}
	}
	return nil, nil
}

// reviewLogEntryIsAI 对齐 Python entry.get("event", AI_REVIEW_EVENT)：缺省视为 AI，空/其他值排除
func reviewLogEntryIsAI(entry map[string]any) bool {
	raw, exists := entry["event"]
	if !exists {
		return true
	}
	event, ok := raw.(string)
	return ok && event == ReviewEventAI
}

// stringAny 宽松转字符串，nil 视为空
func stringAny(value any) string {
	return jsonAnyText(value)
}
