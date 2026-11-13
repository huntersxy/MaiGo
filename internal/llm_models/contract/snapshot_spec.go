package contract

// 失败请求快照的描述结构。
//
// 对齐 MaiBot src/llm_models/request_snapshot.py 的 RequestSnapshot 数据部分：
// 客户端在失败时填充，快照层负责落盘。两侧都需要这个结构，因此放在契约层，
// 避免「客户端 -> 快照层」的反向依赖。

import (
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// 请求类型常量，对齐 MaiBot request_kind 取值
const (
	RequestKindResponse       = "response"
	RequestKindEmbedding      = "embedding"
	RequestKindImageEmbedding = "image_embedding"
	RequestKindAudio          = "audio"
)

// RequestSnapshotSpec 一次失败请求的可诊断信息
type RequestSnapshotSpec struct {
	ClientType      string
	ProviderName    string
	BaseURL         string
	Model           string
	Operation       string
	RequestKind     string
	TaskName        string
	SessionID       string
	Input           []*schema.AgenticMessage
	ContextItems    []ContextItem
	Options         *model.Options
	ProviderRequest map[string]any
	InternalRequest map[string]any
	// LogicalTurnID 同一逻辑轮次固定的轮次标识，重试快照必须复用
	LogicalTurnID string
	Config        ModelConfig
	Error         error
	Recorder      *AttemptRecorder
	Attempts      []map[string]any
	StartedAt     time.Time
}

// NewRequestSnapshotSpec 用模型配置填充快照元数据
func NewRequestSnapshotSpec(cfg ModelConfig, operation, requestKind string, input []*schema.AgenticMessage, options *model.Options, providerRequest, internalRequest map[string]any) RequestSnapshotSpec {
	return RequestSnapshotSpec{
		ClientType:      cfg.ClientType,
		ProviderName:    cfg.ProviderName,
		BaseURL:         cfg.BaseURL,
		Model:           cfg.Model,
		Operation:       operation,
		RequestKind:     requestKind,
		TaskName:        cfg.TaskName,
		SessionID:       cfg.SessionID,
		Input:           input,
		Options:         options,
		ProviderRequest: providerRequest,
		InternalRequest: internalRequest,
		Config:          cfg,
	}
}

// SnapshotLogicalTurnID 从尝试记录器取本轮固定标识；无记录器时为空
func SnapshotLogicalTurnID(recorder *AttemptRecorder) string {
	if recorder == nil {
		return ""
	}
	return recorder.LogicalTurnIDValue()
}
