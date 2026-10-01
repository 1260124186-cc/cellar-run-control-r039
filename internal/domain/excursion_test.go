package domain

import (
	"testing"
	"time"
)

func observationAt(sequence int, temperature float64, at time.Time) GravityObservation {
	return GravityObservation{
		Sequence:     sequence,
		Gravity:      1.050 - float64(sequence)*0.002,
		TemperatureC: temperature,
		ObservedAt:   at,
		ReceivedAt:   at,
	}
}

func TestEvaluateTemperatureExcursionsMarksAndAccumulates(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	observations := []GravityObservation{
		observationAt(1, 20.0, base),
		observationAt(2, 22.0, base.Add(10*time.Second)), // equal to limit is allowed
		observationAt(3, 23.0, base.Add(20*time.Second)), // breach opens
		observationAt(4, 24.0, base.Add(30*time.Second)), // breach continues
		observationAt(5, 21.0, base.Add(40*time.Second)), // breach closes: 20s
	}

	summary := EvaluateTemperatureExcursions(observations, 22.0, nil)

	if summary.LimitC != 22.0 {
		t.Fatalf("limit = %v, want 22", summary.LimitC)
	}
	if summary.BreachCount != 2 {
		t.Fatalf("breach count = %d, want 2", summary.BreachCount)
	}
	if summary.TotalSeconds != 20 {
		t.Fatalf("total seconds = %d, want 20", summary.TotalSeconds)
	}
	if summary.Open {
		t.Fatal("summary must not be open after an in-range reading")
	}
	if summary.Finalized {
		t.Fatal("summary must not be finalized without an end time")
	}
	expectedFlags := []bool{false, false, true, true, false}
	for index, want := range expectedFlags {
		if observations[index].TempOverLimit != want {
			t.Fatalf("observation %d flag = %v, want %v",
				index+1, observations[index].TempOverLimit, want)
		}
	}
}

func TestEvaluateTemperatureExcursionsKeepsOpenBreachUncounted(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	observations := []GravityObservation{
		observationAt(1, 23.0, base),                     // open
		observationAt(2, 20.0, base.Add(10*time.Second)), // closes: 10s
		observationAt(3, 25.0, base.Add(20*time.Second)), // opens again
		observationAt(4, 26.0, base.Add(30*time.Second)), // still open
	}

	summary := EvaluateTemperatureExcursions(observations, 22.0, nil)

	if !summary.Open {
		t.Fatal("last breach reading must keep the interval open")
	}
	if summary.OpenStartedAt == nil || !summary.OpenStartedAt.Equal(base.Add(20*time.Second)) {
		t.Fatalf("open start = %v, want %v", summary.OpenStartedAt, base.Add(20*time.Second))
	}
	if summary.BreachCount != 3 {
		t.Fatalf("breach count = %d, want 3", summary.BreachCount)
	}
	if summary.TotalSeconds != 10 {
		t.Fatalf("total seconds = %d, want 10 (open interval excluded)", summary.TotalSeconds)
	}
}

func TestEvaluateTemperatureExcursionsFinalizesOpenBreach(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	observations := []GravityObservation{
		observationAt(1, 23.0, base.Add(10*time.Second)),
		observationAt(2, 20.0, base.Add(20*time.Second)), // closes first: 10s
		observationAt(3, 25.0, base.Add(30*time.Second)), // open
	}
	end := base.Add(90 * time.Second)

	summary := EvaluateTemperatureExcursions(observations, 22.0, &end)

	if summary.Open || !summary.Finalized {
		t.Fatalf("finalized summary must be closed and final: %+v", summary)
	}
	if summary.OpenStartedAt != nil {
		t.Fatal("finalized summary must clear its open start")
	}
	// 10 seconds closed plus 60 seconds from the last breach to end.
	if summary.TotalSeconds != 70 {
		t.Fatalf("total seconds = %d, want 70", summary.TotalSeconds)
	}
}

func TestRunLifecycleDerivesExcursionAndCopiesIntoMetrics(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run := FermentationRun{
		ID:           "run_test",
		Code:         "RUN-TEST",
		State:        RunReserved,
		Observations: []GravityObservation{},
		Version:      1,
		CreatedAt:    base,
		UpdatedAt:    base,
	}
	vesselID := "vessel_test"
	run.VesselID = &vesselID

	if err := run.StartFermentation(22.0, base); err != nil {
		t.Fatalf("start: %v", err)
	}
	if run.MaxTempLimitC == nil || *run.MaxTempLimitC != 22.0 {
		t.Fatal("run must capture the formula limit at start")
	}

	times := []time.Duration{10, 20, 30, 40}
	temps := []float64{20.0, 23.0, 24.0, 21.0} // breach from +20s to +40s = 20s
	for index := range times {
		observation := observationAt(index+1, temps[index],
			base.Add(times[index]*time.Second))
		if err := run.AddObservation(observation, 30.0 /* a later formula change */); err != nil {
			t.Fatalf("add observation %d: %v", index+1, err)
		}
	}

	// The captured 22C limit must win over the later 30C formula value.
	if run.TemperatureExcursion.LimitC != 22.0 ||
		run.TemperatureExcursion.BreachCount != 2 ||
		run.TemperatureExcursion.TotalSeconds != 20 {
		t.Fatalf("excursion derived against the wrong limit: %+v", run.TemperatureExcursion)
	}

	if err := run.BeginConditioning(base.Add(50 * time.Second)); err != nil {
		t.Fatalf("conditioning: %v", err)
	}
	if err := run.Complete(1.050, 30.0, base.Add(60*time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if run.State != RunCompleted || run.Metrics == nil {
		t.Fatal("run must be completed with metrics")
	}
	if run.Metrics.TempLimitC != 22.0 ||
		run.Metrics.TempBreachCount != 2 ||
		run.Metrics.TempExcursionSeconds != 20 {
		t.Fatalf("metrics excursion fields diverge: %+v", run.Metrics)
	}
	if !run.TemperatureExcursion.Finalized || run.TemperatureExcursion.Open {
		t.Fatal("completed excursion must be finalized")
	}
}

func TestAbortFinalizesOpenBreach(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run := FermentationRun{
		ID:           "run_abort",
		Code:         "RUN-ABORT",
		State:        RunReserved,
		Observations: []GravityObservation{},
		Version:      1,
		CreatedAt:    base,
		UpdatedAt:    base,
	}
	vesselID := "vessel_abort"
	run.VesselID = &vesselID

	if err := run.StartFermentation(22.0, base); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := run.AddObservation(
		observationAt(1, 25.0, base.Add(10*time.Second)), 22.0); err != nil {
		t.Fatalf("add observation: %v", err)
	}
	if !run.TemperatureExcursion.Open {
		t.Fatal("breach should be open before abort")
	}

	if err := run.Abort(22.0, base.Add(70*time.Second)); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if run.State != RunAborted {
		t.Fatal("run must be aborted")
	}
	if run.TemperatureExcursion.Open || !run.TemperatureExcursion.Finalized {
		t.Fatalf("abort must finalize the open breach: %+v", run.TemperatureExcursion)
	}
	if run.TemperatureExcursion.TotalSeconds != 60 {
		t.Fatalf("total seconds = %d, want 60", run.TemperatureExcursion.TotalSeconds)
	}
}
