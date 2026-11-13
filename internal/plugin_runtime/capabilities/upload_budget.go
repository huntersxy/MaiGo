package capabilities

// 上传即时配额，对齐 plugin_runtime/upload_store.py 的 UploadBudget
//
// 与暂存条数上限（MaxPendingUploads*）不同：这里限制的是「同时正在处理」的上传数，
// 只在锁内更新计数、不排队；超限时返回 UploadCapacityError 让调用方回 429。

import "sync"

// UploadBudget 跨调用共享的即时配额
type UploadBudget struct {
	mu     sync.Mutex
	active map[string]int
}

// NewUploadBudget 创建上传即时配额
func NewUploadBudget() *UploadBudget {
	return &UploadBudget{active: map[string]int{}}
}

// Acquire 为 owner 预留一个处理名额；返回 release 供 defer 调用
//
// 单 owner 上限 MaxUploadsPerPlugin、全局上限 MaxUploadsTotal；超限返回
// UploadCapacityError("上传处理中，请稍后再试")。
func (b *UploadBudget) Acquire(owner string) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	b.mu.Lock()
	if b.active == nil {
		b.active = map[string]int{}
	}
	count := b.active[owner]
	total := 0
	for _, value := range b.active {
		total += value
	}
	if count >= MaxUploadsPerPlugin || total >= MaxUploadsTotal {
		b.mu.Unlock()
		return nil, &UploadCapacityError{Message: "上传处理中，请稍后再试"}
	}
	b.active[owner] = count + 1
	b.mu.Unlock()
	released := false
	return func() {
		if released {
			return
		}
		released = true
		b.mu.Lock()
		remaining := b.active[owner] - 1
		if remaining > 0 {
			b.active[owner] = remaining
		} else {
			delete(b.active, owner)
		}
		b.mu.Unlock()
	}, nil
}

// Active 返回 owner 当前占用的名额，供状态展示
func (b *UploadBudget) Active(owner string) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active[owner]
}

var defaultUploadBudget = NewUploadBudget()

// DefaultUploadBudget 返回进程级上传即时配额
func DefaultUploadBudget() *UploadBudget { return defaultUploadBudget }
