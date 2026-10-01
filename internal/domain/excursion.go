package domain

import (
	"math"
	"time"
)

// TemperatureExcursion is the derived temperature-excursion summary for a run.
// It is recomputed whenever observations are appended or edited, and frozen
// (FinalizedAt is set) when fermentation ends: on conditioning entry, on
// completion, or on abort. Every value is derived from the limit snapshotted
// onto each observation at write time, so a later formula threshold change
// cannot rewrite run history.
type TemperatureExcursion struct {
	LimitC            float64    `json:"limit_c"`
	Exceeded          bool       `json:"exceeded"`
	ExceededCount     int        `json:"exceeded_observation_count"`
	ExceededDurationS int64      `json:"exceeded_duration_seconds"`
	FinalizedAt       *time.Time `json:"finalized_at,omitempty"`
}

// SummarizeTemperatureExcursions derives the excursion summary from ordered
// observations. The temperature state recorded at an observation is assumed to
// persist until the next observation; when finalizeAt is supplied (run
// completion or abort) the state at the last observation persists until then.
// The existing FinalizedAt value is preserved so a finalized summary keeps its
// freeze timestamp on every recompute path.
func SummarizeTemperatureExcursions(
	observations []GravityObservation,
	finalizeAt *time.Time,
	existing *TemperatureExcursion,
) *TemperatureExcursion {
	if len(observations) == 0 {
		return nil
	}
	summary := &TemperatureExcursion{}
	if existing != nil && existing.FinalizedAt != nil {
		stamp := existing.FinalizedAt.UTC()
		summary.FinalizedAt = &stamp
	} else if finalizeAt != nil {
		stamp := finalizeAt.UTC()
		summary.FinalizedAt = &stamp
	}
	var total time.Duration
	for index := range observations {
		current := observations[index]
		if current.TemperatureC > current.LimitC {
			summary.Exceeded = true
			summary.ExceededCount++
			var end time.Time
			if index+1 < len(observations) {
				end = observations[index+1].ObservedAt
			} else if summary.FinalizedAt != nil {
				end = *summary.FinalizedAt
			}
			if end.After(current.ObservedAt) {
				total += end.Sub(current.ObservedAt)
			}
		}
		// Observations are ordered; the latest limit is the one that would
		// apply to the next write.
		summary.LimitC = current.LimitC
	}
	if seconds := total.Seconds(); seconds > 0 {
		summary.ExceededDurationS = int64(math.Trunc(seconds))
	}
	return summary
}
