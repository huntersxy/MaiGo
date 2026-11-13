package maisaka

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// MaxStoredResultsPerSession 每个聊天最多暂存的原始结果条数，超出后丢弃最早的
const MaxStoredResultsPerSession = 20

// StoredToolResult 一条暂存的工具原始结果
type StoredToolResult struct {
	ResultID  string
	ToolName  string
	Content   string
	CreatedAt time.Time
}

// ToolResultStore 按聊天分组暂存工具原始结果
type ToolResultStore struct {
	mu                 sync.Mutex
	resultsBySession   map[string][]StoredToolResult
	nextIndexBySession map[string]int
}

// NewToolResultStore 创建工具结果暂存表
func NewToolResultStore() *ToolResultStore {
	return &ToolResultStore{
		resultsBySession:   map[string][]StoredToolResult{},
		nextIndexBySession: map[string]int{},
	}
}

// DefaultToolResultStore 进程级默认暂存表，对应 Python 的 tool_result_store
var DefaultToolResultStore = NewToolResultStore()

// Save 暂存一条原始结果，返回在该聊天内唯一的短编号（如 r3）
func (s *ToolResultStore) Save(sessionID, toolName, content string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	nextIndex := s.nextIndexBySession[sessionID] + 1
	s.nextIndexBySession[sessionID] = nextIndex
	resultID := fmt.Sprintf("r%d", nextIndex)
	s.resultsBySession[sessionID] = append(s.resultsBySession[sessionID], StoredToolResult{
		ResultID:  resultID,
		ToolName:  toolName,
		Content:   content,
		CreatedAt: time.Now(),
	})
	for len(s.resultsBySession[sessionID]) > MaxStoredResultsPerSession {
		s.resultsBySession[sessionID] = s.resultsBySession[sessionID][1:]
	}
	return resultID
}

// Get 按编号取回原始结果；已被淘汰或进程重启后不存在时返回 false
func (s *ToolResultStore) Get(sessionID, resultID string) (StoredToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := strings.TrimSpace(resultID)
	results := s.resultsBySession[sessionID]
	for index := len(results) - 1; index >= 0; index-- {
		if results[index].ResultID == target {
			return results[index], true
		}
	}
	return StoredToolResult{}, false
}
