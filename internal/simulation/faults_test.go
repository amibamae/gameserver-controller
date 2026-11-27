package simulation

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestFaultPlanFiresScheduledAttemptsOnce(t *testing.T) {
	p := NewFaultPlan()
	mustSchedule(t, p, "vultr.CreateInstance", 2, Timeout())
	mustSchedule(t, p, "vultr.CreateInstance", 3, NetworkError("connection reset"))
	mustSchedule(t, p, "wasabi.PutObject", 1, APIError(503, "slow down"))

	want := []string{
		"",
		"simulation: vultr.CreateInstance attempt 2: injected timeout",
		"simulation: vultr.CreateInstance attempt 3: injected network error: connection reset",
		"",
	}
	for i, w := range want {
		if got := errText(p.Next("vultr.CreateInstance")); got != w {
			t.Errorf("attempt %d error = %q, want %q", i+1, got, w)
		}
	}
	if got := errText(p.Next("wasabi.PutObject")); got != "simulation: wasabi.PutObject attempt 1: injected API error 503: slow down" {
		t.Errorf("wasabi error = %q", got)
	}
	if p.Attempts("vultr.CreateInstance") != 4 || p.Attempts("wasabi.PutObject") != 1 || p.Attempts("unused") != 0 {
		t.Errorf("attempts = %d, %d, %d", p.Attempts("vultr.CreateInstance"), p.Attempts("wasabi.PutObject"), p.Attempts("unused"))
	}
	if len(p.Pending()) != 0 {
		t.Errorf("Pending = %v, want none", p.Pending())
	}
}

func TestFaultErrorsWrapKindSentinels(t *testing.T) {
	cases := []struct {
		fault    Fault
		sentinel error
	}{
		{Timeout(), ErrTimeout},
		{NetworkError("refused"), ErrNetwork},
		{APIError(429, "rate limited"), ErrAPI},
	}
	for _, tc := range cases {
		p := NewFaultPlan()
		mustSchedule(t, p, "call", 1, tc.fault)
		err := p.Next("call")
		var fe *FaultError
		if !errors.As(err, &fe) || fe.Call != "call" || fe.Attempt != 1 || fe.Fault != tc.fault {
			t.Errorf("Next = %#v, want *FaultError for %v", err, tc.fault)
		}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("errors.Is(%v, %v) = false", err, tc.sentinel)
		}
		if isDeadline := errors.Is(err, context.DeadlineExceeded); isDeadline != (tc.sentinel == ErrTimeout) {
			t.Errorf("errors.Is(%v, DeadlineExceeded) = %t", err, isDeadline)
		}
	}
}

func TestFaultPlanScheduleRejectsInvalidFaults(t *testing.T) {
	p := NewFaultPlan()
	mustSchedule(t, p, "dup", 1, Timeout())
	cases := []struct {
		call    string
		attempt int
		fault   Fault
		want    string
	}{
		{"", 1, Timeout(), "simulation: fault call name is empty"},
		{"call", 0, Timeout(), `simulation: fault for "call" attempt must be at least 1, got 0`},
		{"call", 1, APIError(399, "x"), "simulation: API fault status 399 is outside 400-599"},
		{"call", 1, APIError(600, "x"), "simulation: API fault status 600 is outside 400-599"},
		{"call", 1, Fault{}, "simulation: unknown fault kind 0"},
		{"dup", 1, NetworkError("x"), "simulation: fault for dup#1 is already scheduled"},
	}
	for _, tc := range cases {
		if got := errText(p.Schedule(tc.call, tc.attempt, tc.fault)); got != tc.want {
			t.Errorf("Schedule(%q, %d) = %q, want %q", tc.call, tc.attempt, got, tc.want)
		}
	}
	if got := p.Pending(); !slices.Equal(got, []CallAttempt{{"dup", 1}}) {
		t.Fatalf("Pending = %v, want only dup#1", got)
	}
}

func TestFaultPlanPendingSortsByCallThenAttempt(t *testing.T) {
	p := NewFaultPlan()
	mustSchedule(t, p, "b", 10, Timeout())
	mustSchedule(t, p, "b", 2, Timeout())
	mustSchedule(t, p, "a", 5, Timeout())
	want := []CallAttempt{{"a", 5}, {"b", 2}, {"b", 10}}
	if got := p.Pending(); !slices.Equal(got, want) {
		t.Fatalf("Pending = %v, want %v", got, want)
	}
	if got := want[2].String(); got != "b#10" {
		t.Fatalf("String = %q, want b#10", got)
	}
}

func mustSchedule(t *testing.T, p *FaultPlan, call string, attempt int, fault Fault) {
	t.Helper()
	if err := p.Schedule(call, attempt, fault); err != nil {
		t.Fatalf("Schedule(%q, %d): %v", call, attempt, err)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
