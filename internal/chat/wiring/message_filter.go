package chatwiring

import (
	"fmt"
	"sync"

	"go.uber.org/zap"

	"MaiGo/internal/chat/message_receive"
	maibotcfg "MaiGo/internal/config/maibot"
)

// ConfigMessageFilter 从当前消息接收配置构造屏蔽词/正则过滤器
// 每次匹配前检查配置是否变化，热重载后立即生效；非法正则保留旧配置并记录真实错误
type ConfigMessageFilter struct {
	lookup func() *maibotcfg.MessageReceiveConfig

	mu       sync.Mutex
	words    []string
	patterns []string
	filter   *messagereceive.BanMessageFilter
}

// NewConfigMessageFilter 用当前配置初始化过滤器；非法正则在启动期返回错误
func NewConfigMessageFilter(lookup func() *maibotcfg.MessageReceiveConfig) (*ConfigMessageFilter, error) {
	filter := &ConfigMessageFilter{lookup: lookup}
	if err := filter.reload(); err != nil {
		return nil, err
	}
	return filter, nil
}

// FirstBanWord 返回第一个命中的屏蔽词
func (f *ConfigMessageFilter) FirstBanWord(text string) string {
	filter := f.current()
	if filter == nil {
		return ""
	}
	return filter.FirstBanWord(text)
}

// FirstBanRegex 返回第一个命中的屏蔽正则原文
func (f *ConfigMessageFilter) FirstBanRegex(text string) string {
	filter := f.current()
	if filter == nil {
		return ""
	}
	return filter.FirstBanRegex(text)
}

func (f *ConfigMessageFilter) current() *messagereceive.BanMessageFilter {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.reload(); err != nil {
		zap.L().Error("屏蔽词/正则配置热重载失败，沿用旧配置", zap.Error(err))
	}
	return f.filter
}

// reload 按配置重编译；配置不变时跳过，非法配置返回真实错误
func (f *ConfigMessageFilter) reload() error {
	var cfg *maibotcfg.MessageReceiveConfig
	if f.lookup != nil {
		cfg = f.lookup()
	}
	words, patterns, err := messageReceiveFilterLists(cfg)
	if err != nil {
		return err
	}
	if sameStrings(f.words, words) && sameStrings(f.patterns, patterns) {
		return nil
	}
	filter, err := messagereceive.NewBanMessageFilter(words, patterns)
	if err != nil {
		return err
	}
	f.words = words
	f.patterns = patterns
	f.filter = filter
	return nil
}

func messageReceiveFilterLists(cfg *maibotcfg.MessageReceiveConfig) ([]string, []string, error) {
	if cfg == nil {
		return nil, nil, nil
	}
	words, err := stringListConfig(cfg.BanWords, "ban_words")
	if err != nil {
		return nil, nil, err
	}
	patterns, err := stringListConfig(cfg.BanMsgsRegex, "ban_msgs_regex")
	if err != nil {
		return nil, nil, err
	}
	return words, patterns, nil
}

func stringListConfig(value any, field string) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s 配置必须是字符串数组", field)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s 配置必须是字符串数组", field)
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
