package modelprofiles

import (
	"context"
	"fmt"
	"strings"
)

// StaticStore 单档案内存存储，供 Maisaka CLI 等场景绕过持久化 Store
type StaticStore struct {
	active   Profile
	plainKey string
}

// NewStaticStore 构造仅含一个 active 档案的 Source；apiKey 为明文
func NewStaticStore(name, provider, baseURL, modelName, apiKey string) (*StaticStore, error) {
	name = strings.TrimSpace(name)
	provider = strings.TrimSpace(provider)
	baseURL = strings.TrimSpace(baseURL)
	modelName = strings.TrimSpace(modelName)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("API Key 为空")
	}
	row := Profile{
		Name:          name,
		Provider:      provider,
		BaseURL:       baseURL,
		ModelName:     modelName,
		ContextWindow: 128000,
		Active:        true,
	}
	if err := validate(row, apiKey, false); err != nil {
		return nil, err
	}
	return &StaticStore{active: row, plainKey: apiKey}, nil
}

func (s *StaticStore) Active(context.Context) (Profile, error) {
	if s == nil {
		return Profile{}, fmt.Errorf("静态模型存储未初始化")
	}
	return s.active, nil
}

func (s *StaticStore) List(context.Context) ([]Profile, error) {
	if s == nil {
		return nil, fmt.Errorf("静态模型存储未初始化")
	}
	return []Profile{s.active}, nil
}

func (s *StaticStore) DecryptKey(row Profile) (string, error) {
	if s == nil {
		return "", fmt.Errorf("静态模型存储未初始化")
	}
	if row.Name != s.active.Name {
		return "", ErrNotFound
	}
	return s.plainKey, nil
}

var _ Source = (*StaticStore)(nil)
