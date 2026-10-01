package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// temperatureExcursions verifies that each observation is marked against the
// formula limit, that the accumulated over-limit duration is recomputed as
// observations are appended, that completion/abort finalize a still-open
// breach, and that changing the formula threshold only affects later runs.
func temperatureExcursions(api *client) error {
	if err := excursionAccumulatesAndFinalizes(api); err != nil {
		return err
	}
	if err := excursionClosesOnAbort(api); err != nil {
		return err
	}
	if err := excursionThresholdChangeIsNotRetroactive(api); err != nil {
		return err
	}
	return nil
}

func excursionAccumulatesAndFinalizes(api *client) error {
	formula, err := ensureApprovedFormulaWithMaxTemp(api, "Excursion Formula", 22.0)
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-301", 300)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-EXCURSIONS")
	if err != nil {
		return err
	}
	runID := started.Run.ID
	version := started.Run.Version
	if started.Run.MaxTempLimitC == nil || *started.Run.MaxTempLimitC != 22.0 {
		return errInvalidWorkflow("run did not capture the formula temperature limit at start")
	}

	start := *started.Run.StartedAt
	// Times are spaced 10 seconds apart. Breach readings at +10s and +20s,
	// an in-range reading at +30s closes the first breach (20 seconds), then
	// one breach at +40s stays open until completion.
	points := []struct {
		gravity     float64
		temperature float64
		overLimit   bool
	}{
		{gravity: 1.050, temperature: 20.0, overLimit: false},
		{gravity: 1.044, temperature: 23.5, overLimit: true},
		{gravity: 1.038, temperature: 24.0, overLimit: true},
		{gravity: 1.030, temperature: 21.0, overLimit: false},
		{gravity: 1.022, temperature: 25.0, overLimit: true},
	}
	for index, point := range points {
		sequence := index + 1
		observedAt := start.Add(time.Duration(sequence) * 10 * time.Second)
		run, err := appendCheckObservation(api, runID, version, sequence,
			point.gravity, point.temperature, observedAt, "excursion reading")
		if err != nil {
			return err
		}
		version = run.Version
		last := run.Observations[len(run.Observations)-1]
		if last.TempOverLimit != point.overLimit {
			return fmt.Errorf("observation %d over-limit flag = %v, want %v",
				sequence, last.TempOverLimit, point.overLimit)
		}
		if run.TemperatureExcursion == nil {
			return errInvalidWorkflow("run detail is missing the excursion summary")
		}
	}

	// After the in-range fourth reading the first two breach readings form a
	// closed 20 second breach, while the fifth reading opens a new one.
	intermediate, err := getCheckRun(api, runID)
	if err != nil {
		return err
	}
	summary := intermediate.TemperatureExcursion
	if summary == nil || summary.LimitC != 22.0 || summary.BreachCount != 3 ||
		summary.TotalSeconds != 20 || !summary.Open {
		return fmt.Errorf("unexpected intermediate excursion summary: %+v", summary)
	}

	conditioningResponse, err := api.call("POST", "/v1/runs/"+runID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(conditioningResponse, 200, "begin conditioning for excursion run"); err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}

	// Completion finalizes the open breach. Because the final breach reading
	// carries a near-future timestamp and completion happens immediately, the
	// trailing interval rounds to zero seconds; the previously closed breach
	// still contributes exactly 20 seconds. The exact trailing-arithmetic is
	// covered by the domain unit tests with a controlled clock.
	completeResponse, err := api.call("POST", "/v1/runs/"+runID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete excursion run"); err != nil {
		return err
	}
	var completed pairView
	if err := completeResponse.decode(&completed); err != nil {
		return err
	}
	final := completed.Run
	if final.TemperatureExcursion == nil {
		return errInvalidWorkflow("completed run lost the excursion summary")
	}
	if final.TemperatureExcursion.Open || !final.TemperatureExcursion.Finalized {
		return errInvalidWorkflow("completed run still reports an open excursion")
	}
	if final.TemperatureExcursion.BreachCount != 3 {
		return fmt.Errorf("completed breach count = %d, want 3",
			final.TemperatureExcursion.BreachCount)
	}
	if final.TemperatureExcursion.TotalSeconds != 20 {
		return fmt.Errorf("completed excursion seconds = %d, want 20",
			final.TemperatureExcursion.TotalSeconds)
	}
	if final.Metrics == nil {
		return errInvalidWorkflow("completed run is missing metrics")
	}
	if final.Metrics.TempBreachCount != final.TemperatureExcursion.BreachCount ||
		final.Metrics.TempExcursionSeconds != final.TemperatureExcursion.TotalSeconds ||
		final.Metrics.TempLimitC != 22.0 {
		return errInvalidWorkflow("final metrics diverge from the excursion summary")
	}

	// The run list must expose the same derived result as the detail view.
	listResponse, err := api.call("GET", "/v1/runs", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listResponse, 200, "list runs for excursion consistency"); err != nil {
		return err
	}
	var list runListView
	if err := listResponse.decode(&list); err != nil {
		return err
	}
	found := false
	for _, entry := range list.Runs {
		if entry.ID != runID {
			continue
		}
		found = true
		if entry.TemperatureExcursion == nil ||
			entry.TemperatureExcursion.TotalSeconds != final.TemperatureExcursion.TotalSeconds ||
			entry.TemperatureExcursion.BreachCount != final.TemperatureExcursion.BreachCount {
			return errInvalidWorkflow("run list excursion summary disagrees with run detail")
		}
	}
	if !found {
		return errInvalidWorkflow("completed excursion run missing from list")
	}
	return nil
}

func excursionClosesOnAbort(api *client) error {
	formula, err := ensureApprovedFormulaWithMaxTemp(api, "Excursion Abort Formula", 22.0)
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-302", 300)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-EXC-ABORT")
	if err != nil {
		return err
	}
	runID := started.Run.ID
	version := started.Run.Version

	points := []struct {
		gravity     float64
		temperature float64
	}{
		{gravity: 1.050, temperature: 20.0},
		{gravity: 1.040, temperature: 24.0},
	}
	for index, point := range points {
		sequence := index + 1
		// The run only just started, so valid observed times live between the
		// run start and one minute ahead. The breach therefore closes at an
		// abort time within a second of opening; the exact trailing interval
		// is verified deterministically in the domain unit tests, while this
		// check proves that abort synchronously finalizes the open breach.
		observedAt := time.Now().UTC().Add(time.Duration(sequence) * 500 * time.Millisecond)
		run, err := appendCheckObservation(api, runID, version, sequence,
			point.gravity, point.temperature, observedAt, "abort reading")
		if err != nil {
			return err
		}
		version = run.Version
	}

	openRun, err := getCheckRun(api, runID)
	if err != nil {
		return err
	}
	if openRun.TemperatureExcursion == nil || !openRun.TemperatureExcursion.Open ||
		openRun.TemperatureExcursion.BreachCount != 1 {
		return fmt.Errorf("expected an open single breach before abort, got %+v",
			openRun.TemperatureExcursion)
	}

	// Abort after fermentation must synchronously close and finalize the
	// breach instead of leaving an open interval on the terminated run.
	abortResponse, err := api.call("POST", "/v1/runs/"+runID+"/abort",
		nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(abortResponse, 200, "abort excursion run"); err != nil {
		return err
	}
	var aborted pairView
	if err := abortResponse.decode(&aborted); err != nil {
		return err
	}
	if aborted.Run.State != domain.RunAborted {
		return errInvalidWorkflow("aborted run has unexpected state")
	}
	summary := aborted.Run.TemperatureExcursion
	if summary == nil {
		return errInvalidWorkflow("aborted run lost the excursion summary")
	}
	if summary.Open || !summary.Finalized || summary.BreachCount != 1 {
		return fmt.Errorf("abort did not finalize the open breach: %+v", summary)
	}
	if summary.TotalSeconds < 0 {
		return fmt.Errorf("abort charged a negative duration: %d", summary.TotalSeconds)
	}
	return nil
}

func excursionThresholdChangeIsNotRetroactive(api *client) error {
	oldFormula, err := ensureApprovedFormulaWithMaxTemp(api, "Threshold Old Formula", 22.0)
	if err != nil {
		return err
	}
	oldVessel, err := createCheckVessel(api, "VSL-303", 300)
	if err != nil {
		return err
	}
	oldRun, err := createStartedRun(api, oldFormula.ID, oldVessel.ID, "RUN-THRESH-OLD")
	if err != nil {
		return err
	}
	runID := oldRun.Run.ID
	version := oldRun.Run.Version
	start := *oldRun.Run.StartedAt

	// A 23C reading is a breach against the captured 22C limit.
	over, err := appendCheckObservation(api, runID, version, 1,
		1.050, 23.0, start.Add(10*time.Second), "old threshold reading")
	if err != nil {
		return err
	}
	if over.TemperatureExcursion == nil || over.TemperatureExcursion.BreachCount != 1 {
		return errInvalidWorkflow("reading above the captured limit was not marked")
	}

	// A different approved formula carrying a higher limit represents the
	// threshold change; it must only affect runs started afterwards.
	newFormula, err := ensureApprovedFormulaWithMaxTemp(api, "Threshold New Formula", 30.0)
	if err != nil {
		return err
	}
	newVessel, err := createCheckVessel(api, "VSL-304", 300)
	if err != nil {
		return err
	}
	newRun, err := createStartedRun(api, newFormula.ID, newVessel.ID, "RUN-THRESH-NEW")
	if err != nil {
		return err
	}
	if newRun.Run.MaxTempLimitC == nil || *newRun.Run.MaxTempLimitC != 30.0 {
		return errInvalidWorkflow("new run did not capture the revised limit")
	}
	compliant, err := appendCheckObservation(api, newRun.Run.ID, newRun.Run.Version, 1,
		1.050, 23.0, (*newRun.Run.StartedAt).Add(10*time.Second), "new threshold reading")
	if err != nil {
		return err
	}
	if compliant.TemperatureExcursion == nil ||
		compliant.TemperatureExcursion.BreachCount != 0 ||
		compliant.Observations[0].TempOverLimit {
		return errInvalidWorkflow("revised threshold affected a reading unexpectedly")
	}

	// The already open run keeps evaluating against its captured 22C limit.
	recheck, err := getCheckRun(api, runID)
	if err != nil {
		return err
	}
	if recheck.MaxTempLimitC == nil || *recheck.MaxTempLimitC != 22.0 {
		return errInvalidWorkflow("existing run lost its captured threshold")
	}
	if recheck.TemperatureExcursion == nil ||
		recheck.TemperatureExcursion.LimitC != 22.0 ||
		recheck.TemperatureExcursion.BreachCount != 1 {
		return errInvalidWorkflow("threshold change retroactively rewrote an open run")
	}
	return nil
}

func ensureApprovedFormulaWithMaxTemp(
	api *client,
	name string,
	maxTempC float64,
) (formulaView, error) {
	payload := createFormulaPayload(name)
	payload["max_fermentation_temp_c"] = maxTempC
	createdResponse, err := api.call("POST", "/v1/formulas", payload, nil)
	if err != nil {
		return formulaView{}, err
	}
	if err := requireStatus(createdResponse, 201, "create threshold formula"); err != nil {
		return formulaView{}, err
	}
	var formula formulaView
	if err := createdResponse.decode(&formula); err != nil {
		return formulaView{}, err
	}
	stepsResponse, err := api.call("PUT", "/v1/formulas/"+formula.ID+"/steps",
		formulaStepPayload(), ptrInt64(1))
	if err != nil {
		return formulaView{}, err
	}
	if err := requireStatus(stepsResponse, 200, "set threshold formula steps"); err != nil {
		return formulaView{}, err
	}
	approveResponse, err := api.call("POST", "/v1/formulas/"+formula.ID+"/approve",
		nil, ptrInt64(2))
	if err != nil {
		return formulaView{}, err
	}
	if err := requireStatus(approveResponse, 200, "approve threshold formula"); err != nil {
		return formulaView{}, err
	}
	if err := approveResponse.decode(&formula); err != nil {
		return formulaView{}, err
	}
	return formula, nil
}

func appendCheckObservation(
	api *client,
	runID string,
	version int64,
	sequence int,
	gravity, temperature float64,
	observedAt time.Time,
	note string,
) (runView, error) {
	response, err := api.call("POST", "/v1/runs/"+runID+"/observations",
		map[string]any{
			"sequence":      sequence,
			"gravity":       gravity,
			"temperature_c": temperature,
			"observed_at":   observedAt,
			"note":          note,
		}, ptrInt64(version))
	if err != nil {
		return runView{}, err
	}
	if err := requireStatus(response, 200, "append excursion observation"); err != nil {
		return runView{}, err
	}
	var run runView
	if err := response.decode(&run); err != nil {
		return runView{}, err
	}
	return run, nil
}

func getCheckRun(api *client, runID string) (runView, error) {
	response, err := api.call("GET", "/v1/runs/"+runID, nil, nil)
	if err != nil {
		return runView{}, err
	}
	if err := requireStatus(response, 200, "read excursion run"); err != nil {
		return runView{}, err
	}
	var run runView
	if err := response.decode(&run); err != nil {
		return runView{}, err
	}
	return run, nil
}
