package host

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// RunnerGroupSpec 双 Supervisor 启动计划中的单个分组
type RunnerGroupSpec struct {
	Group RunnerGroup
	// Options 分组启动参数；Group 为空时使用本结构体的 Group
	Options StartRunnerOptions
	// RequireReady 为 true 时等待 runner.ready，超时或失败会回滚已启动分组
	RequireReady bool
	// ReadyTimeout 等待就绪超时，<=0 使用 DefaultRunnerSpawnWait
	ReadyTimeout time.Duration
}

// RunnerGroupPlan 返回内置、第三方分组的启动顺序，内置优先
func RunnerGroupPlan() []RunnerGroup {
	return []RunnerGroup{RunnerGroupBuiltin, RunnerGroupThirdParty}
}

func runnerGroupPriority(group RunnerGroup) int {
	switch group {
	case RunnerGroupBuiltin:
		return 0
	case RunnerGroupThirdParty:
		return 1
	default:
		return 2
	}
}

// StartRunnerGroups 按内置优先、第三方在后的顺序启动分组
//
// 任一分组启动失败或要求就绪但未就绪时，按已启动分组的逆序回滚，避免半启动状态。
func (h *Host) StartRunnerGroups(ctx context.Context, specs []RunnerGroupSpec) ([]*RunnerState, error) {
	if h == nil {
		return nil, fmt.Errorf("Host 未初始化")
	}
	normalized, err := normalizeRunnerGroupSpecs(specs)
	if err != nil {
		return nil, err
	}
	states := make([]*RunnerState, 0, len(normalized))
	for _, spec := range normalized {
		state, startErr := h.StartRunner(ctx, spec.Options)
		if startErr != nil {
			h.StopRunnerGroupsReverse(specsToGroups(normalized[:len(states)]))
			return nil, fmt.Errorf("启动 Runner 分组 %s 失败: %w", spec.Group, startErr)
		}
		states = append(states, state)
		if !spec.RequireReady {
			continue
		}
		timeout := spec.ReadyTimeout
		if timeout <= 0 {
			timeout = h.RunnerSpawnTimeout()
		}
		if _, readyErr := h.WaitForRunnerReady(ctx, spec.Group, timeout); readyErr != nil {
			h.StopRunnerGroupsReverse(specsToGroups(normalized[:len(states)]))
			return nil, fmt.Errorf("Runner 分组 %s 未就绪: %w", spec.Group, readyErr)
		}
	}
	return states, nil
}

func normalizeRunnerGroupSpecs(specs []RunnerGroupSpec) ([]RunnerGroupSpec, error) {
	normalized := make([]RunnerGroupSpec, 0, len(specs))
	seen := make(map[RunnerGroup]struct{}, len(specs))
	for _, spec := range specs {
		group := spec.Group
		if group == "" {
			group = spec.Options.Group
		}
		group = runnerGroupOrDefault(group)
		if _, exists := seen[group]; exists {
			return nil, fmt.Errorf("Runner 分组重复: %s", group)
		}
		seen[group] = struct{}{}
		spec.Group = group
		spec.Options.Group = group
		normalized = append(normalized, spec)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		return runnerGroupPriority(normalized[i].Group) < runnerGroupPriority(normalized[j].Group)
	})
	return normalized, nil
}

func specsToGroups(specs []RunnerGroupSpec) []RunnerGroup {
	groups := make([]RunnerGroup, 0, len(specs))
	for _, spec := range specs {
		groups = append(groups, spec.Group)
	}
	return groups
}

// StopRunnerGroupsReverse 按启动逆序停止指定分组，第三方先于内置
func (h *Host) StopRunnerGroupsReverse(groups []RunnerGroup) {
	if h == nil {
		return
	}
	unique := make([]RunnerGroup, 0, len(groups))
	seen := make(map[RunnerGroup]struct{}, len(groups))
	for _, group := range groups {
		group = runnerGroupOrDefault(group)
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		unique = append(unique, group)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return runnerGroupPriority(unique[i]) > runnerGroupPriority(unique[j])
	})
	for _, group := range unique {
		h.StopRunner(group)
	}
}
