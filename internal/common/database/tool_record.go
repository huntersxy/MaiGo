package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

// ToolRecord 工具调用记录，语义对齐 MaiBot database_model.py ToolRecord / tool_records 表
type ToolRecord struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ToolID        string    `gorm:"column:tool_id;type:text;not null;default:''" json:"tool_id"`
	Timestamp     time.Time `gorm:"column:timestamp;index" json:"timestamp"`
	SessionID     string    `gorm:"column:session_id;type:text;not null;default:''" json:"session_id"`
	ToolName      string    `gorm:"column:tool_name;type:text;not null;default:''" json:"tool_name"`
	ToolReasoning *string   `gorm:"column:tool_reasoning;type:text" json:"tool_reasoning,omitempty"`
	ToolData      string    `gorm:"column:tool_data;type:text" json:"tool_data,omitempty"`
}

// TableName 固定工具记录表名
func (ToolRecord) TableName() string { return "tool_records" }

// ToolRecordInput 写入一条工具调用记录
type ToolRecordInput struct {
	ToolID        string
	SessionID     string
	ToolName      string
	ToolReasoning string
	ToolData      any
}

// StoreToolInfo 保存工具调用记录；tool_id 为空时使用微秒时间戳，对齐 Python store_tool_info
func (m *Manager) StoreToolInfo(ctx context.Context, input ToolRecordInput) (*ToolRecord, error) {
	if m == nil || m.db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("工具记录缺少 session_id")
	}
	toolID := strings.TrimSpace(input.ToolID)
	if toolID == "" {
		toolID = fmt.Sprintf("%d", time.Now().UnixMicro())
	}
	data := input.ToolData
	if data == nil {
		data = map[string]any{}
	}
	raw, err := sonic.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("序列化工具记录失败: %w", err)
	}
	reasoning := input.ToolReasoning
	record := &ToolRecord{
		ToolID:        toolID,
		Timestamp:     time.Now(),
		SessionID:     sessionID,
		ToolName:      strings.TrimSpace(input.ToolName),
		ToolReasoning: &reasoning,
		ToolData:      string(raw),
	}
	var existing ToolRecord
	err = m.db.WithContext(ctx).Where("tool_id=?", toolID).Order("id DESC").First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		existing.Timestamp = record.Timestamp
		existing.SessionID = record.SessionID
		existing.ToolName = record.ToolName
		existing.ToolReasoning = record.ToolReasoning
		existing.ToolData = record.ToolData
		if saveErr := m.db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
			return nil, saveErr
		}
		return &existing, nil
	}
	if err := m.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, err
	}
	return record, nil
}

// GetToolRecord 按 tool_id 读取工具记录
func (m *Manager) GetToolRecord(ctx context.Context, toolID string) (*ToolRecord, error) {
	toolID = strings.TrimSpace(toolID)
	if toolID == "" {
		return nil, fmt.Errorf("工具记录编号不能为空")
	}
	var record ToolRecord
	if err := m.db.WithContext(ctx).Where("tool_id=?", toolID).Order("id DESC").First(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

// ListToolRecordsBySession 按会话倒序读取工具记录
func (m *Manager) ListToolRecordsBySession(ctx context.Context, sessionID string, limit int) ([]ToolRecord, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("会话编号不能为空")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []ToolRecord
	err := m.db.WithContext(ctx).Where("session_id=?", sessionID).Order("timestamp DESC, id DESC").Limit(limit).Find(&records).Error
	return records, err
}
