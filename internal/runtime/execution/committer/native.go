package committer

import (
	"context"
	"sync"

	"MaiGo/internal/runtime/execution/policy"
)

type scope struct {
	kind     string
	targetID int64
}

type lane struct {
	pending []<-chan func()
}

type Native struct {
	mu     sync.Mutex
	lanes  map[scope]*lane
	closed bool
	done   chan struct{}
}

func NewNative() *Native {
	return &Native{lanes: make(map[scope]*lane), done: make(chan struct{})}
}

func (native *Native) Reserve(kind string, targetID int64) (func(func()), error) {
	key := scope{kind: kind, targetID: targetID}
	ready := make(chan func(), 1)
	native.mu.Lock()
	if native.closed {
		native.mu.Unlock()
		return nil, policy.ErrClosed
	}
	current := native.lanes[key]
	if current == nil {
		current = &lane{}
		native.lanes[key] = current
		go native.runLane(key, current)
	}
	current.pending = append(current.pending, ready)
	native.mu.Unlock()
	var once sync.Once
	return func(run func()) {
		once.Do(func() { ready <- run })
	}, nil
}

func (native *Native) runLane(key scope, current *lane) {
	for {
		native.mu.Lock()
		if len(current.pending) == 0 {
			delete(native.lanes, key)
			native.checkStopped()
			native.mu.Unlock()
			return
		}
		ready := current.pending[0]
		current.pending[0] = nil
		current.pending = current.pending[1:]
		native.mu.Unlock()
		if run := <-ready; run != nil {
			runSafely(run)
		}
	}
}

func (native *Native) Shutdown(ctx context.Context) error {
	native.mu.Lock()
	if !native.closed {
		native.closed = true
		native.checkStopped()
	}
	native.mu.Unlock()
	select {
	case <-native.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (native *Native) checkStopped() {
	if native.closed && len(native.lanes) == 0 {
		close(native.done)
	}
}
