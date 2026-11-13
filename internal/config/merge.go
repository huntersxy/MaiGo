package config

import (
	"fmt"
)

// MergeInMemorySnapshot 在现有配置快照上合并字段，不写 app_config
func MergeInMemorySnapshot(merge func(*RuntimeConfig)) error {
	current := Snapshot()
	if current == nil {
		defaults := DefaultRuntime()
		current = &defaults
	}
	merged := *current
	merged.Chat.TimeRules = append([]TimeRuleConfig(nil), current.Chat.TimeRules...)
	merge(&merged)
	validated, err := merged.Validate()
	if err != nil {
		return fmt.Errorf("合并后配置校验失败: %w", err)
	}
	store.mu.Lock()
	store.current.Store(&validated)
	store.mu.Unlock()
	return nil
}
