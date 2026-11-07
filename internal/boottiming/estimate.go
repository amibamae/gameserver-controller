// Package boottiming provides local, advisory boot-time estimates.
//
// It deliberately contains no persistence or provider code so callers can use
// it with local history without sharing that history with a hosted service.
package boottiming

import (
	"errors"
	"math"
	"math/bits"
	"sort"
	"time"
)

const (
	// DefaultSafetyFactor gives the estimate headroom for variation in startup
	// time while keeping the configured multiplier visible to callers.
	DefaultSafetyFactor = 1.25

	maxDuration = time.Duration(1<<63 - 1)
)

var (
	ErrNoDurations         = errors.New("boot-time estimate requires at least one duration")
	ErrInvalidDuration     = errors.New("boot-time samples must be positive durations")
	ErrInvalidSafetyFactor = errors.New("boot-time safety factor must be finite and at least 1")
	ErrEstimateOverflow    = errors.New("boot-time estimate exceeds the maximum duration")
)

// Estimate returns the nearest-rank 75th percentile of successful boot
// durations multiplied by safetyFactor, rounded up to the next nanosecond.
// The input slice is not modified. All samples must be positive; callers
// should omit failed, incomplete, or corrupt observations before calling.
// A safety factor of 1 preserves the selected sample exactly.
func Estimate(durations []time.Duration, safetyFactor float64) (time.Duration, error) {
	if len(durations) == 0 {
		return 0, ErrNoDurations
	}
	if math.IsNaN(safetyFactor) || math.IsInf(safetyFactor, 0) || safetyFactor < 1 {
		return 0, ErrInvalidSafetyFactor
	}

	samples := append([]time.Duration(nil), durations...)
	for _, duration := range samples {
		if duration <= 0 {
			return 0, ErrInvalidDuration
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })

	// Nearest-rank percentile: ceil(0.75*n), expressed without a potentially
	// overflowing multiplication. The rank is one-based.
	rank := len(samples) - len(samples)/4
	p75 := samples[rank-1]
	if safetyFactor == 1 {
		return p75, nil
	}

	scaled := float64(p75) * safetyFactor
	maxAsFloat := float64(maxDuration)
	if math.IsInf(scaled, 0) || math.IsNaN(scaled) || scaled >= maxAsFloat {
		return 0, ErrEstimateOverflow
	}
	rounded := math.Ceil(scaled)
	if rounded < 1 || rounded >= maxAsFloat {
		return 0, ErrEstimateOverflow
	}
	return time.Duration(rounded), nil
}

// ProgressPercent returns an advisory estimate of startup progress. It is
// clamped to [0, 99] until ready is true, at which point it returns 100.
// Invalid or unavailable estimates report 0 before readiness. Once elapsed
// time reaches the estimate, the display remains at 99 until readiness is
// confirmed; callers must not treat this value as a readiness signal.
func ProgressPercent(elapsed, estimate time.Duration, ready bool) int {
	if ready {
		return 100
	}
	if elapsed <= 0 || estimate <= 0 {
		return 0
	}
	if elapsed >= estimate {
		return 99
	}

	// Compute floor(elapsed*100/estimate) without overflowing time.Duration.
	hi, lo := bits.Mul64(uint64(elapsed), 100)
	percent, _ := bits.Div64(hi, lo, uint64(estimate))
	if percent > 99 {
		return 99
	}
	return int(percent)
}
