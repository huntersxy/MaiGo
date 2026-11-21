package test

import (
	"testing"

	"MaiGo/internal/maisaka/turn_trigger"
)

func TestGroupViewingDecayAndRecovery(t *testing.T) {
	var view turn_trigger.ViewFrequencyController
	if !view.Decide(0.125, 0.1) || view.Internal != 0.125 {
		t.Fatalf("unexpected first view: %+v", view)
	}
	view.Activate(0.125)
	if view.RecordPlannerRun() || view.Probability(0.125) != 1 {
		t.Fatalf("unexpected first decay: %+v", view)
	}
	view.Activate(0.75)
	if !view.RecordPlannerRun() || view.Probability(0.125) != 0 {
		t.Fatalf("unexpected recovery start: %+v", view)
	}
	for round := 0; round < 16; round++ {
		if view.Decide(0.125, 0.9) {
			t.Fatal("view accepted above its recovery probability")
		}
	}
	if view.Phase != turn_trigger.ViewFrequencyIdle || view.Probability(0.125) != 0.125 {
		t.Fatalf("unexpected recovery end: %+v", view)
	}
}

func TestGroupViewingIntervalUsesDistinctArrivalTimes(t *testing.T) {
	interval, ok := turn_trigger.MeanMessageInterval([]float64{19, 4, 9, 9})
	if !ok || interval != 7.5 {
		t.Fatalf("interval = %v, ok = %v", interval, ok)
	}
}
