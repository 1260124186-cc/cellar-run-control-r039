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
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	FormulaID      string     `json:"formula_id"`
	VesselID       *string    `json:"vessel_id,omitempty"`
	PlannedVolumeL float64    `json:"planned_volume_liters"`
	Notes          string     `json:"notes,omitempty"`
	State          RunState   `json:"state"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	EndedAt        *time.Time `json:"ended_at,omitempty"`
	// MaxTempLimitC captures the approved formula's maximum fermentation
	// temperature when fermentation starts. All excursion results derive
	// from this captured value, so later formula edits never reach a run.
	MaxTempLimitC *float64             `json:"max_temp_limit_c,omitempty"`
	Observations  []GravityObservation `json:"observations"`
	// TemperatureExcursion is the persisted derived breach summary, shared
	// by run details, the run list, and the final completion metrics.
	TemperatureExcursion *TemperatureExcursion `json:"temperature_excursion,omitempty"`
	Metrics              *RunMetrics           `json:"metrics,omitempty"`
	Version              int64                 `json:"version"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
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
	if r.MaxTempLimitC != nil {
		value := *r.MaxTempLimitC
		cloned.MaxTempLimitC = &value
	}
	if r.TemperatureExcursion != nil {
		value := *r.TemperatureExcursion
		if value.OpenStartedAt != nil {
			opened := *value.OpenStartedAt
			value.OpenStartedAt = &opened
		}
		cloned.TemperatureExcursion = &value
	}
	if r.Metrics != nil {
		value := *r.Metrics
		cloned.Metrics = &value
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

func (r *FermentationRun) StartFermentation(maxTempLimitC float64, now time.Time) error {
	if r.State != RunReserved || r.VesselID == nil {
		return NewError(CodeRunState, "only a reserved run can start fermentation")
	}
	stamp := now.UTC()
	r.State = RunFermenting
	r.StartedAt = &stamp
	if r.MaxTempLimitC == nil {
		limit := maxTempLimitC
		r.MaxTempLimitC = &limit
	}
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

// captureTempLimit records the formula limit for runs that already had
// observations or an in-flight fermentation state before it was captured.
// Once captured the limit never changes, so later formula edits cannot
// retroactively alter this run's derived results.
func (r *FermentationRun) captureTempLimit(maxTempLimitC float64) {
	if r.MaxTempLimitC == nil {
		limit := maxTempLimitC
		r.MaxTempLimitC = &limit
	}
}

// refreshTemperatureExcursion recomputes the persisted breach summary from
// the current observations. endAt closes and finalizes a still-open breach
// only when the run terminates.
func (r *FermentationRun) refreshTemperatureExcursion(endAt *time.Time) {
	if r.MaxTempLimitC == nil {
		return
	}
	r.TemperatureExcursion = EvaluateTemperatureExcursions(
		r.Observations, *r.MaxTempLimitC, endAt)
}

func (r *FermentationRun) AddObservation(
	observation GravityObservation,
	maxTempLimitC float64,
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
	r.captureTempLimit(maxTempLimitC)
	observation.RunID = r.ID
	observation.TempOverLimit = observation.TemperatureC > *r.MaxTempLimitC
	r.Observations = append(r.Observations, observation)
	r.refreshTemperatureExcursion(nil)
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
	r.State = RunConditioning
	r.UpdatedAt = now.UTC()
	r.Version++
	return nil
}

func (r *FermentationRun) Complete(
	originalGravity float64,
	maxTempLimitC float64,
	now time.Time,
) error {
	if r.State != RunConditioning {
		return NewError(CodeRunState, "only a conditioning run can be completed")
	}
	if len(r.Observations) < 3 {
		return NewError(CodeRunState,
			"completion requires at least three observations")
	}
	r.captureTempLimit(maxTempLimitC)
	stamp := now.UTC()
	// Finalize the breach summary at completion so any breach still open at
	// the last observation is closed at the termination time and the result
	// becomes part of the immutable final metrics.
	r.refreshTemperatureExcursion(&stamp)
	final := r.Observations[len(r.Observations)-1]
	metrics := CalculateMetrics(originalGravity, final.Gravity, len(r.Observations))
	if r.TemperatureExcursion != nil {
		metrics.TempLimitC = r.TemperatureExcursion.LimitC
		metrics.TempBreachCount = r.TemperatureExcursion.BreachCount
		metrics.TempExcursionSeconds = r.TemperatureExcursion.TotalSeconds
	}
	r.Metrics = &metrics
	r.State = RunCompleted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r *FermentationRun) Abort(maxTempLimitC float64, now time.Time) error {
	if r.State == RunCompleted || r.State == RunAborted {
		return NewError(CodeRunState, "completed or aborted runs cannot be aborted")
	}
	stamp := now.UTC()
	if r.HadStarted() {
		r.captureTempLimit(maxTempLimitC)
		// A terminated run must not report a still-open breach: close it at
		// the abort time so details and any later metrics view stay aligned.
		r.refreshTemperatureExcursion(&stamp)
	}
	r.State = RunAborted
	r.EndedAt = &stamp
	r.UpdatedAt = stamp
	r.Version++
	return nil
}

func (r FermentationRun) HadStarted() bool {
	return r.StartedAt != nil
}
