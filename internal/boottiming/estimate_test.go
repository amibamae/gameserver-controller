package boottiming

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestEstimateUsesNearestRankP75AndSafetyFactor(t *testing.T) {
	samples := []time.Duration{5 * time.Second, time.Second, 4 * time.Second, 2 * time.Second, 3 * time.Second}
	got, err := Estimate(samples, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if want := 6 * time.Second; got != want {
		t.Fatalf("Estimate() = %s, want %s", got, want)
	}
	if samples[0] != 5*time.Second || samples[1] != time.Second {
		t.Fatalf("Estimate() modified its input: %v", samples)
	}
}

func TestEstimateRoundsUpAndPreservesFactorOne(t *testing.T) {
	sample := time.Duration(10)
	got, err := Estimate([]time.Duration{sample}, 1.01)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Duration(11); got != want {
		t.Fatalf("Estimate() = %d ns, want %d ns", got, want)
	}

	got, err = Estimate([]time.Duration{sample}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != sample {
		t.Fatalf("Estimate() = %d ns, want exact sample %d ns", got, sample)
	}
}

func TestEstimateRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name   string
		values []time.Duration
		factor float64
		want   error
	}{
		{name: "no samples", factor: 1, want: ErrNoDurations},
		{name: "zero sample", values: []time.Duration{time.Second, 0}, factor: 1, want: ErrInvalidDuration},
		{name: "negative sample", values: []time.Duration{-time.Second}, factor: 1, want: ErrInvalidDuration},
		{name: "factor below one", values: []time.Duration{time.Second}, factor: 0.9, want: ErrInvalidSafetyFactor},
		{name: "NaN factor", values: []time.Duration{time.Second}, factor: math.NaN(), want: ErrInvalidSafetyFactor},
		{name: "infinite factor", values: []time.Duration{time.Second}, factor: math.Inf(1), want: ErrInvalidSafetyFactor},
		{name: "overflow", values: []time.Duration{time.Duration(1 << 62)}, factor: 3, want: ErrEstimateOverflow},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Estimate(test.values, test.factor)
			if !errors.Is(err, test.want) {
				t.Fatalf("Estimate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProgressPercentIsAdvisoryAndCappedUntilReady(t *testing.T) {
	cases := []struct {
		name     string
		elapsed  time.Duration
		estimate time.Duration
		ready    bool
		want     int
	}{
		{name: "zero elapsed", estimate: 10 * time.Second, want: 0},
		{name: "half", elapsed: 5 * time.Second, estimate: 10 * time.Second, want: 50},
		{name: "near estimate", elapsed: 999 * time.Millisecond, estimate: time.Second, want: 99},
		{name: "past estimate stays below ready", elapsed: 20 * time.Second, estimate: 10 * time.Second, want: 99},
		{name: "no estimate", elapsed: time.Second, want: 0},
		{name: "confirmed ready", elapsed: 20 * time.Second, estimate: time.Second, ready: true, want: 100},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := ProgressPercent(test.elapsed, test.estimate, test.ready); got != test.want {
				t.Fatalf("ProgressPercent() = %d, want %d", got, test.want)
			}
		})
	}
}
