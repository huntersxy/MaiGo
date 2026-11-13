package utils

// A_memorix 关系记忆生命周期策略，对齐 Python core/utils/memory_lifecycle_policy.py
// 生命周期状态以关系元数据为事实源，所有计算都基于调用方传入的墙钟时间，因此结果确定
// 注意：Python 源码中只有 RelationLifecycle 系列类型，不存在 KnowledgeLifecycleState

import (
	"fmt"
	"math"
)

// RelationLifecycleEvent 会改变关系保留状态的领域事件
type RelationLifecycleEvent string

const (
	LifecycleEventAccess    RelationLifecycleEvent = "access"
	LifecycleEventEvidence  RelationLifecycleEvent = "evidence"
	LifecycleEventReinforce RelationLifecycleEvent = "reinforce"
	LifecycleEventWeaken    RelationLifecycleEvent = "weaken"
	LifecycleEventFreeze    RelationLifecycleEvent = "freeze"
	LifecycleEventForget    RelationLifecycleEvent = "forget"
)

// RelationLifecyclePolicy 关系生命周期参数，字段默认值见 DefaultRelationLifecyclePolicy
type RelationLifecyclePolicy struct {
	HalfLifeHours         float64
	FreezeThreshold       float64
	ReviveThreshold       float64
	AccessAlpha           float64
	AccessCooldownSeconds float64
	ReinforceAlpha        float64
	WeakenAlpha           float64
}

// DefaultRelationLifecyclePolicy 返回 Python dataclass 的默认参数
func DefaultRelationLifecyclePolicy() RelationLifecyclePolicy {
	return RelationLifecyclePolicy{
		HalfLifeHours:         24.0,
		FreezeThreshold:       0.1,
		ReviveThreshold:       0.15,
		AccessAlpha:           0.05,
		AccessCooldownSeconds: 3600.0,
		ReinforceAlpha:        0.5,
		WeakenAlpha:           0.5,
	}
}

// Validate 对齐 RelationLifecyclePolicy.__post_init__ 的校验顺序与报错文案
func (p RelationLifecyclePolicy) Validate() error {
	if !isFinite(p.HalfLifeHours) || p.HalfLifeHours <= 0.0 {
		return fmt.Errorf("half_life_hours 必须是大于0的有限数")
	}
	unitFields := []struct {
		name  string
		value float64
	}{
		{"freeze_threshold", p.FreezeThreshold},
		{"revive_threshold", p.ReviveThreshold},
		{"access_alpha", p.AccessAlpha},
		{"reinforce_alpha", p.ReinforceAlpha},
		{"weaken_alpha", p.WeakenAlpha},
	}
	for _, field := range unitFields {
		if !isFinite(field.value) || field.value < 0.0 || field.value > 1.0 {
			return fmt.Errorf("%s 必须位于[0, 1]", field.name)
		}
	}
	if !isFinite(p.AccessCooldownSeconds) || p.AccessCooldownSeconds < 0.0 {
		return fmt.Errorf("access_cooldown_seconds 必须是大于等于0的有限数")
	}
	if !(p.FreezeThreshold > 0.0 && p.FreezeThreshold < 1.0) {
		return fmt.Errorf("freeze_threshold 必须位于(0, 1)")
	}
	if p.ReviveThreshold <= p.FreezeThreshold {
		return fmt.Errorf("revive_threshold 必须大于 freeze_threshold")
	}
	return nil
}

// NewRelationLifecyclePolicy 构造并校验策略，对齐 Python 在构造时就抛错的行为
func NewRelationLifecyclePolicy(policy RelationLifecyclePolicy) (RelationLifecyclePolicy, error) {
	if err := policy.Validate(); err != nil {
		return RelationLifecyclePolicy{}, err
	}
	return policy, nil
}

// RelationLifecycleState 计算生命周期所需的最小关系状态，可选字段用指针表示 None
type RelationLifecycleState struct {
	Strength               float64
	AnchorAt               float64
	IsInactive             bool
	InactiveSince          *float64
	InactiveReason         *string
	LastAccessReinforcedAt *float64
}

// RelationLifecycleDecision 一次维护或事件计算的确定性结果
type RelationLifecycleDecision struct {
	Strength               float64
	AnchorAt               float64
	NextLifecycleAt        *float64
	IsInactive             bool
	InactiveSince          *float64
	InactiveReason         *string
	LastAccessReinforcedAt *float64
	Changed                bool
}

// ClampUnit 把有限数限制到 [0, 1]，对齐 clamp_unit
func ClampUnit(value float64) (float64, error) {
	if !isFinite(value) {
		return 0, fmt.Errorf("生命周期强度必须是有限数")
	}
	return math.Min(1.0, math.Max(0.0, value)), nil
}

// RetentionAt 惰性计算指定墙钟时刻的关系保留强度，对齐 retention_at
func RetentionAt(state RelationLifecycleState, now float64, policy RelationLifecyclePolicy) (float64, error) {
	if !isFinite(now) {
		return 0, fmt.Errorf("now 必须是有限时间戳")
	}
	if !isFinite(state.AnchorAt) {
		return 0, fmt.Errorf("anchor_at 必须是有限时间戳")
	}
	strength, err := ClampUnit(state.Strength)
	if err != nil {
		return 0, err
	}
	elapsedHours := math.Max(0.0, now-state.AnchorAt) / 3600.0
	return strength * math.Pow(2.0, -elapsedHours/policy.HalfLifeHours), nil
}

// ThresholdCrossingAt 计算当前锚点首次到达冻结阈值的墙钟时间，对齐 threshold_crossing_at
func ThresholdCrossingAt(strength float64, anchorAt float64, policy RelationLifecyclePolicy) (float64, error) {
	normalized, err := ClampUnit(strength)
	if err != nil {
		return 0, err
	}
	if !isFinite(anchorAt) {
		return 0, fmt.Errorf("anchor_at 必须是有限时间戳")
	}
	if normalized <= policy.FreezeThreshold {
		return anchorAt, nil
	}
	elapsedHours := policy.HalfLifeHours * math.Log2(normalized/policy.FreezeThreshold)
	return anchorAt + elapsedHours*3600.0, nil
}

// EvaluateLifecycle 计算关系是否应冻结或安排下一次到期检查，对齐 evaluate_lifecycle
func EvaluateLifecycle(state RelationLifecycleState, now float64, policy RelationLifecyclePolicy) (RelationLifecycleDecision, error) {
	timestamp, err := lifecycleLogicalNow(state, now)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	effective, err := RetentionAt(state, timestamp, policy)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	strength, err := ClampUnit(state.Strength)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	if state.IsInactive {
		return RelationLifecycleDecision{
			Strength:               strength,
			AnchorAt:               state.AnchorAt,
			NextLifecycleAt:        nil,
			IsInactive:             true,
			InactiveSince:          state.InactiveSince,
			InactiveReason:         state.InactiveReason,
			LastAccessReinforcedAt: state.LastAccessReinforcedAt,
			Changed:                false,
		}, nil
	}

	dueAt, err := ThresholdCrossingAt(state.Strength, state.AnchorAt, policy)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	if effective <= policy.FreezeThreshold || timestamp >= dueAt {
		since := timestamp
		reason := "decay"
		return RelationLifecycleDecision{
			Strength:               strength,
			AnchorAt:               state.AnchorAt,
			NextLifecycleAt:        nil,
			IsInactive:             true,
			InactiveSince:          &since,
			InactiveReason:         &reason,
			LastAccessReinforcedAt: state.LastAccessReinforcedAt,
			Changed:                true,
		}, nil
	}
	next := dueAt
	return RelationLifecycleDecision{
		Strength:               strength,
		AnchorAt:               state.AnchorAt,
		NextLifecycleAt:        &next,
		IsInactive:             false,
		InactiveSince:          nil,
		InactiveReason:         nil,
		LastAccessReinforcedAt: state.LastAccessReinforcedAt,
		Changed:                true,
	}, nil
}

// ApplyLifecycleEvent 把访问、强化、弱化或遗忘事件应用到关系状态，对齐 apply_lifecycle_event
// strength 对齐 Python 的 strength 参数默认值 1.0，调用方不传时显式传 1.0
func ApplyLifecycleEvent(state RelationLifecycleState, event RelationLifecycleEvent, now float64, policy RelationLifecyclePolicy, strength float64) (RelationLifecycleDecision, error) {
	timestamp, err := lifecycleLogicalNow(state, now)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	current, err := RetentionAt(state, timestamp, policy)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	if !isFinite(strength) || strength < 0.0 {
		return RelationLifecycleDecision{}, fmt.Errorf("事件强度必须是大于等于0的有限数")
	}

	clampedStrength, err := ClampUnit(state.Strength)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}

	var updated float64
	var reason *string
	alpha := 0.0

	switch event {
	case LifecycleEventAccess:
		cooldownElapsed := state.LastAccessReinforcedAt == nil ||
			timestamp-*state.LastAccessReinforcedAt >= policy.AccessCooldownSeconds
		alpha = math.Min(1.0, policy.AccessAlpha*strength)
		updated = current + alpha*(1.0-current)
		revivesWithoutScoreChange := state.IsInactive && cooldownElapsed && alpha > 0.0 &&
			updated >= policy.ReviveThreshold
		if !cooldownElapsed || ((alpha == 0.0 || updated == current) && !revivesWithoutScoreChange) {
			var nextAt *float64
			if !state.IsInactive {
				next, err := ThresholdCrossingAt(state.Strength, state.AnchorAt, policy)
				if err != nil {
					return RelationLifecycleDecision{}, err
				}
				if current <= policy.FreezeThreshold || timestamp >= next {
					since := timestamp
					decay := "decay"
					return RelationLifecycleDecision{
						Strength:               clampedStrength,
						AnchorAt:               state.AnchorAt,
						NextLifecycleAt:        nil,
						IsInactive:             true,
						InactiveSince:          &since,
						InactiveReason:         &decay,
						LastAccessReinforcedAt: state.LastAccessReinforcedAt,
						Changed:                true,
					}, nil
				}
				nextAt = &next
			}
			return RelationLifecycleDecision{
				Strength:               clampedStrength,
				AnchorAt:               state.AnchorAt,
				NextLifecycleAt:        nextAt,
				IsInactive:             state.IsInactive,
				InactiveSince:          state.InactiveSince,
				InactiveReason:         state.InactiveReason,
				LastAccessReinforcedAt: state.LastAccessReinforcedAt,
				Changed:                false,
			}, nil
		}
	case LifecycleEventReinforce:
		alpha = math.Min(1.0, policy.ReinforceAlpha*strength)
		updated = current + alpha*(1.0-current)
	case LifecycleEventEvidence:
		updated = 1.0
	case LifecycleEventWeaken:
		alpha = math.Min(1.0, policy.WeakenAlpha*strength)
		updated = current * (1.0 - alpha)
		if updated <= policy.FreezeThreshold {
			weaken := "weaken"
			reason = &weaken
		}
	case LifecycleEventFreeze:
		updated = current
		manual := "manual_freeze"
		reason = &manual
	case LifecycleEventForget:
		updated = 0.0
		manual := "manual_forget"
		reason = &manual
	default:
		return RelationLifecycleDecision{}, fmt.Errorf("不支持的生命周期事件: %s", string(event))
	}

	updated, err = ClampUnit(updated)
	if err != nil {
		return RelationLifecycleDecision{}, err
	}
	forceInactive := event == LifecycleEventFreeze || event == LifecycleEventForget
	shouldFreeze := forceInactive || updated <= policy.FreezeThreshold
	shouldRevive := updated >= policy.ReviveThreshold && !forceInactive

	isInactive := state.IsInactive
	inactiveSince := state.InactiveSince
	inactiveReason := state.InactiveReason

	if shouldFreeze {
		isInactive = true
		if state.IsInactive {
			inactiveSince = state.InactiveSince
		} else {
			since := timestamp
			inactiveSince = &since
		}
		if reason != nil {
			inactiveReason = reason
		} else {
			decay := "decay"
			inactiveReason = &decay
		}
	} else if !state.IsInactive || shouldRevive {
		isInactive = false
		inactiveSince = nil
		inactiveReason = nil
	}

	var nextAt *float64
	if !isInactive {
		next, err := ThresholdCrossingAt(updated, timestamp, policy)
		if err != nil {
			return RelationLifecycleDecision{}, err
		}
		nextAt = &next
	}

	lastAccess := state.LastAccessReinforcedAt
	if event == LifecycleEventAccess {
		reinforcedAt := timestamp
		lastAccess = &reinforcedAt
	}

	return RelationLifecycleDecision{
		Strength:               updated,
		AnchorAt:               timestamp,
		NextLifecycleAt:        nextAt,
		IsInactive:             isInactive,
		InactiveSince:          inactiveSince,
		InactiveReason:         inactiveReason,
		LastAccessReinforcedAt: lastAccess,
		Changed:                true,
	}, nil
}

// ApplyLifecycleEventDefault 对齐 Python 的 strength 默认值 1.0
func ApplyLifecycleEventDefault(state RelationLifecycleState, event RelationLifecycleEvent, now float64, policy RelationLifecyclePolicy) (RelationLifecycleDecision, error) {
	return ApplyLifecycleEvent(state, event, now, policy, 1.0)
}

// lifecycleLogicalNow 复刻 EvaluateLifecycle/ApplyLifecycleEvent 共用的逻辑墙钟下界
// 墙钟允许被校时回拨，但持久化生命周期时间不能倒退：取 now、anchor_at、
// last_access_reinforced_at、inactive_since 的最大值
func lifecycleLogicalNow(state RelationLifecycleState, now float64) (float64, error) {
	if !isFinite(now) {
		return 0, fmt.Errorf("now 必须是有限时间戳")
	}
	if !isFinite(state.AnchorAt) {
		return 0, fmt.Errorf("anchor_at 必须是有限时间戳")
	}
	if state.LastAccessReinforcedAt != nil && !isFinite(*state.LastAccessReinforcedAt) {
		return 0, fmt.Errorf("last_access_reinforced_at 必须是有限时间戳")
	}
	if state.InactiveSince != nil && !isFinite(*state.InactiveSince) {
		return 0, fmt.Errorf("inactive_since 必须是有限时间戳")
	}
	timestamp := math.Max(now, state.AnchorAt)
	if state.LastAccessReinforcedAt != nil {
		timestamp = math.Max(timestamp, *state.LastAccessReinforcedAt)
	}
	if state.InactiveSince != nil {
		timestamp = math.Max(timestamp, *state.InactiveSince)
	}
	return timestamp, nil
}

// isFinite 对齐 math.isfinite
func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
