package telegram

import "sync"

// RuntimeState 网关就绪与账号快照
type RuntimeState struct {
	mu        sync.RWMutex
	ready     bool
	accountID string
	username  string
	connected bool
}

func NewRuntimeState() *RuntimeState {
	return &RuntimeState{}
}

func (s *RuntimeState) MarkReady(accountID, username string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	s.connected = true
	s.accountID = accountID
	s.username = username
}

func (s *RuntimeState) MarkNotReady() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = false
	s.connected = false
}

func (s *RuntimeState) SnapshotForReport() (ready bool, accountID, username string, connected bool) {
	if s == nil {
		return false, "", "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready, s.accountID, s.username, s.connected
}

func (s *RuntimeState) AccountID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accountID
}

func (s *RuntimeState) Username() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.username
}

func (s *RuntimeState) Ready() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}
