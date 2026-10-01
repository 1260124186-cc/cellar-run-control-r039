package domain

import (
	"math"
	"time"
)

// TemperatureExcursion is the derived summary of how long a run spent above
// the approved formula's maximum fermentation temperature. It is computed
// only from the run's captured temperature limit and its persisted
// observations, so changing a formula threshold later never rewrites it.
type TemperatureExcursion struct {
	LimitC        float64    `json:"limit_celsius"`
	BreachCount   int        `json:"breach_count"`
	TotalSeconds  int64      `json:"total_duration_seconds"`
	Open          bool       `json:"open"`
	OpenStartedAt *time.Time `json:"open_started_at,omitempty"`
	Finalized     bool       `json:"finalized"`
}

// EvaluateTemperatureExcursions marks every observation against the captured
// formula limit and accumulates the time spent above it.
//
// A breach begins at the first reading strictly above the limit and stays open
// until the next reading back at or below it: the full interval between those
// readings is charged to the breach. A breach that has not been closed by a
// later in-range reading remains open and contributes no elapsed time while
// the run is active. When endAt is non-nil (run completed or aborted), an open
// breach is closed at endAt and that trailing interval is charged; the summary
// is then finalized and stays immutable.
func EvaluateTemperatureExcursions(
	observations []GravityObservation,
	limitC float64,
	endAt *time.Time,
) *TemperatureExcursion {
	summary := &TemperatureExcursion{LimitC: limitC}

	var openSince *time.Time
	var total time.Duration
	for index := range observations {
		observation := &observations[index]
		breached := observation.TemperatureC > limitC
		observation.TempOverLimit = breached
		if breached {
			summary.BreachCount++
			if openSince == nil {
				opened := observation.ObservedAt.UTC()
				openSince = &opened
			}
			continue
		}
		if openSince != nil {
			total += observation.ObservedAt.UTC().Sub(*openSince)
			openSince = nil
		}
	}

	if openSince != nil {
		opened := *openSince
		summary.Open = true
		summary.OpenStartedAt = &opened
		if endAt != nil {
			closing := endAt.UTC()
			if closing.After(opened) {
				total += closing.Sub(opened)
			}
			summary.Open = false
			summary.OpenStartedAt = nil
			summary.Finalized = true
		}
	} else if endAt != nil {
		summary.Finalized = true
	}

	summary.TotalSeconds = int64(math.Round(total.Seconds()))
	if summary.TotalSeconds < 0 {
		summary.TotalSeconds = 0
	}
	return summary
}
