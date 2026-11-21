package weixin_clawbot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AccountState 本地账号态（不含密钥日志）
type AccountState struct {
	AccountID string            `json:"account_id"`
	Token     string            `json:"token"`
	Remark    string            `json:"remark"`
	UserID    string            `json:"user_id"`
	Cursor    string            `json:"cursor"`
	BaseURL   string            `json:"base_url"`
	Contexts  map[string]string `json:"contexts"`
	Seen      []string          `json:"seen"`
}

// Store 账号文件：slot0=account.json；slotN=account-N.json
type Store struct {
	mu    sync.Mutex
	path  string
	state AccountState
}

func NewStore(path string) *Store {
	return &Store{
		path:  path,
		state: emptyState(),
	}
}

func emptyState() AccountState {
	return AccountState{BaseURL: DefaultBaseURL, Contexts: map[string]string{}, Seen: []string{}}
}

func AccountFileName(slot int) string {
	if slot <= 0 {
		return "account.json"
	}
	return fmt.Sprintf("account-%d.json", slot)
}

func (s *Store) State() AccountState {
	if s == nil {
		return emptyState()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

func cloneState(state AccountState) AccountState {
	clone := state
	clone.Seen = append([]string{}, state.Seen...)
	if clone.Contexts == nil {
		clone.Contexts = map[string]string{}
	} else {
		m := make(map[string]string, len(clone.Contexts))
		for k, v := range clone.Contexts {
			m[k] = v
		}
		clone.Contexts = m
	}
	return clone
}

func (s *Store) Load() error {
	if s == nil {
		return fmt.Errorf("store 为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.state = emptyState()
			return nil
		}
		return err
	}
	state := emptyState()
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	if state.Contexts == nil {
		state.Contexts = map[string]string{}
	}
	s.state = state
	return nil
}

func (s *Store) Save() error {
	if s == nil {
		return fmt.Errorf("store 为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(s.state)
}

func (s *Store) saveLocked(state AccountState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) updateAndSave(update func(*AccountState)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneState(s.state)
	update(&next)
	if err := s.saveLocked(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) Clear() error {
	return s.updateAndSave(func(state *AccountState) { *state = emptyState() })
}

func (s *Store) invalidate() error {
	return s.updateAndSave(func(state *AccountState) {
		previous := *state
		*state = emptyState()
		state.AccountID, state.Remark, state.UserID = previous.AccountID, previous.Remark, previous.UserID
	})
}

func (s *Store) SetRemark(remark string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Remark = remark
}

func (s *Store) HasIdentity() bool {
	state := s.State()
	return state.AccountID != "" || state.Token != "" || state.Remark != ""
}
