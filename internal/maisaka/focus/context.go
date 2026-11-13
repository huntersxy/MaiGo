package focus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"maibot-go/internal/maisaka"
)

// CheckpointVersion 新版 Focus 检查点版本
const CheckpointVersion = 1

// CheckpointSourceCompaction 上下文压缩摘要消息的 source 标记
const CheckpointSourceCompaction = "context_compaction"

// DefaultCheckpointRoot 默认检查点目录，对应 Python PROJECT_ROOT/data/focus
const DefaultCheckpointRoot = "data/focus"

// IsSummaryMessage 是否为压缩摘要消息
func IsSummaryMessage(message maisaka.ContextMessage) bool {
	return message.Source == CheckpointSourceCompaction
}

// EmojiCandidateSource 表情候选快照消息的 source 标记
const EmojiCandidateSource = maisaka.EmojiCandidateSource

// toolResultMediaKind 工具结果媒体消息的 metadata.kind 标记
const toolResultMediaKind = maisaka.ToolResultMediaKind

// InputRecord 一条尚未被判断的真实输入编号
type InputRecord struct {
	Sequence  int    `json:"sequence"`
	MessageID string `json:"message_id"`
}

// Entry 一个入站消息或一个完整的模型—工具批次，压缩时不可拆分
type Entry struct {
	Messages   []maisaka.ContextMessage
	SessionIDs map[string]struct{}
	Sequence   int
	Watermarks map[string]int
	Inputs     map[string][]InputRecord
}

// NewEntry 构造条目并补默认容器
func NewEntry(messages []maisaka.ContextMessage, sessionIDs ...string) *Entry {
	entry := &Entry{
		Messages:   append([]maisaka.ContextMessage(nil), messages...),
		SessionIDs: make(map[string]struct{}, len(sessionIDs)),
		Watermarks: map[string]int{},
		Inputs:     map[string][]InputRecord{},
	}
	for _, sessionID := range sessionIDs {
		if sessionID != "" {
			entry.SessionIDs[sessionID] = struct{}{}
		}
	}
	return entry
}

// Progress 条目在各会话上的进度水位
func (e *Entry) Progress() map[string]int {
	if e == nil {
		return nil
	}
	if len(e.Watermarks) > 0 {
		return e.Watermarks
	}
	if e.Sequence == 0 {
		return nil
	}
	out := make(map[string]int, len(e.SessionIDs))
	for sessionID := range e.SessionIDs {
		out[sessionID] = e.Sequence
	}
	return out
}

// Size 条目中计入上下文的真实消息数
func (e *Entry) Size() int {
	if e == nil {
		return 0
	}
	size := 0
	for _, message := range e.Messages {
		if message.CountInContext {
			size++
		}
	}
	return size
}

// SummaryText 压缩输入文本，跳过历史摘要
func (e *Entry) SummaryText() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, len(e.Messages))
	for _, message := range e.Messages {
		if IsSummaryMessage(message) {
			continue
		}
		if text := maisaka.MessageTextForCompaction(message); text != "" {
			parts = append(parts, text)
		}
	}
	sessionIDs := e.SortedSessionIDs()
	canonical, _ := json.Marshal(sessionIDs)
	return fmt.Sprintf("session_ids=%s\n%s", canonical, strings.Join(parts, "\n"))
}

// SortedSessionIDs 排序后的会话编号
func (e *Entry) SortedSessionIDs() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.SessionIDs))
	for sessionID := range e.SessionIDs {
		out = append(out, sessionID)
	}
	sort.Strings(out)
	return out
}

// AddSessionID 增加会话归属
func (e *Entry) AddSessionID(sessionID string) {
	if e.SessionIDs == nil {
		e.SessionIDs = map[string]struct{}{}
	}
	if sessionID != "" {
		e.SessionIDs[sessionID] = struct{}{}
	}
}

// record 检查点里的单条消息
type record struct {
	Item          maisaka.ContextItem `json:"item"`
	Text          string              `json:"text"`
	Source        string              `json:"source"`
	Counted       bool                `json:"counted"`
	Role          string              `json:"role"`
	Timestamp     string              `json:"timestamp,omitempty"`
	Kind          string              `json:"kind"`
	SourceKind    string              `json:"source_kind,omitempty"`
	Metadata      map[string]any      `json:"metadata,omitempty"`
	ToolCallID    string              `json:"tool_call_id,omitempty"`
	MessageID     string              `json:"message_id,omitempty"`
	SessionID     string              `json:"session_id,omitempty"`
	LogicalTurnID string              `json:"logical_turn_id,omitempty"`
	ToolName      string              `json:"tool_name,omitempty"`
	Success       *bool               `json:"success,omitempty"`
	Content       string              `json:"content,omitempty"`
	Prefix        string              `json:"prefix,omitempty"`
	ReferenceType string              `json:"reference_type,omitempty"`
	EmojiHashes   map[string]string   `json:"emoji_hashes,omitempty"`
}

// focusKind 判定检查点消息类别，显式标记优先
func focusKind(message maisaka.ContextMessage) string {
	if kind, ok := message.Metadata["focus_kind"].(string); ok && kind != "" {
		return kind
	}
	switch {
	case message.Role == maisaka.RoleAssistant && (len(message.ToolCalls) > 0 || message.Reasoning != ""):
		return "model"
	case message.Role == maisaka.RoleTool:
		return "tool"
	case message.MetadataString("kind") == toolResultMediaKind:
		return "media"
	case message.Source == EmojiCandidateSource:
		return "emoji"
	case message.Source == maisaka.SourceReference || IsSummaryMessage(message):
		// 上下文摘要按 Python 语义同属 ReferenceMessage，靠 source 区分
		return "reference"
	case message.CountInContext:
		return "chat"
	default:
		return "internal"
	}
}

// SerializeMessage 保存恢复后直接使用的模型投影
func SerializeMessage(message maisaka.ContextMessage) record {
	rec := record{
		Item:       contextItemOf(message),
		Text:       message.Content,
		Source:     message.Source,
		Counted:    message.CountInContext,
		Role:       string(message.Role),
		Kind:       focusKind(message),
		Metadata:   message.Metadata,
		ToolName:   message.ToolName,
		ToolCallID: message.ToolCallID,
		MessageID:  message.MessageID,
		SessionID:  message.SessionID,
	}
	if !message.Timestamp.IsZero() {
		rec.Timestamp = message.Timestamp.Format(time.RFC3339Nano)
	}
	if rec.Kind == "model" {
		rec.SourceKind = message.Source
	}
	if rec.Kind == "reference" {
		rec.Content = message.Content
		if prefix, ok := message.Metadata["display_prefix"].(string); ok {
			rec.Prefix = prefix
		}
		if refType, ok := message.Metadata["reference_type"].(string); ok {
			rec.ReferenceType = refType
		}
	}
	if rec.Kind == "tool" {
		success := message.Metadata["success"]
		if value, ok := success.(bool); ok {
			rec.Success = &value
		}
	}
	if rec.Kind == "media" {
		// 媒体消息靠 tool_call_id/message_id/session_id 重建归属，正文另存 visible_text
		rec.ToolCallID = message.MetadataString("tool_call_id")
		rec.MessageID = message.MessageID
		rec.SessionID = message.SessionID
	}
	if rec.Kind == "emoji" {
		rec.EmojiHashes = emojiHashesRecord(message)
		rec.SessionID = message.SessionID
	}
	return rec
}

// emojiHashesRecord 把表情候选的序号映射转成检查点可序列化的字符串键字典
func emojiHashesRecord(message maisaka.ContextMessage) map[string]string {
	if message.Metadata == nil {
		return nil
	}
	raw, ok := message.Metadata["emoji_hashes"]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case map[string]any:
		out := make(map[string]string, len(value))
		for key, item := range value {
			if hash, ok := item.(string); ok {
				out[key] = hash
			}
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out
	default:
		return nil
	}
}

// DeserializeMessage 从检查点记录恢复消息
func DeserializeMessage(rec record) maisaka.ContextMessage {
	message := maisaka.ContextMessage{
		Role:           maisaka.ContextRole(rec.Role),
		Content:        rec.Text,
		Reasoning:      "",
		ToolCalls:      rec.Item.ToolCalls,
		ToolCallID:     firstNonEmptyValue(rec.Item.ToolCallID, rec.ToolCallID),
		ToolName:       firstNonEmptyValue(rec.ToolName, rec.Item.ToolName),
		Source:         rec.Source,
		CountInContext: rec.Counted,
		MessageID:      rec.MessageID,
		SessionID:      rec.SessionID,
		LogicalTurnID:  rec.LogicalTurnID,
		Images:         rec.Item.Images,
		Metadata:       rec.Metadata,
	}
	if message.Metadata == nil {
		message.Metadata = map[string]any{}
	}
	if rec.Kind == "model" {
		message.Role = maisaka.RoleAssistant
		message.Source = rec.SourceKind
	}
	if rec.Kind == "reference" {
		message.Content = rec.Content
		message.Source = maisaka.SourceReference
		message.Metadata["display_prefix"] = rec.Prefix
		message.Metadata["reference_type"] = rec.ReferenceType
		// 检查点恢复的参考消息按 Python 语义常驻，不参与一次性消费
		message.Metadata["remaining_uses"] = nil
	}
	if rec.Kind == "tool" && rec.Success != nil {
		message.Metadata["success"] = *rec.Success
	}
	if rec.Kind == "media" {
		// 媒体消息恢复为普通 user 内容消息，归属靠 tool_call_id 与 media 索引
		message.Role = maisaka.RoleUser
		message.Source = maisaka.SourceReference
		message.ToolCallID = rec.ToolCallID
		message.MessageID = rec.MessageID
		message.SessionID = rec.SessionID
		message.Metadata["kind"] = toolResultMediaKind
		message.Metadata["tool_call_id"] = rec.ToolCallID
	}
	if rec.Kind == "emoji" {
		message.Source = EmojiCandidateSource
		message.Metadata["emoji_hashes"] = rec.EmojiHashes
	}
	message.Metadata["focus_kind"] = rec.Kind
	if rec.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			message.Timestamp = ts
		}
	}
	if message.Content == "" && rec.Item.Text != "" {
		message.Content = rec.Item.Text
	}
	return message
}

func contextItemOf(message maisaka.ContextMessage) maisaka.ContextItem {
	items := maisaka.BuildContextItemsFromHistoryEntry(message, true)
	if len(items) == 0 {
		return maisaka.ContextItem{Role: message.Role, Text: message.Content}
	}
	return items[0]
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// checkpointEntry 检查点里的条目
type checkpointEntry struct {
	Messages   []record                 `json:"messages"`
	SessionIDs []string                 `json:"session_ids"`
	Sequence   int                      `json:"sequence"`
	Watermarks map[string]int           `json:"watermarks"`
	Inputs     map[string][]InputRecord `json:"inputs"`
}

// CheckpointMember 检查点里保存的会话身份，用于仅凭检查点重建 Session
type CheckpointMember struct {
	SessionID   string `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Platform    string `json:"platform,omitempty"`
	TargetID    int64  `json:"target_id,omitempty"`
	IsGroup     bool   `json:"is_group,omitempty"`
}

// Checkpoint 新版检查点
type Checkpoint struct {
	Version          int                         `json:"version"`
	ScopeID          string                      `json:"scope_id"`
	Members          map[string]CheckpointMember `json:"members,omitempty"`
	Sequence         int                         `json:"sequence"`
	Considered       map[string]int              `json:"considered"`
	Seen             []string                    `json:"seen"`
	WaitUntil        *float64                    `json:"wait_until"`
	WaitSessionID    *string                     `json:"wait_session_id"`
	WaitToken        *string                     `json:"wait_token"`
	ConsecutiveWaits int                         `json:"consecutive_waits"`
	Entries          []checkpointEntry           `json:"entries"`
}

// CheckpointState 保存时附带的运行态
type CheckpointState struct {
	Sequence         int
	Members          map[string]CheckpointMember
	Considered       map[string]int
	Seen             []string
	WaitUntil        *float64
	WaitSessionID    *string
	WaitToken        *string
	ConsecutiveWaits int
}

// CheckpointStore 仅保存新版状态；写入临时文件后原子替换
type CheckpointStore struct {
	scopeID string
	path    string
}

// NewCheckpointStore 构造共享组检查点存储
func NewCheckpointStore(scopeID, root string) *CheckpointStore {
	if root == "" {
		root = DefaultCheckpointRoot
	}
	sum := sha256Hex(scopeID)
	return &CheckpointStore{scopeID: scopeID, path: filepath.Join(root, sum+".json")}
}

// Path 检查点文件路径
func (s *CheckpointStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Load 读取检查点；不存在时返回空
func (s *CheckpointStore) Load() (*Checkpoint, error) {
	if s == nil {
		return nil, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var payload Checkpoint
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload.Version != CheckpointVersion || payload.ScopeID != s.scopeID {
		return nil, fmt.Errorf("Focus 检查点版本或共享组身份不匹配")
	}
	return &payload, nil
}

// Save 写入检查点
func (s *CheckpointStore) Save(entries []*Entry, state CheckpointState) error {
	payload := &Checkpoint{
		Version:          CheckpointVersion,
		ScopeID:          s.scopeID,
		Members:          state.Members,
		Sequence:         state.Sequence,
		Considered:       state.Considered,
		Seen:             state.Seen,
		WaitUntil:        state.WaitUntil,
		WaitSessionID:    state.WaitSessionID,
		WaitToken:        state.WaitToken,
		ConsecutiveWaits: state.ConsecutiveWaits,
	}
	if payload.Considered == nil {
		payload.Considered = map[string]int{}
	}
	if payload.Seen == nil {
		payload.Seen = []string{}
	}
	for _, entry := range entries {
		recorded := checkpointEntry{
			SessionIDs: entry.SortedSessionIDs(),
			Sequence:   entry.Sequence,
			Watermarks: entry.Watermarks,
			Inputs:     entry.Inputs,
		}
		if recorded.Watermarks == nil {
			recorded.Watermarks = map[string]int{}
		}
		if recorded.Inputs == nil {
			recorded.Inputs = map[string][]InputRecord{}
		}
		if recorded.SessionIDs == nil {
			recorded.SessionIDs = []string{}
		}
		for _, message := range entry.Messages {
			recorded.Messages = append(recorded.Messages, SerializeMessage(message))
		}
		payload.Entries = append(payload.Entries, recorded)
	}
	return s.write(payload)
}

func (s *CheckpointStore) write(payload *Checkpoint) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, s.path)
}

// FindCheckpointForSession 在检查点目录里查找包含指定会话的记录
func FindCheckpointForSession(sessionID, root string) (*Checkpoint, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if root == "" {
		root = DefaultCheckpointRoot
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var payload Checkpoint
		if err := json.Unmarshal(data, &payload); err != nil || payload.Version != CheckpointVersion {
			continue
		}
		if member, ok := payload.Members[sessionID]; ok && member.SessionID != "" {
			return &payload, nil
		}
		for _, entry := range payload.Entries {
			for _, id := range entry.SessionIDs {
				if id == sessionID {
					return &payload, nil
				}
			}
		}
	}
	return nil, nil
}

// PurgeSession 清理尚未加载的新版检查点里某会话的材料
func PurgeSession(sessionID string, excluded map[string]struct{}, root string) error {
	if root == "" {
		root = DefaultCheckpointRoot
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if _, skip := excluded[path]; skip {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var payload Checkpoint
		if err := json.Unmarshal(data, &payload); err != nil {
			return err
		}
		if payload.Version != CheckpointVersion {
			return fmt.Errorf("Focus 检查点版本不匹配")
		}
		store := NewCheckpointStore(payload.ScopeID, root)
		if store.Path() != path {
			return fmt.Errorf("Focus 检查点路径与共享组身份不匹配")
		}
		kept := payload.Entries[:0]
		for _, entry := range payload.Entries {
			drop := false
			for _, sid := range entry.SessionIDs {
				if sid == sessionID {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, entry)
			}
		}
		payload.Entries = kept
		delete(payload.Considered, sessionID)
		seen := payload.Seen[:0]
		for _, key := range payload.Seen {
			if !strings.HasPrefix(key, sessionID+"\x00") {
				seen = append(seen, key)
			}
		}
		payload.Seen = seen
		if payload.WaitSessionID != nil && *payload.WaitSessionID == sessionID {
			payload.WaitUntil = nil
			payload.WaitSessionID = nil
			payload.WaitToken = nil
			payload.ConsecutiveWaits = 0
		}
		if err := store.write(&payload); err != nil {
			return err
		}
	}
	return nil
}

// DecodeCheckpointEntries 把检查点条目还原为内存条目
func DecodeCheckpointEntries(payload *Checkpoint) []*Entry {
	if payload == nil {
		return nil
	}
	entries := make([]*Entry, 0, len(payload.Entries))
	for _, recorded := range payload.Entries {
		entry := NewEntry(nil, recorded.SessionIDs...)
		entry.Sequence = recorded.Sequence
		entry.Watermarks = recorded.Watermarks
		entry.Inputs = recorded.Inputs
		for _, rec := range recorded.Messages {
			entry.Messages = append(entry.Messages, DeserializeMessage(rec))
		}
		entries = append(entries, entry)
	}
	return entries
}
