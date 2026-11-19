package snowluma

import (
	"sync"
)

// RuntimeState 网关就绪与路由开关；断连时 routing 复位为 true
type RuntimeState struct {
	mu                 sync.Mutex
	connected          bool
	accountID          string
	scope              string
	detectedProtocol   string
	routingEnabled     bool
	reportedRouting    *bool
	reportedProtocol   string
	reportedAccountID  string
	reportedScope      string
}

func NewRuntimeState() *RuntimeState {
	return &RuntimeState{routingEnabled: true}
}

func (s *RuntimeState) AccountID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return ""
	}
	return s.accountID
}

func (s *RuntimeState) Scope() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected {
		return ""
	}
	return s.scope
}

func (s *RuntimeState) RoutingEnabled() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routingEnabled
}

func (s *RuntimeState) DetectedProtocol() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detectedProtocol
}

func (s *RuntimeState) SetDetectedProtocol(protocol string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detectedProtocol = protocol
}

// MarkConnected 记录本地连接就绪信息（Host 上报由 Adapter 负责）
func (s *RuntimeState) MarkConnected(accountID, scope, protocol string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.accountID = accountID
	s.scope = scope
	if protocol != "" {
		s.detectedProtocol = protocol
	}
	s.reportedAccountID = accountID
	s.reportedScope = scope
	s.reportedProtocol = s.detectedProtocol
	routing := s.routingEnabled
	s.reportedRouting = &routing
}

// SetRoutingEnabled 只切 Host 聊天路由；不断 WS
func (s *RuntimeState) SetRoutingEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routingEnabled = enabled
	s.reportedRouting = nil
}

// MarkDisconnected 断连：routing 复位 true，清账号
func (s *RuntimeState) MarkDisconnected() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routingEnabled = true
	trueVal := true
	s.reportedRouting = &trueVal
	s.connected = false
	s.accountID = ""
	s.scope = ""
	s.reportedAccountID = ""
	s.reportedScope = ""
}

func (s *RuntimeState) Connected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// SnapshotForReport 返回上报 Host 所需字段
func (s *RuntimeState) SnapshotForReport() (ready bool, accountID, scope, protocol string, connected bool) {
	if s == nil {
		return false, "", "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routingEnabled, s.accountID, s.scope, s.detectedProtocol, s.connected
}
