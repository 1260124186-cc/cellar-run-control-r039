package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const checkMaxFermentationTempC = 22.0

func observationsCompletion(api *client) error {
	formula, err := ensureApprovedFormula(api, "Completion Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-202", 300)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-OBSERVATIONS")
	if err != nil {
		return err
	}

	base := time.Now().UTC().Add(10 * time.Second)
	points := []struct {
		gravity     float64
		temperature float64
	}{
		{gravity: 1.050, temperature: 23.0},
		{gravity: 1.036, temperature: 23.0},
		{gravity: 1.028, temperature: 20.0},
		{gravity: 1.014, temperature: 19.8},
	}
	version := started.Run.Version
	var run runView
	for index, point := range points {
		sequence := index + 1
		response, callErr := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       point.gravity,
				"temperature_c": point.temperature,
				"observed_at":   verifiedAt(base, sequence),
				"note":          fmt.Sprintf("observation %d", sequence),
			}, ptrInt64(version))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(response, 200, "append observation"); err != nil {
			return err
		}
		if err := response.decode(&run); err != nil {
			return err
		}
		version = run.Version
		if err := requireObservationLimits(run.Observations, checkMaxFermentationTempC); err != nil {
			return err
		}
	}

	if run.Temperature == nil {
		return errInvalidWorkflow("run detail is missing the temperature excursion summary")
	}
	if !run.Temperature.Exceeded || run.Temperature.Count != 2 ||
		run.Temperature.LimitC != checkMaxFermentationTempC ||
		run.Temperature.DurationSecs != 20 {
		return errInvalidWorkflow(fmt.Sprintf(
			"running excursion summary is wrong: %+v", run.Temperature))
	}

	invalidRange, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      5,
			"gravity":       1.4,
			"temperature_c": 20,
			"observed_at":   verifiedAt(base, 5),
			"note":          "invalid range",
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireCode(invalidRange, 400, string(domain.CodeInvalidInput),
		"reject out-of-range gravity"); err != nil {
		return err
	}

	nonMonotonic, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      5,
			"gravity":       1.012,
			"temperature_c": 20,
			"observed_at":   verifiedAt(base, 4),
			"note":          "repeated time",
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireCode(nonMonotonic, 409, string(domain.CodeObservationConflict),
		"reject non-monotonic observation"); err != nil {
		return err
	}

	// Edit the first excursion back under the limit; the derived summary must
	// drop to a single excursion covering only the second interval.
	editedCool, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 20.0,
			"observed_at":   verifiedAt(base, 1),
			"note":          "observation 1 corrected",
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(editedCool, 200, "edit observation under limit"); err != nil {
		return err
	}
	if err := editedCool.decode(&run); err != nil {
		return err
	}
	version = run.Version
	if run.Observations[0].TemperatureOver {
		return errInvalidWorkflow("edited observation was not re-evaluated against the limit")
	}
	if run.Temperature == nil || run.Temperature.Count != 1 ||
		run.Temperature.DurationSecs != 10 {
		return errInvalidWorkflow(fmt.Sprintf(
			"edited excursion summary was not updated: %+v", run.Temperature))
	}

	// Restore the excursion: editing must mark the observation again.
	editedHot, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 23.0,
			"observed_at":   verifiedAt(base, 1),
			"note":          "observation 1 corrected again",
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(editedHot, 200, "edit observation over limit"); err != nil {
		return err
	}
	if err := editedHot.decode(&run); err != nil {
		return err
	}
	version = run.Version
	if !run.Observations[0].TemperatureOver ||
		run.Temperature == nil || run.Temperature.Count != 2 ||
		run.Temperature.DurationSecs != 20 {
		return errInvalidWorkflow(fmt.Sprintf(
			"restored excursion summary was not updated: %+v", run.Temperature))
	}

	// Editing beyond an existing neighbour time must fail without writes.
	badOrder, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 23.0,
			"observed_at":   verifiedAt(base, 2),
			"note":          "collides with neighbour",
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireCode(badOrder, 409, string(domain.CodeObservationConflict),
		"reject edited observation out of order"); err != nil {
		return err
	}
	missingSequence, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      99,
			"gravity":       1.010,
			"temperature_c": 20.0,
			"observed_at":   verifiedAt(base, 1),
		}, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireCode(missingSequence, 409, string(domain.CodeObservationConflict),
		"reject edit of unknown observation sequence"); err != nil {
		return err
	}

	conditioningResponse, err := api.call("POST",
		"/v1/runs/"+started.Run.ID+"/conditioning", nil, ptrInt64(version))
	if err != nil {
		return err
	}
	if err := requireStatus(conditioningResponse, 200, "begin conditioning"); err != nil {
		return err
	}
	var conditioning runView
	if err := conditioningResponse.decode(&conditioning); err != nil {
		return err
	}
	if err := requireRunState(conditioning, domain.RunConditioning); err != nil {
		return err
	}
	if conditioning.Temperature == nil ||
		conditioning.Temperature.FinalizedAt == nil ||
		conditioning.Temperature.Count != 2 ||
		conditioning.Temperature.DurationSecs != 20 {
		return errInvalidWorkflow(fmt.Sprintf(
			"conditioning did not freeze the excursion summary: %+v",
			conditioning.Temperature))
	}

	completeResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioning.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(completeResponse, 200, "complete run"); err != nil {
		return err
	}
	var completed pairView
	if err := completeResponse.decode(&completed); err != nil {
		return err
	}
	if err := requireRunState(completed.Run, domain.RunCompleted); err != nil {
		return err
	}
	if err := requireVesselState(completed.Vessel, domain.VesselCleaning); err != nil {
		return err
	}
	if completed.Run.Metrics == nil ||
		completed.Run.Metrics.ObservationCount != 4 ||
		completed.Run.Metrics.ApparentAttenuation <= 0 ||
		completed.Run.Metrics.EstimatedAlcoholABV <= 0 {
		return errInvalidWorkflow("completion metrics were not calculated")
	}
	if err := requireFinalizedExcursion(completed.Run); err != nil {
		return err
	}

	repeatResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(completed.Run.Version))
	if err != nil {
		return err
	}
	if err := requireCode(repeatResponse, 409, string(domain.CodeRunState),
		"complete run twice"); err != nil {
		return err
	}

	readResponse, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(readResponse, 200, "read completed run"); err != nil {
		return err
	}
	var readBack runView
	if err := readResponse.decode(&readBack); err != nil {
		return err
	}
	if len(readBack.Observations) != 4 || readBack.Metrics == nil {
		return errInvalidWorkflow("completed run lost observations or metrics")
	}
	if err := requireFinalizedExcursion(readBack); err != nil {
		return err
	}

	// Completed runs are immutable: edits must not disturb derived results.
	completedEdit, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 10.0,
			"observed_at":   verifiedAt(base, 1),
		}, ptrInt64(completed.Run.Version))
	if err != nil {
		return err
	}
	if err := requireCode(completedEdit, 409, string(domain.CodeRunState),
		"reject edit of completed run"); err != nil {
		return err
	}

	listResponse, err := api.call("GET", "/v1/runs", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listResponse, 200, "list runs"); err != nil {
		return err
	}
	var list struct {
		Runs []runView `json:"runs"`
	}
	if err := listResponse.decode(&list); err != nil {
		return err
	}
	for _, item := range list.Runs {
		if item.ID == started.Run.ID {
			if err := requireFinalizedExcursion(item); err != nil {
				return err
			}
			return nil
		}
	}
	return errInvalidWorkflow("completed run missing from listing")
}

func abortExcursionFreeze(api *client) error {
	formula, err := ensureApprovedFormula(api, "Abort Formula")
	if err != nil {
		return err
	}
	vessel, err := createCheckVessel(api, "VSL-203", 300)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-ABORT-TEMP")
	if err != nil {
		return err
	}

	readResponse, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(readResponse, 200, "read started run"); err != nil {
		return err
	}
	var run runView
	if err := readResponse.decode(&run); err != nil {
		return err
	}
	if run.StartedAt == "" {
		return errInvalidWorkflow("started run missing start timestamp")
	}
	start, err := time.Parse(time.RFC3339, run.StartedAt)
	if err != nil {
		return err
	}

	first, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 24.0,
			"observed_at":   start,
		}, ptrInt64(run.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(first, 200, "append first observation"); err != nil {
		return err
	}
	second, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      2,
			"gravity":       1.040,
			"temperature_c": 24.0,
			"observed_at":   start.Add(time.Second),
		}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(second, 200, "append second observation"); err != nil {
		return err
	}

	// The open interval after the last observation stays open while active, so
	// let wall time advance before abort to verify the trailing interval is
	// captured when the derived summary is frozen.
	time.Sleep(3100 * time.Millisecond)

	abortResponse, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/abort",
		nil, ptrInt64(started.Run.Version+2))
	if err != nil {
		return err
	}
	if err := requireStatus(abortResponse, 200, "abort run"); err != nil {
		return err
	}
	var aborted pairView
	if err := abortResponse.decode(&aborted); err != nil {
		return err
	}
	if err := requireRunState(aborted.Run, domain.RunAborted); err != nil {
		return err
	}
	if aborted.Run.Temperature == nil ||
		!aborted.Run.Temperature.Exceeded ||
		aborted.Run.Temperature.Count != 2 ||
		aborted.Run.Temperature.FinalizedAt == nil {
		return errInvalidWorkflow(fmt.Sprintf(
			"aborted run did not freeze excursion summary: %+v", aborted.Run.Temperature))
	}
	if aborted.Run.Temperature.DurationSecs < 3 {
		return errInvalidWorkflow(fmt.Sprintf(
			"aborted run missing trailing excursion duration: %+v",
			aborted.Run.Temperature))
	}
	if err := requireVesselState(aborted.Vessel, domain.VesselCleaning); err != nil {
		return err
	}

	postAbortEdit, err := api.call("PUT", "/v1/runs/"+started.Run.ID+"/observations",
		map[string]any{
			"sequence":      1,
			"gravity":       1.050,
			"temperature_c": 18.0,
			"observed_at":   start.Add(time.Second),
		}, ptrInt64(aborted.Run.Version))
	if err != nil {
		return err
	}
	return requireCode(postAbortEdit, 409, string(domain.CodeRunState),
		"reject edit of aborted run")
}

func requireObservationLimits(
	observations []domain.GravityObservation,
	limit float64,
) error {
	for _, observation := range observations {
		over := observation.TemperatureC > limit
		if observation.LimitC != limit || observation.TemperatureOver != over {
			return errInvalidWorkflow(fmt.Sprintf(
				"observation %d has wrong excursion mark (limit %v, over %v)",
				observation.Sequence, observation.LimitC, observation.TemperatureOver))
		}
	}
	return nil
}

func requireFinalizedExcursion(run runView) error {
	if run.Temperature == nil || run.Temperature.FinalizedAt == nil {
		return errInvalidWorkflow("run is missing a finalized temperature excursion summary")
	}
	if run.Metrics == nil {
		return errInvalidWorkflow("run is missing final metrics")
	}
	if run.Metrics.ExceededCount != run.Temperature.Count ||
		run.Metrics.ExceededDurationSecs != run.Temperature.DurationSecs ||
		run.Metrics.TemperatureExceeded != run.Temperature.Exceeded ||
		run.Metrics.TemperatureLimitC != run.Temperature.LimitC {
		return errInvalidWorkflow(fmt.Sprintf(
			"final metrics %+v disagree with excursion summary %+v",
			run.Metrics, run.Temperature))
	}
	return nil
}

func createCheckVessel(api *client, code string, capacity float64) (vesselView, error) {
	response, err := api.call("POST", "/v1/vessels", map[string]any{
		"code":            code,
		"capacity_liters": capacity,
	}, nil)
	if err != nil {
		return vesselView{}, err
	}
	if err := requireStatus(response, 201, "create check vessel"); err != nil {
		return vesselView{}, err
	}
	var vessel vesselView
	if err := response.decode(&vessel); err != nil {
		return vesselView{}, err
	}
	return vessel, nil
}

func createStartedRun(
	api *client,
	formulaID string,
	vesselID string,
	code string,
) (pairView, error) {
	runResponse, err := api.call("POST", "/v1/runs",
		createRunPayload(code, formulaID), nil)
	if err != nil {
		return pairView{}, err
	}
	if err := requireStatus(runResponse, 201, "create check run"); err != nil {
		return pairView{}, err
	}
	var run runView
	if err := runResponse.decode(&run); err != nil {
		return pairView{}, err
	}
	reserveResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/reserve",
		map[string]any{"vessel_id": vesselID}, ptrInt64(run.Version))
	if err != nil {
		return pairView{}, err
	}
	if err := requireStatus(reserveResponse, 200, "reserve check vessel"); err != nil {
		return pairView{}, err
	}
	var reserved pairView
	if err := reserveResponse.decode(&reserved); err != nil {
		return pairView{}, err
	}
	startResponse, err := api.call("POST", "/v1/runs/"+run.ID+"/start",
		nil, ptrInt64(reserved.Run.Version))
	if err != nil {
		return pairView{}, err
	}
	if err := requireStatus(startResponse, 200, "start check run"); err != nil {
		return pairView{}, err
	}
	var started pairView
	if err := startResponse.decode(&started); err != nil {
		return pairView{}, err
	}
	return started, nil
}
