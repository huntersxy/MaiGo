package services

// 聊天数据迁移的主循环边界；只等待指定聊天流，在途消息不会丢弃。
// 对齐 services/chat_migration_gate.py。

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ChatMigrationGate 暂停指定聊天流的新活动并等待在途操作结束
type ChatMigrationGate struct {
	mu      sync.Mutex
	blocked map[string]struct{}
	active  map[string]int
	changed chan struct{}
}

// NewChatMigrationGate 构造迁移门
func NewChatMigrationGate() *ChatMigrationGate {
	return &ChatMigrationGate{
		blocked: map[string]struct{}{},
		active:  map[string]int{},
		changed: make(chan struct{}),
	}
}

func (g *ChatMigrationGate) notifyLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

// Activity 在聊天流未暂停时执行操作；暂停期间阻塞等待
func (g *ChatMigrationGate) Activity(ctx context.Context, sessionID string, operation func() error) error {
	sessionID = trimMigrationID(sessionID)
	if sessionID == "" || g == nil {
		return operation()
	}
	for {
		g.mu.Lock()
		if _, blocked := g.blocked[sessionID]; !blocked {
			g.active[sessionID]++
			g.mu.Unlock()
			defer func() {
				g.mu.Lock()
				g.active[sessionID]--
				if g.active[sessionID] <= 0 {
					delete(g.active, sessionID)
				}
				g.notifyLocked()
				g.mu.Unlock()
			}()
			return operation()
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// Pause 暂停聊天流并等待在途操作结束
func (g *ChatMigrationGate) Pause(ctx context.Context, sessionIDs []string) error {
	if g == nil {
		return nil
	}
	ids := make(map[string]struct{}, len(sessionIDs))
	for _, id := range sessionIDs {
		if id = trimMigrationID(id); id != "" {
			ids[id] = struct{}{}
		}
	}
	g.mu.Lock()
	for id := range ids {
		if _, blocked := g.blocked[id]; blocked {
			g.mu.Unlock()
			return errors.New("聊天流已有迁移任务，请先完成或恢复该任务")
		}
	}
	for id := range ids {
		g.blocked[id] = struct{}{}
	}
	for {
		busy := false
		for id := range ids {
			if g.active[id] > 0 {
				busy = true
				break
			}
		}
		if !busy {
			g.mu.Unlock()
			return nil
		}
		changed := g.changed
		g.mu.Unlock()
		timer := time.NewTimer(60 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			g.Resume(sessionIDs)
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
			g.Resume(sessionIDs)
			return errors.New("等待聊天流在途操作超时")
		}
		g.mu.Lock()
	}
}

// Resume 恢复聊天流
func (g *ChatMigrationGate) Resume(sessionIDs []string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range sessionIDs {
		delete(g.blocked, trimMigrationID(id))
	}
	g.notifyLocked()
}

// Blocked 判断聊天流是否处于迁移暂停
func (g *ChatMigrationGate) Blocked(sessionID string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, blocked := g.blocked[trimMigrationID(sessionID)]
	return blocked
}

// Block 直接标记聊天流暂停，用于启动恢复未完成任务
func (g *ChatMigrationGate) Block(sessionIDs []string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range sessionIDs {
		if id = trimMigrationID(id); id != "" {
			g.blocked[id] = struct{}{}
		}
	}
}

func trimMigrationID(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t') {
		value = value[1:]
	}
	for len(value) > 0 && (value[len(value)-1] == ' ' || value[len(value)-1] == '\t') {
		value = value[:len(value)-1]
	}
	return value
}
