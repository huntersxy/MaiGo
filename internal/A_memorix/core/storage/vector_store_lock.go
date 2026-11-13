package storage

// 向量存储使用的可重入互斥锁
// 对齐 Python 的 threading.RLock：cleanup checkpoint 期间持锁，
// 同一调用方仍可继续调用 add/delete/save 等方法，其它调用方阻塞

import (
	"runtime"
	"sync"
)

// reentrantMutex 是可重入互斥锁，按 goroutine 识别持有者
type reentrantMutex struct {
	mutex sync.Mutex
	cond  *sync.Cond
	owner uint64
	depth int
}

// newReentrantMutex 创建可重入互斥锁
func newReentrantMutex() *reentrantMutex {
	lock := &reentrantMutex{}
	lock.cond = sync.NewCond(&lock.mutex)
	return lock
}

// Lock 获取锁，持有者重复获取只增加重入计数
// 注意：参数必须是 *reentrantMutex，否则会复制内部的 Cond
func (m *reentrantMutex) Lock() {
	goroutine := goroutineID()
	m.mutex.Lock()
	for m.owner != 0 && m.owner != goroutine {
		m.cond.Wait()
	}
	m.owner = goroutine
	m.depth++
	m.mutex.Unlock()
}

// Unlock 释放一次重入计数，计数归零时唤醒等待者
func (m *reentrantMutex) Unlock() {
	goroutine := goroutineID()
	m.mutex.Lock()
	if m.owner != goroutine {
		m.mutex.Unlock()
		panic("解锁未持有的向量存储锁")
	}
	m.depth--
	if m.depth == 0 {
		m.owner = 0
		m.cond.Signal()
	}
	m.mutex.Unlock()
}

// heldByCurrentGoroutine 判断当前 goroutine 是否持有该锁
func (m *reentrantMutex) heldByCurrentGoroutine() bool {
	goroutine := goroutineID()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.owner == goroutine
}

// goroutineID 从栈信息前缀解析当前 goroutine 编号
// 只用于识别可重入锁的持有者，不参与业务逻辑
func goroutineID() uint64 {
	var buffer [64]byte
	size := runtime.Stack(buffer[:], false)
	const prefix = "goroutine "
	if size <= len(prefix) {
		return 0
	}
	value := uint64(0)
	for _, char := range buffer[len(prefix):size] {
		if char < '0' || char > '9' {
			break
		}
		value = value*10 + uint64(char-'0')
	}
	return value
}

// vectorStoreLock 构造 VectorStore 使用的可重入锁
func vectorStoreLock() *reentrantMutex { return newReentrantMutex() }
