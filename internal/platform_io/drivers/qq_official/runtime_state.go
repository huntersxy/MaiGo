package qq_official

import "sync"

// RuntimeState 网关就绪与账号快照
type RuntimeState struct {
	mu               sync.RWMutex
	ready            bool
	connected        bool
	accountID        string // 官方 bot id
	unifiedAccountID  string
	botName          string
	inboundReady     bool
}

func NewRuntimeState() *RuntimeState {
	return &RuntimeState{}
}

func (s *RuntimeState) MarkReady(accountID, botName, unified string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = true
	s.connected = true
	s.inboundReady = true
	s.accountID = accountID
	s.botName = botName
	s.unifiedAccountID = unified
}

func (s *RuntimeState) MarkNotReady() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = false
	s.connected = false
	s.inboundReady = false
}

func (s *RuntimeState) SetInboundReady(ready bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inboundReady = ready
}

func (s *RuntimeState) SnapshotForReport() (ready bool, routeAccountID, botName string, connected bool) {
	if s == nil {
		return false, "", "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	route := s.unifiedAccountID
	if route == "" {
		route = s.accountID
	}
	return s.ready, route, s.botName, s.connected
}

func (s *RuntimeState) AccountID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accountID
}

func (s *RuntimeState) RouteAccountID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.unifiedAccountID != "" {
		return s.unifiedAccountID
	}
	return s.accountID
}

func (s *RuntimeState) BotName() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.botName
}

func (s *RuntimeState) Ready() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

func (s *RuntimeState) InboundReady() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inboundReady && s.ready
}
