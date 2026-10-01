package domain

import "math"

type RunMetrics struct {
	FinalGravity        float64 `json:"final_gravity"`
	ApparentAttenuation float64 `json:"apparent_attenuation_percent"`
	EstimatedAlcoholABV float64 `json:"estimated_alcohol_abv"`
	ObservationCount    int     `json:"observation_count"`
	// Finalized temperature-excursion results copied from the run summary at
	// completion, so the completed run reports one consistent set of figures.
	TemperatureLimitC    float64 `json:"temperature_limit_c"`
	TemperatureExceeded  bool    `json:"temperature_exceeded"`
	ExceededCount        int     `json:"temperature_exceeded_count"`
	ExceededDurationSecs int64   `json:"temperature_exceeded_duration_seconds"`
}

func CalculateMetrics(
	originalGravity, finalGravity float64,
	count int,
	excursion *TemperatureExcursion,
) RunMetrics {
	attenuation := 0.0
	if originalGravity > 1 {
		attenuation = ((originalGravity - finalGravity) / (originalGravity - 1)) * 100
	}
	alcohol := (originalGravity - finalGravity) * 131.25
	if alcohol < 0 {
		alcohol = 0
	}
	metrics := RunMetrics{
		FinalGravity:        round2(finalGravity),
		ApparentAttenuation: round2(attenuation),
		EstimatedAlcoholABV: round2(alcohol),
		ObservationCount:    count,
	}
	if excursion != nil {
		metrics.TemperatureLimitC = excursion.LimitC
		metrics.TemperatureExceeded = excursion.Exceeded
		metrics.ExceededCount = excursion.ExceededCount
		metrics.ExceededDurationSecs = excursion.ExceededDurationS
	}
	return metrics
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
