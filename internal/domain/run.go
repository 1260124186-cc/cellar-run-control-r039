package domain

import "time"

type RunState string

const (
	RunPlanned      RunState = "planned"
	RunReserved     RunState = "reserved"
	RunFermenting   RunState = "fermenting"
	RunConditioning RunState = "conditioning"
	RunCompleted    RunState = "completed"
	RunAborted      RunState = "aborted"
)

type FermentationRun struct {
	ID             string                `json:"id"`
	Code           string                `json:"code"`
	FormulaID      string                `json:"formula_id"`
	VesselID       *string               `json:"vessel_id,omitempty"`
	PlannedVolumeL float64               `json:"planned_volume_liters"`
	Notes          string                `json:"notes,omitempty"`
	State          RunState              `json:"state"`
	StartedAt      *time.Time            `json:"started_at,omitempty"`
	EndedAt        *time.Time            `json:"ended_at,omitempty"`
	Observations   []GravityObservation  `json:"observations"`
	Temperature    *TemperatureExcursion `json:"temperature_excursion,omitempty"`
	Metrics        *RunMetrics           `json:"metrics,omitempty"`
	Version        int64                 `json:"version"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

func (r FermentationRun) Clone() FermentationRun {
	cloned := r
	cloned.Observations = append([]GravityObservation(nil), r.Observations...)
	if r.VesselID != nil {
		value := *r.VesselID
		cloned.VesselID = &value
	}
	if r.StartedAt != nil {
		value := *r.StartedAt
		cloned.StartedAt = &value
	}
	if r.EndedAt != nil {
		value := *r.EndedAt
		cloned.EndedAt = &value
	}
	if r.Metrics != nil {
		value := *r.Metrics
		cloned.Metrics = &value
	}
	if r.Temperature != nil {
		value := *r.Temperature
		cloned.Temperature = &value
	}
	return cloned
}

func (r FermentationRun) Validate() error {
	if err := ValidateRunCode(r.Code, "code"); err != nil {
		return err
	}
	if r.PlannedVolumeL <= 0 {
		return FieldError(CodeInvalidInput, "planned_volume_liters",
			"must be greater than zero")
	}
	if len([]rune(r.Notes)) > 1000 {
		return FieldError(CodeInvalidInput, "notes",
			"must not exceed 1000 characters")
	}
	return nil
}

func (r *FermentationRun) ReserveVessel(vesselID string, now time.Time) error {
	if r.State != RunPlanned {
		return NewError(CodeRunState, "only a planned run can reserve a vessel")
	}
	value := vesselID
	r.VesselID = &value
	r.State = RunReserved
	r.UpdatedAt = now.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) StartFermentation(now time.Time) error {
	if r.State != RunReserved || r.VesselID == nil {
		return NewError(CodeRunState, "only a reserved run can start fermentation")
	}
	stamp := now.UTC()
	r.State = RunFermenting
	r.StartedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r *FermentationRun) AddObservation(
	observation GravityObservation,
	maxTempC float64,
) error {
	if r.State != RunFermenting || r.StartedAt == nil {
		return NewError(CodeRunState, "observations require a fermenting run")
	}
	if len(r.Observations) >= 500 {
		return NewError(CodeObservationConflict,
			"run cannot contain more than 500 observations")
	}
	var previous *GravityObservation
	if len(r.Observations) > 0 {
		value := r.Observations[len(r.Observations)-1]
		previous = &value
	}
	if err := ValidateObservation(*r.StartedAt, previous, observation, observation.ReceivedAt); err != nil {
		return err
	}
	expected := len(r.Observations) + 1
	if observation.Sequence != expected {
		return NewError(CodeObservationConflict,
			"observation sequence must be contiguous and begin at 1")
	}
	observation.RunID = r.ID
	observation.LimitC = maxTempC
	observation.TemperatureOver = observation.TemperatureC > maxTempC
	r.Observations = append(r.Observations, observation)
	r.Temperature = SummarizeTemperatureExcursions(r.Observations, nil, r.Temperature)
	r.UpdatedAt = observation.ReceivedAt.UTC()
	r.Version++
	return nil
}

// UpdateObservation revises an existing observation of a fermenting run. The
// formula limit in force now is snapshotted onto the revised observation and
// the derived excursion summary is recomputed synchronously; unedited
// observations keep the limit they were written with, so a threshold change
// never reaches into history.
func (r *FermentationRun) UpdateObservation(
	observation GravityObservation,
	maxTempC float64,
) error {
	if r.State != RunFermenting || r.StartedAt == nil {
		return NewError(CodeRunState, "observations can only be edited while fermenting")
	}
	if observation.Sequence < 1 || observation.Sequence > len(r.Observations) {
		return NewError(CodeObservationConflict,
			"observation sequence does not exist for this run")
	}
	index := observation.Sequence - 1
	var previous *GravityObservation
	if index > 0 {
		value := r.Observations[index-1]
		previous = &value
	}
	var next *GravityObservation
	if index+1 < len(r.Observations) {
		value := r.Observations[index+1]
		next = &value
	}
	if err := ValidateObservationPosition(*r.StartedAt, previous, next,
		observation, observation.ReceivedAt); err != nil {
		return err
	}
	observation.RunID = r.ID
	observation.LimitC = maxTempC
	observation.TemperatureOver = observation.TemperatureC > maxTempC
	r.Observations[index] = observation
	r.Temperature = SummarizeTemperatureExcursions(r.Observations, nil, r.Temperature)
	r.UpdatedAt = observation.ReceivedAt.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) BeginConditioning(now time.Time) error {
	if r.State != RunFermenting {
		return NewError(CodeRunState, "only a fermenting run can enter conditioning")
	}
	if len(r.Observations) < 2 {
		return NewError(CodeRunState,
			"conditioning requires at least two observations")
	}
	stamp := now.UTC()
	// Fermentation ends here, so the open interval after the last observation
	// closes at the conditioning instant. Observations can no longer be
	// appended or edited, which freezes the summary ahead of completion.
	r.Temperature = SummarizeTemperatureExcursions(r.Observations, &stamp, r.Temperature)
	r.State = RunConditioning
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r *FermentationRun) Complete(originalGravity float64, now time.Time) error {
	if r.State != RunConditioning {
		return NewError(CodeRunState, "only a conditioning run can be completed")
	}
	if len(r.Observations) < 3 {
		return NewError(CodeRunState,
			"completion requires at least three observations")
	}
	stamp := now.UTC()
	final := r.Observations[len(r.Observations)-1]
	r.Temperature = SummarizeTemperatureExcursions(r.Observations, &stamp, r.Temperature)
	metrics := CalculateMetrics(originalGravity, final.Gravity, len(r.Observations), r.Temperature)
	r.Metrics = &metrics
	r.State = RunCompleted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r *FermentationRun) Abort(now time.Time) error {
	if r.State == RunCompleted || r.State == RunAborted {
		return NewError(CodeRunState, "completed or aborted runs cannot be aborted")
	}
	stamp := now.UTC()
	// Freeze the derived excursion results at the abort instant, including the
	// open interval from the last observation.
	r.Temperature = SummarizeTemperatureExcursions(r.Observations, &stamp, r.Temperature)
	r.State = RunAborted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r FermentationRun) HadStarted() bool {
	return r.StartedAt != nil
}
