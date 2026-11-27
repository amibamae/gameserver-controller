package simulation

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestSameSeedGivesSameRun(t *testing.T) {
	draw := func(seed uint64) []uint64 {
		s := New(Config{Seed: seed, Start: testStart})
		out := make([]uint64, 8)
		for i := range out {
			out[i] = s.Rand().Uint64()
		}
		return out
	}
	if a, b := draw(42), draw(42); !slices.Equal(a, b) {
		t.Fatalf("seed 42 runs differ: %v vs %v", a, b)
	}
	if a, b := draw(42), draw(43); slices.Equal(a, b) {
		t.Fatalf("seeds 42 and 43 gave the same run %v", a)
	}
	if NewRand(7).Uint64() != NewRand(7).Uint64() {
		t.Fatal("NewRand(7) is not deterministic")
	}
}

func TestSimWiresClockAndFaults(t *testing.T) {
	s := New(Config{Seed: 1, Start: testStart})
	if !s.Clock().Now().Equal(testStart) {
		t.Fatalf("Now = %s, want %s", s.Clock().Now(), testStart)
	}
	if err := s.Faults().Schedule("dns.Upsert", 1, Timeout()); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := s.Step("upsert", func() error { return s.Faults().Next("dns.Upsert") }); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Step = %v, want the injected timeout unchanged", err)
	}
	if err := s.Step("retry", func() error { return s.Faults().Next("dns.Upsert") }); err != nil {
		t.Fatalf("retry Step = %v, want nil", err)
	}
}

func TestSimStepInvariantFailureTakesPrecedence(t *testing.T) {
	s := New(Config{})
	broken := false
	if err := s.Invariant("not-broken", func() error {
		if broken {
			return errors.New("broken")
		}
		return nil
	}); err != nil {
		t.Fatalf("Invariant: %v", err)
	}
	err := s.Step("break", func() error { broken = true; return ErrNetwork })
	want := `simulation: invariant "not-broken" failed after step "break": broken`
	if errText(err) != want {
		t.Fatalf("Step = %q, want %q", errText(err), want)
	}
	if errors.Is(err, ErrNetwork) {
		t.Fatal("invariant failure should replace the action error")
	}
}

func TestSimAdvanceChecksInvariantsAfterEachTimer(t *testing.T) {
	s := New(Config{Start: testStart})
	count := 0
	var steps []string
	if err := s.Invariant("at-most-two", func() error {
		if count > 2 {
			return errors.New("count exceeded 2")
		}
		return nil
	}); err != nil {
		t.Fatalf("Invariant: %v", err)
	}
	if err := s.Invariant("record", func() error { steps = append(steps, s.Clock().Now().Sub(testStart).String()); return nil }); err != nil {
		t.Fatalf("Invariant: %v", err)
	}
	if _, err := s.Clock().NewTicker("increment", time.Second, func(time.Time) { count++ }); err != nil {
		t.Fatalf("NewTicker: %v", err)
	}
	if err := s.Advance(1500 * time.Millisecond); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !slices.Equal(steps, []string{"1s", "1.5s"}) {
		t.Fatalf("invariant ran at %v, want after the timer and at the end of Advance", steps)
	}
	err := s.Advance(time.Minute)
	want := `simulation: invariant "at-most-two" failed after step "increment": count exceeded 2`
	if errText(err) != want {
		t.Fatalf("Advance = %q, want %q", errText(err), want)
	}
	if got := s.Clock().Now(); !got.Equal(testStart.Add(3 * time.Second)) {
		t.Fatalf("clock stopped at %s, want the failing step at +3s", got)
	}
}

func TestSimAdvanceNamesEndOfAdvanceStep(t *testing.T) {
	s := New(Config{Start: testStart})
	if err := s.Invariant("deadline", func() error {
		if s.Clock().Now().After(testStart) {
			return errors.New("past deadline")
		}
		return nil
	}); err != nil {
		t.Fatalf("Invariant: %v", err)
	}
	err := s.Advance(time.Second)
	want := `simulation: invariant "deadline" failed after step "advance to 2026-01-02T03:04:06Z": past deadline`
	if errText(err) != want {
		t.Fatalf("Advance = %q, want %q", errText(err), want)
	}
}
