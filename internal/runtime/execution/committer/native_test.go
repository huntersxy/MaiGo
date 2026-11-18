package committer

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeOrdersWithinSession(t *testing.T) {
	native := NewNative()
	defer native.Shutdown()

	var mu sync.Mutex
	var order []uint64
	done := make(chan struct{}, 3)
	record := func(seq uint64) func() {
		return func() {
			mu.Lock()
			order = append(order, seq)
			mu.Unlock()
			done <- struct{}{}
		}
	}

	native.Submit("group", 1, 3, record(3))
	native.Submit("group", 1, 2, record(2))
	native.Submit("group", 1, 1, record(1))
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("等待提交超时")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if want := []uint64{1, 2, 3}; !reflect.DeepEqual(order, want) {
		t.Fatalf("提交顺序 = %v, 期望 %v", order, want)
	}
}

func TestNativeRunsSessionsInParallel(t *testing.T) {
	native := NewNative()
	defer native.Shutdown()

	blocked := make(chan struct{})
	release := make(chan struct{})
	other := make(chan struct{})

	native.Submit("group", 1, 1, func() {
		close(blocked)
		<-release
	})
	<-blocked
	native.Submit("private", 2, 1, func() { close(other) })

	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("不同会话被串行阻塞")
	}
	close(release)
}

func TestNativeSkipConsumesSequence(t *testing.T) {
	native := NewNative()
	defer native.Shutdown()

	ran := make(chan struct{})
	native.Submit("group", 1, 2, func() { close(ran) })
	native.Skip("group", 1, 1)

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("跳过缺口后事件未提交")
	}
}

func TestNativeShutdownDrainsPending(t *testing.T) {
	native := NewNative()

	var runs atomic.Int32
	done := make(chan struct{})
	native.Submit("group", 1, 3, func() {
		runs.Add(1)
		close(done)
	})
	native.Skip("group", 1, 5)

	native.Shutdown()

	if got := runs.Load(); got != 1 {
		t.Fatalf("关停排空执行次数 = %d, 期望 1", got)
	}
	select {
	case <-done:
	default:
		t.Fatal("关停未等待提交完成")
	}
}

func TestNativeInlineSequenceZero(t *testing.T) {
	native := NewNative()
	defer native.Shutdown()

	ran := false
	native.Submit("group", 1, 0, func() { ran = true })
	if !ran {
		t.Fatal("seq 0 应同步执行")
	}
}
