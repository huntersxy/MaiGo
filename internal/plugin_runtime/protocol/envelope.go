// Package protocol 定义 Host ↔ Runner RPC 信封与载荷，对齐 src/plugin_runtime/protocol/
package protocol

import (
	"sync/atomic"
	"time"
)

const (
	ProtocolVersion = "1.0.0"
	MinSDKVersion   = "1.0.0"
	MaxSDKVersion   = "2.99.99"
)

// MessageType RPC 消息类型，对齐 protocol.envelope.MessageType
type MessageType string

const (
	MessageTypeRequest   MessageType = "request"
	MessageTypeResponse  MessageType = "response"
	MessageTypeBroadcast MessageType = "broadcast"
)

// ConfigReloadScope 配置热重载范围
type ConfigReloadScope string

const (
	ConfigReloadScopeSelf  ConfigReloadScope = "self"
	ConfigReloadScopeBot   ConfigReloadScope = "bot"
	ConfigReloadScopeModel ConfigReloadScope = "model"
)

// Envelope RPC 统一消息封装；序列化：Envelope → map → MsgPack
type Envelope struct {
	ProtocolVersion string         `msgpack:"protocol_version" json:"protocol_version"`
	RequestID       int64          `msgpack:"request_id" json:"request_id"`
	MessageType     MessageType    `msgpack:"message_type" json:"message_type"`
	Method          string         `msgpack:"method" json:"method"`
	PluginID        string         `msgpack:"plugin_id" json:"plugin_id"`
	TimestampMS     int64          `msgpack:"timestamp_ms" json:"timestamp_ms"`
	TimeoutMS       int64          `msgpack:"timeout_ms" json:"timeout_ms"`
	Payload         map[string]any `msgpack:"payload" json:"payload"`
	Error           map[string]any `msgpack:"error,omitempty" json:"error,omitempty"`
}

// NewRequestEnvelope 构造出站请求信封
func NewRequestEnvelope(requestID int64, method, pluginID string, payload map[string]any) Envelope {
	if payload == nil {
		payload = map[string]any{}
	}
	return Envelope{
		ProtocolVersion: ProtocolVersion,
		RequestID:       requestID,
		MessageType:     MessageTypeRequest,
		Method:          method,
		PluginID:        pluginID,
		TimestampMS:     time.Now().UnixMilli(),
		TimeoutMS:       30000,
		Payload:         payload,
	}
}

func (e Envelope) IsRequest() bool   { return e.MessageType == MessageTypeRequest }
func (e Envelope) IsResponse() bool  { return e.MessageType == MessageTypeResponse }
func (e Envelope) IsBroadcast() bool { return e.MessageType == MessageTypeBroadcast }

// MakeResponse 基于当前请求创建响应信封
func (e Envelope) MakeResponse(payload map[string]any, err map[string]any) Envelope {
	if payload == nil {
		payload = map[string]any{}
	}
	return Envelope{
		ProtocolVersion: e.ProtocolVersion,
		RequestID:       e.RequestID,
		MessageType:     MessageTypeResponse,
		Method:          e.Method,
		PluginID:        e.PluginID,
		TimestampMS:     time.Now().UnixMilli(),
		TimeoutMS:       e.TimeoutMS,
		Payload:         payload,
		Error:           err,
	}
}

// MakeErrorResponse 基于当前请求创建错误响应
func (e Envelope) MakeErrorResponse(code, message string, details map[string]any) Envelope {
	if details == nil {
		details = map[string]any{}
	}
	return e.MakeResponse(nil, map[string]any{
		"code":    code,
		"message": message,
		"details": details,
	})
}

// RequestIDGenerator 单调递增 int64 请求 ID；并发安全且零值可用
type RequestIDGenerator struct {
	next atomic.Int64
}

func NewRequestIDGenerator(start int64) *RequestIDGenerator {
	if start <= 0 {
		start = 1
	}
	g := &RequestIDGenerator{}
	g.next.Store(start)
	return g
}

// Next 返回下一个请求 ID；零值生成器从 1 开始，保持单调递增与唯一
func (g *RequestIDGenerator) Next() int64 {
	for {
		current := g.next.Load()
		if current <= 0 {
			// 零值生成器从 1 开始，成功占用 0 时直接推进到 2
			if g.next.CompareAndSwap(0, 2) {
				return 1
			}
			continue
		}
		if g.next.CompareAndSwap(current, current+1) {
			return current
		}
	}
}
