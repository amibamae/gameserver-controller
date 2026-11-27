package simulation

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

var testStart = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// recorder collects "name@offset" entries for fired callbacks.
type recorder struct {
	clock *Clock
	fired []string
}

func (r *recorder) fn(name string) func(time.Time) {
	return func(now time.Time) {
		r.fired = append(r.fired, fmt.Sprintf("%s@%s", name, now.Sub(testStart)))
	}
}

func mustTimer(t *testing.T) func(*Timer, error) *Timer {
	return func(timer *Timer, err error) *Timer {
		t.Helper()
		if err != nil {
			t.Fatalf("schedule: %v", err)
		}
		return timer
	}
}

func mustAdvance(t *testing.T, c *Clock, d time.Duration) {
	t.Helper()
	if err := c.Advance(d); err != nil {
		t.Fatalf("Advance(%s): %v", d, err)
	}
}

func assertFired(t *testing.T, r *recorder, want ...string) {
	t.Helper()
	if !slices.Equal(r.fired, want) {
		t.Fatalf("fired = %v, want %v", r.fired, want)
	}
}

func TestClockNowMovesOnlyOnAdvance(t *testing.T) {
	c := NewClock(testStart)
	if !c.Now().Equal(testStart) {
		t.Fatalf("Now = %s, want %s", c.Now(), testStart)
	}
	mustAdvance(t, c, 90*time.Second)
	if want := testStart.Add(90 * time.Second); !c.Now().Equal(want) {
		t.Fatalf("Now = %s, want %s", c.Now(), want)
	}
}

func TestClockTimersFireInDueOrderThenScheduleOrder(t *testing.T) {
	c := NewClock(testStart)
	r := &recorder{clock: c}
	mustTimer(t)(c.AfterFunc("late", 3*time.Second, r.fn("late")))
	mustTimer(t)(c.AfterFunc("tie-a", time.Second, r.fn("tie-a")))
	mustTimer(t)(c.AfterFunc("tie-b", time.Second, r.fn("tie-b")))
	mustTimer(t)(c.AfterFunc("now", -time.Second, r.fn("now")))
	mustAdvance(t, c, 0)
	assertFired(t, r, "now@0s")
	mustAdvance(t, c, 2*time.Second)
	assertFired(t, r, "now@0s", "tie-a@1s", "tie-b@1s")
	if c.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", c.Pending())
	}
	mustAdvance(t, c, time.Second)
	assertFired(t, r, "now@0s", "tie-a@1s", "tie-b@1s", "late@3s")
	if c.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", c.Pending())
	}
}

func TestClockTimerScheduledByCallbackFiresInSameAdvance(t *testing.T) {
	c := NewClock(testStart)
	r := &recorder{clock: c}
	mustTimer(t)(c.AfterFunc("first", time.Second, func(time.Time) {
		r.fired = append(r.fired, "first")
		mustTimer(t)(c.AfterFunc("second", time.Second, r.fn("second")))
	}))
	mustAdvance(t, c, 5*time.Second)
	assertFired(t, r, "first", "second@2s")
}

func TestClockTickerFiresEveryPeriodUntilStopped(t *testing.T) {
	c := NewClock(testStart)
	r := &recorder{clock: c}
	var ticker *Timer
	ticker = mustTimer(t)(c.NewTicker("tick", 10*time.Second, func(now time.Time) {
		r.fn("tick")(now)
		if len(r.fired) == 3 && !ticker.Stop() {
			t.Error("Stop inside callback = false, want true")
		}
	}))
	if ticker.Name() != "tick" {
		t.Fatalf("Name = %q, want tick", ticker.Name())
	}
	mustAdvance(t, c, 25*time.Second)
	assertFired(t, r, "tick@10s", "tick@20s")
	mustAdvance(t, c, time.Minute)
	assertFired(t, r, "tick@10s", "tick@20s", "tick@30s")
	if c.Pending() != 0 {
		t.Fatalf("Pending = %d, want 0", c.Pending())
	}
}

func TestTimerStopReportsWhetherScheduled(t *testing.T) {
	c := NewClock(testStart)
	r := &recorder{clock: c}
	stopped := mustTimer(t)(c.AfterFunc("stopped", time.Second, r.fn("stopped")))
	fired := mustTimer(t)(c.AfterFunc("fired", time.Second, r.fn("fired")))
	if !stopped.Stop() {
		t.Fatal("first Stop = false, want true")
	}
	if stopped.Stop() {
		t.Fatal("second Stop = true, want false")
	}
	mustAdvance(t, c, time.Second)
	assertFired(t, r, "fired@1s")
	if fired.Stop() {
		t.Fatal("Stop after firing = true, want false")
	}
}

func TestClockRejectsInvalidInput(t *testing.T) {
	c := NewClock(testStart)
	noop := func(time.Time) {}
	_, nilFn := c.AfterFunc("empty", time.Second, nil)
	_, zeroPeriod := c.NewTicker("zero", 0, noop)
	_, negPeriod := c.NewTicker("neg", -time.Second, noop)
	_, nilTick := c.NewTicker("empty-tick", time.Second, nil)
	cases := []struct {
		err  error
		want string
	}{
		{nilFn, `simulation: timer "empty" has no callback`},
		{zeroPeriod, `simulation: ticker "zero" period must be positive, got 0s`},
		{negPeriod, `simulation: ticker "neg" period must be positive, got -1s`},
		{nilTick, `simulation: timer "empty-tick" has no callback`},
		{c.Advance(-time.Nanosecond), "simulation: cannot advance by negative duration -1ns"},
	}
	for _, tc := range cases {
		if tc.err == nil || tc.err.Error() != tc.want {
			t.Errorf("error = %v, want %q", tc.err, tc.want)
		}
	}
	if c.Pending() != 0 || !c.Now().Equal(testStart) {
		t.Fatalf("rejected input changed the clock: pending %d, now %s", c.Pending(), c.Now())
	}
}

func TestClockRejectsReentrantAdvance(t *testing.T) {
	c := NewClock(testStart)
	var inner error
	mustTimer(t)(c.AfterFunc("reenter", time.Second, func(time.Time) { inner = c.Advance(time.Second) }))
	mustAdvance(t, c, time.Second)
	if inner == nil || inner.Error() != "simulation: Advance called from inside a timer callback" {
		t.Fatalf("inner Advance error = %v", inner)
	}
	mustAdvance(t, c, time.Second)
	if want := testStart.Add(2 * time.Second); !c.Now().Equal(want) {
		t.Fatalf("Now = %s, want %s", c.Now(), want)
	}
}
