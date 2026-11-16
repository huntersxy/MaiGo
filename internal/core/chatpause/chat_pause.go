// Package chatpause 维护本次运行内的聊天回复暂停状态，独立于回复频率配置，重启后解除
package chatpause

import "sync"

// State 类型规则优先于默认值，单会话命令可覆盖类型规则
type State struct {
	mu           sync.RWMutex
	types        map[bool]struct{}
	sessions     map[string]bool
	sessionTypes map[string]bool
}

// NewState 创建暂停状态
func NewState() *State {
	return &State{
		types:        map[bool]struct{}{},
		sessions:     map[string]bool{},
		sessionTypes: map[string]bool{},
	}
}

var defaultState = NewState()

// Default 返回进程内共享的暂停状态
func Default() *State { return defaultState }

// IsPaused 判断会话是否暂停；单会话规则优先
func (s *State) IsPaused(sessionID string, isGroup bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if paused, ok := s.sessions[sessionID]; ok {
		return paused
	}
	_, paused := s.types[isGroup]
	return paused
}

// SetType 批量设置某类聊天；同时清除该类型的单会话覆盖
func (s *State) SetType(isGroup bool, paused bool) {
	s.mu.Lock()
	if paused {
		s.types[isGroup] = struct{}{}
	} else {
		delete(s.types, isGroup)
	}
	for sessionID, sessionType := range s.sessionTypes {
		if sessionType == isGroup {
			delete(s.sessions, sessionID)
			delete(s.sessionTypes, sessionID)
		}
	}
	s.mu.Unlock()
}

// SetSession 设置单个会话的暂停状态
func (s *State) SetSession(sessionID string, isGroup bool, paused bool) {
	s.mu.Lock()
	s.sessions[sessionID] = paused
	s.sessionTypes[sessionID] = isGroup
	s.mu.Unlock()
}
