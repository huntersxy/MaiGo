package contract

import (
	"context"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// AttemptRecorder 同一逻辑请求内的尝试与快照路径。
//
// 由 attemptRunner/failover 放入 context，客户端在失败时通过它拿到
// 「本次请求已写到哪个快照文件」，从而把后续尝试追加到同一份快照而不是
// 每试一次就落一个新文件。
//
// 对齐 MaiBot src/llm_models/model_client/base_client.py 的
// RequestSnapshot.request_id / attempt 累积语义：Python 靠对象引用共享，
// Go 用 context 传指针。放在本包是因为「客户端层」与「快照层」都要用它，
// 而快照层又反向依赖客户端的请求构造逻辑——留在任一侧都会循环。
type AttemptRecorder struct {
	mu            sync.Mutex
	RequestID     string
	LogicalTurnID string
	snapshotPath  string
	attempts      []map[string]any
}

type attemptContextKey struct{}

// NewAttemptRecorder 创建尝试记录器，生成请求级 UUID
func NewAttemptRecorder() *AttemptRecorder {
	return &AttemptRecorder{RequestID: uuid.NewString()}
}

// AttemptRecorderFromContext 从上下文取尝试记录器，无则返回 nil
func AttemptRecorderFromContext(ctx context.Context) *AttemptRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(attemptContextKey{}).(*AttemptRecorder)
	return recorder
}

// EnsureAttemptRecorder 保证上下文里有尝试记录器；已有则复用
//
// 复用而非新建是必需的：同一逻辑请求的重试必须共享 RequestID 与快照路径，
// 否则每次重试都会落一个新快照，排障时看不到完整尝试序列。
func EnsureAttemptRecorder(ctx context.Context) (context.Context, *AttemptRecorder) {
	if ctx == nil {
		ctx = context.Background()
	}
	if recorder := AttemptRecorderFromContext(ctx); recorder != nil {
		return ctx, recorder
	}
	recorder := NewAttemptRecorder()
	return context.WithValue(ctx, attemptContextKey{}, recorder), recorder
}

// Append 追加一次尝试 payload
func (r *AttemptRecorder) Append(attempt map[string]any) {
	if r == nil || attempt == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
}

// AttemptsSnapshot 返回尝试列表的副本
func (r *AttemptRecorder) AttemptsSnapshot() []map[string]any {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]map[string]any, len(r.attempts))
	copy(out, r.attempts)
	return out
}

// SetLogicalTurnID 设置逻辑轮次标识；已有值时不覆盖
//
// 不覆盖是刻意的：一轮对话可能跨多次模型调用，首个写入者才是权威来源，
// 后续调用若覆盖会让快照里的轮次归类错乱。
func (r *AttemptRecorder) SetLogicalTurnID(id string) {
	if r == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.LogicalTurnID == "" {
		r.LogicalTurnID = id
	}
}

// LogicalTurnID 返回本轮固定标识
func (r *AttemptRecorder) LogicalTurnIDValue() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.LogicalTurnID
}

// SnapshotPath 返回已写入的快照文件路径，未写过时为空
func (r *AttemptRecorder) SnapshotPath() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshotPath
}

// SetSnapshotPath 记录快照文件路径；已有值时不覆盖
//
// 同 LogicalTurnID：首个成功落盘的位置才是权威路径，后续尝试只追加事件。
func (r *AttemptRecorder) SetSnapshotPath(path string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshotPath == "" {
		r.snapshotPath = path
	}
}
