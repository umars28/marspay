package risk

import (
	"fmt"
	"math"
)

const (
	MinHoldbackBps = 150
	MaxHoldbackBps = 4500

	CoverageMultiple = 200
	CentiBpsPerBps   = 100

	DisputeWeight = 0.25
	RefundWeight  = 0.02

	InstantScoreFloor = 40
)

type Factors struct {
	AgeDays          int
	RefundRateBps    int
	DisputeRateBps   int
	VolumeStability  int
	VerifiedBusiness bool
}

type Score struct {
	Total       int            `json:"total"`
	HoldbackBps int            `json:"holdback_bps"`
	Mode        string         `json:"mode"`
	Components  map[string]int `json:"components"`
}

func Evaluate(f Factors) (Score, error) {
	if f.RefundRateBps < 0 || f.DisputeRateBps < 0 {
		return Score{}, fmt.Errorf("risk: rates must not be negative")
	}
	if f.VolumeStability < 0 || f.VolumeStability > 25 {
		return Score{}, fmt.Errorf("risk: volume stability must be between 0 and 25")
	}

	components := map[string]int{
		"age":              ageScore(f.AgeDays),
		"refund_rate":      rateScore(f.RefundRateBps, 100),
		"dispute_rate":     rateScore(f.DisputeRateBps, 50),
		"volume_stability": f.VolumeStability,
	}

	total := 0
	for _, v := range components {
		total += v
	}
	if f.VerifiedBusiness {
		total += 5
	}
	total = clamp(total, 0, 100)

	holdback := HoldbackBps(expectedLossCentiBps(f))

	mode := "instant"
	if total < InstantScoreFloor {
		mode = "batch"
	}

	return Score{Total: total, HoldbackBps: holdback, Mode: mode, Components: components}, nil
}

func HoldbackBps(expectedLossCentiBps int) int {
	if expectedLossCentiBps < 0 {
		expectedLossCentiBps = 0
	}
	return clamp(expectedLossCentiBps*CoverageMultiple/CentiBpsPerBps,
		MinHoldbackBps, MaxHoldbackBps)
}

func expectedLossCentiBps(f Factors) int {
	loss := float64(f.DisputeRateBps)*DisputeWeight + float64(f.RefundRateBps)*RefundWeight

	switch {
	case f.AgeDays < 30:
		loss += 10
	case f.AgeDays < 90:
		loss += 4
	case f.AgeDays < 365:
		loss += 1
	}

	return int(math.Ceil(loss * CentiBpsPerBps))
}

func ageScore(days int) int {
	switch {
	case days >= 365:
		return 25
	case days >= 180:
		return 20
	case days >= 90:
		return 15
	case days >= 30:
		return 10
	default:
		return 4
	}
}

func rateScore(rateBps, tolerance int) int {
	if tolerance <= 0 {
		return 0
	}
	if rateBps >= tolerance*4 {
		return 0
	}
	score := 25 - (rateBps*25)/(tolerance*4)
	return clamp(score, 0, 25)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
