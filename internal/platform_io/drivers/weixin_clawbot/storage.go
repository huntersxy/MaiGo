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
	AccountID    string            `json:"account_id"`
	Token        string            `json:"token"`
	Remark       string            `json:"remark"`
	UserID       string            `json:"user_id"`
	Cursor       string            `json:"cursor"`
	BaseURL      string            `json:"base_url"`
	Contexts     map[string]string `json:"contexts"`
}

// Store 账号文件：slot0=account.json；slotN=account-N.json
type Store struct {
	mu    sync.Mutex
	path  string
	state AccountState
}

func NewStore(path string) *Store {
	return &Store{
		path: path,
		state: AccountState{
			Contexts: map[string]string{},
		},
	}
}

func AccountFileName(slot int) string {
	if slot <= 0 {
		return "account.json"
	}
	return fmt.Sprintf("account-%d.json", slot)
}

func (s *Store) State() AccountState {
	if s == nil {
		return AccountState{Contexts: map[string]string{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := s.state
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
			s.state = AccountState{Contexts: map[string]string{}}
			return nil
		}
		return err
	}
	var state AccountState
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) Update(fn func(state *AccountState)) error {
	if s == nil {
		return fmt.Errorf("store 为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Contexts == nil {
		s.state.Contexts = map[string]string{}
	}
	fn(&s.state)
	return nil
}

func (s *Store) Clear() error {
	if s == nil {
		return fmt.Errorf("store 为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keepAccount := s.state.AccountID
	keepRemark := s.state.Remark
	keepUser := s.state.UserID
	s.state = AccountState{
		AccountID: keepAccount,
		Remark:    keepRemark,
		UserID:    keepUser,
		Contexts:  map[string]string{},
	}
	_ = os.Remove(s.path)
	return nil
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
