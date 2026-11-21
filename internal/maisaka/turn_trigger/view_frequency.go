package turn_trigger

import (
	"math"
	"slices"
)

const viewDecayStep = 1.0 / 16
const viewRecoveryRounds = 16

type ViewFrequencyPhase string

const (
	ViewFrequencyIdle     ViewFrequencyPhase = "idle"
	ViewFrequencyDecay    ViewFrequencyPhase = "decay"
	ViewFrequencyRecovery ViewFrequencyPhase = "recovery"
)

type ViewFrequencyController struct {
	Phase          ViewFrequencyPhase
	Internal       float64
	RecoveryRounds int
}

func (c *ViewFrequencyController) Probability(external float64) float64 {
	external = clampViewFrequency(external)
	if external == 0 {
		return 0
	}
	switch c.Phase {
	case ViewFrequencyDecay:
		return 1
	case ViewFrequencyRecovery:
		return external * float64(c.RecoveryRounds) / viewRecoveryRounds
	default:
		return external
	}
}

func (c *ViewFrequencyController) Decide(external, draw float64) bool {
	external = clampViewFrequency(external)
	if c.Phase == ViewFrequencyIdle || c.Phase == "" {
		c.Phase = ViewFrequencyIdle
		c.Internal = external
	}
	accepted := draw < c.Probability(external)
	if c.Phase == ViewFrequencyRecovery {
		c.RecoveryRounds++
		c.Internal = external * float64(c.RecoveryRounds) / viewRecoveryRounds
		if c.RecoveryRounds >= viewRecoveryRounds {
			c.Phase = ViewFrequencyIdle
		}
	}
	return accepted
}

func (c *ViewFrequencyController) Activate(external float64) {
	if c.Phase == ViewFrequencyDecay {
		return
	}
	c.Phase = ViewFrequencyDecay
	c.Internal = clampViewFrequency(external)
	c.RecoveryRounds = 0
}

func (c *ViewFrequencyController) RecordPlannerRun() bool {
	if c.Phase != ViewFrequencyDecay {
		return false
	}
	c.Internal = math.Max(0, c.Internal-viewDecayStep)
	if c.Internal > 0 {
		return false
	}
	c.Phase = ViewFrequencyRecovery
	c.RecoveryRounds = 0
	return true
}

func clampViewFrequency(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func MeanMessageInterval(received []float64) (float64, bool) {
	if len(received) < 2 {
		return 0, false
	}
	sorted := slices.Clone(received)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	if len(sorted) < 2 {
		return 0, false
	}
	return (sorted[len(sorted)-1] - sorted[0]) / float64(len(sorted)-1), true
}

func PredictMessageInterval(current, previous float64, hasCurrent, hasPrevious bool) (float64, bool) {
	switch {
	case hasCurrent && hasPrevious:
		return (current + previous) / 2, true
	case hasCurrent:
		return current, true
	case hasPrevious:
		return previous, true
	default:
		return 0, false
	}
}
