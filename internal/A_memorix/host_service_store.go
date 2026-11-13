package memorix

// A_memorix 宿主配置存储的 maibot.InstanceHolder 适配器
// 把 TOML 实例快照与热重载回调接到 HostConfigStore，避免宿主服务直接持有全局单例

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"

	"maibot-go/internal/config/maibot"

	"github.com/pelletier/go-toml/v2"
)

var _ HostConfigStore = (*HolderHostConfigStore)(nil)

// HolderHostConfigStore 基于 maibot.InstanceHolder 的配置存储
// Raw 每次从 bot_config.toml 现读，保证与磁盘中 [a_memorix] 原表一致
type HolderHostConfigStore struct {
	holder *maibot.InstanceHolder

	mu          sync.Mutex
	callbacks   []func(scopes []string)
	lastRaw     map[string]any
	lastRawSeen bool
}

// NewHolderHostConfigStore 构造适配器
func NewHolderHostConfigStore(holder *maibot.InstanceHolder) *HolderHostConfigStore {
	store := &HolderHostConfigStore{holder: holder}
	store.refreshRawBaseline()
	if holder != nil {
		holder.RegisterReloadCallbackForSections(func(_ *maibot.InstanceConfig, scopes []maibot.ReloadScope) {
			if !store.consumeAMemorixChange() {
				return
			}
			converted := make([]string, 0, len(scopes))
			for _, scope := range scopes {
				converted = append(converted, string(scope))
			}
			store.dispatch(converted)
		}, "a_memorix")
	}
	return store
}

// refreshRawBaseline 记录当前 [a_memorix] 原表，供热重载时判断本节是否变化
// 对齐 Python config_manager.register_reload_callback(..., sections=("a_memorix",))
func (s *HolderHostConfigStore) refreshRawBaseline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.readRawLocked()
	if !ok {
		return
	}
	s.lastRaw = raw
	s.lastRawSeen = true
}

// consumeAMemorixChange 比较重载后 [a_memorix] 是否变化；读失败时保守放行
func (s *HolderHostConfigStore) consumeAMemorixChange() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.readRawLocked()
	if !ok {
		return true
	}
	if s.lastRawSeen && reflect.DeepEqual(s.lastRaw, raw) {
		return false
	}
	s.lastRaw = raw
	s.lastRawSeen = true
	return true
}

func (s *HolderHostConfigStore) readRawLocked() (map[string]any, bool) {
	if s == nil || s.holder == nil {
		return nil, false
	}
	config := s.holder.Config()
	if config == nil || config.BotConfigPath == "" {
		return nil, false
	}
	data, err := os.ReadFile(config.BotConfigPath)
	if err != nil {
		return nil, false
	}
	document := map[string]any{}
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, false
	}
	return anyToMap(document["a_memorix"]), true
}

// BotConfigPath 返回当前实例的 bot_config.toml
func (s *HolderHostConfigStore) BotConfigPath() string {
	if s == nil || s.holder == nil {
		return ""
	}
	config := s.holder.Config()
	if config == nil {
		return ""
	}
	return config.BotConfigPath
}

// AMemorixSnapshot 返回解析后的配置模型与 [a_memorix] 原表
func (s *HolderHostConfigStore) AMemorixSnapshot() (HostConfigSnapshot, error) {
	if s == nil || s.holder == nil {
		return HostConfigSnapshot{}, errors.New("A_Memorix 配置 Holder 未注入")
	}
	config := s.holder.Config()
	if config == nil {
		return HostConfigSnapshot{}, errors.New("A_Memorix 实例配置尚未加载")
	}
	path := config.BotConfigPath
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return HostConfigSnapshot{Model: config.Bot.AMemorix}, nil
		}
		return HostConfigSnapshot{}, err
	}
	document := map[string]any{}
	if err := toml.Unmarshal(data, &document); err != nil {
		return HostConfigSnapshot{}, err
	}
	raw := anyToMap(document["a_memorix"])
	return HostConfigSnapshot{Model: config.Bot.AMemorix, Raw: raw}, nil
}

// Reload 触发 bot 配置重载；holder 会同步分发已注册回调
func (s *HolderHostConfigStore) Reload(context.Context) error {
	if s == nil || s.holder == nil {
		return errors.New("A_Memorix 配置 Holder 未注入")
	}
	return s.holder.ReloadFromDisk([]maibot.ReloadScope{maibot.ReloadScopeBot})
}

// RegisterReloadCallback 注册宿主服务回调
func (s *HolderHostConfigStore) RegisterReloadCallback(callback func(scopes []string)) {
	if s == nil || callback == nil {
		return
	}
	s.mu.Lock()
	s.callbacks = append(s.callbacks, callback)
	s.mu.Unlock()
}

func (s *HolderHostConfigStore) dispatch(scopes []string) {
	s.mu.Lock()
	callbacks := append([]func([]string){}, s.callbacks...)
	s.mu.Unlock()
	for _, callback := range callbacks {
		callback(scopes)
	}
}
