package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

// ContextItem 对齐 history 中的 ContextItem 快照；图片以 Path 持久化，Data 仅用于入队
type ContextItem struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Path     string `json:"path,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Name     string `json:"name,omitempty"`
	Data     string `json:"data,omitempty"`
	// LogicalTurnID 对齐 Python ContextItemMeta.logical_turn_id，供 WebUI 事件关联回合
	LogicalTurnID string `json:"logical_turn_id,omitempty"`
}

// HistoryRecord 对齐 history.jsonl 的一条记录
type HistoryRecord struct {
	ID      string        `json:"id"`
	Time    string        `json:"time"`
	Kind    string        `json:"kind"`
	ReplyTo string        `json:"reply_to,omitempty"`
	Items   []ContextItem `json:"items"`
}

// PendingTurn 记录未确认结束的回合
type PendingTurn struct {
	InputIDs []string `json:"input_ids"`
}

// BackendState 对齐 acp_agent state.json 的单个 backend
type BackendState struct {
	SessionID  string       `json:"session_id,omitempty"`
	CWD        string       `json:"cwd,omitempty"`
	Policy     string       `json:"policy,omitempty"`
	StopReason string       `json:"stop_reason,omitempty"`
	Pending    *PendingTurn `json:"pending,omitempty"`
}

// agentStateFile 对齐 AcpAgent.save 的状态文件
type agentStateFile struct {
	Inbox    []HistoryRecord          `json:"inbox"`
	Backends map[string]*BackendState `json:"backends"`
}

// AgentHistory 对齐 agent/history.py：JSONL 追加、去重、图片落盘
type AgentHistory struct {
	root string
	mu   sync.Mutex
}

// NewAgentHistory 创建历史目录
func NewAgentHistory(root string) *AgentHistory {
	return &AgentHistory{root: root}
}

// Root 返回 agent 数据根
func (h *AgentHistory) Root() string { return h.root }

func (h *AgentHistory) path() string {
	return filepath.Join(h.root, "rollout.jsonl")
}

// Load 读取全部记录，按 id 去重后返回首次出现的顺序
func (h *AgentHistory) Load() ([]HistoryRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loadLocked()
}

func (h *AgentHistory) loadLocked() ([]HistoryRecord, error) {
	file, err := os.Open(h.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	seen := map[string]bool{}
	var records []HistoryRecord
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record HistoryRecord
		if err := sonic.ConfigStd.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("解析 Agent 历史失败: %w", err)
		}
		if record.ID == "" || seen[record.ID] {
			continue
		}
		seen[record.ID] = true
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// Append 追加记录；同一 id 只保留首次写入
func (h *AgentHistory) Append(records ...HistoryRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(records) == 0 {
		return nil
	}
	existing, err := h.loadLocked()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(existing))
	for _, record := range existing {
		seen[record.ID] = true
	}
	if err := os.MkdirAll(h.root, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(h.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, record := range records {
		if record.ID == "" {
			record.ID = randomRecordID()
		}
		if seen[record.ID] {
			continue
		}
		line, err := sonic.ConfigStd.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			return err
		}
		seen[record.ID] = true
	}
	return file.Sync()
}

// Pack 把图片 base64 写入 images/<sha256>.<ext>，返回可持久化的快照
func (h *AgentHistory) Pack(items []ContextItem) ([]ContextItem, error) {
	out := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if item.Type != "image" || strings.TrimSpace(item.Data) == "" {
			out = append(out, item)
			continue
		}
		data, err := base64.StdEncoding.DecodeString(item.Data)
		if err != nil {
			return nil, fmt.Errorf("ACP 图片 base64 无效: %w", err)
		}
		sum := sha256.Sum256(data)
		format := imageFormat(item.MIMEType)
		dir := filepath.Join(h.root, "images")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, hex.EncodeToString(sum[:])+"."+format)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		item.Path = path
		item.Data = ""
		if item.MIMEType == "" {
			item.MIMEType = "image/" + format
		}
		out = append(out, item)
	}
	return out, nil
}

// Unpack 还原图片 base64，供 ACP prompt 使用
func (h *AgentHistory) Unpack(items []ContextItem) ([]ContextItem, error) {
	out := make([]ContextItem, 0, len(items))
	for _, item := range items {
		if item.Type == "image" && item.Path != "" && item.Data == "" {
			raw, err := os.ReadFile(item.Path)
			if err != nil {
				return nil, fmt.Errorf("读取 ACP 图片失败: %w", err)
			}
			item.Data = base64.StdEncoding.EncodeToString(raw)
			if item.MIMEType == "" {
				item.MIMEType = mimeForPath(item.Path)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// NewRecord 创建一条快照记录；等同于 Python record pack
func NewRecord(kind string, replyTo string, items []ContextItem, history *AgentHistory) (HistoryRecord, error) {
	packed, err := history.Pack(items)
	if err != nil {
		return HistoryRecord{}, err
	}
	return HistoryRecord{
		ID:      randomRecordID(),
		Time:    time.Now().UTC().Format(time.RFC3339),
		Kind:    kind,
		ReplyTo: replyTo,
		Items:   packed,
	}, nil
}

func imageFormat(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}

func mimeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

func expandUserPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Abs(path)
}
