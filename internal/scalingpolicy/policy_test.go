package scalingpolicy

import (
	"testing"
	"time"
)

var created = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func at(minutes float64) time.Time {
	return created.Add(time.Duration(minutes * float64(time.Minute)))
}

func mustResolve(t *testing.T, settings Settings) Policy {
	t.Helper()
	policy, err := Resolve(settings)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func TestDefaultsUseMinutes58To59OfEveryHour(t *testing.T) {
	policy := mustResolve(t, Settings{})
	if policy.BillingIncrement != time.Hour || policy.WindowStart != 58*time.Minute || policy.WindowEnd != 59*time.Minute || !policy.BillingAware {
		t.Fatalf("defaults = %+v", policy)
	}
	cases := map[float64]bool{0: false, 30: false, 57.9: false, 58: true, 58.5: true, 59: false, 60: false, 118: true, 118.99: true, 119: false, 178.2: true}
	for minute, want := range cases {
		if got := policy.InWindow(created, at(minute)); got != want {
			t.Errorf("minute %v: InWindow = %v, want %v", minute, got, want)
		}
	}
	start, end := policy.Window(created, at(59.5))
	if !start.Equal(at(118)) || !end.Equal(at(119)) {
		t.Fatalf("after the window closes, the next one is in the next hour: %v-%v", start, end)
	}
}

func TestScaleDownWaitsForTheWindowAfterIdleTime(t *testing.T) {
	policy := mustResolve(t, Settings{})
	idle := 30 * time.Minute

	if decision := policy.ScaleDown(created, at(40), time.Time{}, idle); decision.Allowed || decision.Reason != "server_has_players" {
		t.Fatalf("server with players: %+v", decision)
	}
	// Idle since minute 10: idle long enough at 40, but the paid hour runs
	// to 60, so wait for minute 58.
	decision := policy.ScaleDown(created, at(40), at(10), idle)
	if decision.Allowed || !decision.NotBefore.Equal(at(58)) {
		t.Fatalf("idle but early: %+v", decision)
	}
	if decision := policy.ScaleDown(created, at(58.2), at(10), idle); !decision.Allowed {
		t.Fatalf("in window: %+v", decision)
	}
	// Idle since minute 40: not idle long enough by the first window, so
	// the next chance is minute 118.
	decision = policy.ScaleDown(created, at(58.2), at(40), idle)
	if decision.Allowed || !decision.NotBefore.Equal(at(118)) {
		t.Fatalf("not idle long enough: %+v", decision)
	}
	// Idle time ends inside a window: check again exactly then.
	decision = policy.ScaleDown(created, at(50), at(28.5), idle)
	if decision.Allowed || !decision.NotBefore.Equal(at(58.5)) {
		t.Fatalf("idle ends in window: %+v", decision)
	}
}

func TestScaleUpUsesTheRunningServersWindow(t *testing.T) {
	policy := mustResolve(t, Settings{})
	if decision := policy.ScaleUp(false, time.Time{}, at(5)); !decision.Allowed {
		t.Fatalf("cold start must be immediate: %+v", decision)
	}
	if decision := policy.ScaleUp(true, created, at(20)); decision.Allowed || !decision.NotBefore.Equal(at(58)) {
		t.Fatalf("adding capacity mid-hour: %+v", decision)
	}
	if decision := policy.ScaleUp(true, created, at(58.1)); !decision.Allowed {
		t.Fatalf("in window: %+v", decision)
	}
}

func TestCheapServersIgnoreTheBillingWindow(t *testing.T) {
	cheap := mustResolve(t, Settings{HourlyCost: "0.004", IgnoreBillingBelowHourlyCost: "0.01"})
	if cheap.BillingAware {
		t.Fatal("a server below the threshold must not wait for the window")
	}
	if decision := cheap.ScaleDown(created, at(40), at(5), 30*time.Minute); !decision.Allowed {
		t.Fatalf("cheap server: %+v", decision)
	}
	if decision := cheap.ScaleUp(true, created, at(20)); !decision.Allowed {
		t.Fatalf("cheap server scale up: %+v", decision)
	}
	pricey := mustResolve(t, Settings{HourlyCost: "0.036", IgnoreBillingBelowHourlyCost: "0.01"})
	if !pricey.BillingAware {
		t.Fatal("a server above the threshold must use the window")
	}
	equal := mustResolve(t, Settings{HourlyCost: "0.01", IgnoreBillingBelowHourlyCost: "0.01"})
	if !equal.BillingAware {
		t.Fatal("the threshold is exclusive")
	}
}

func TestCustomWindowAndIncrement(t *testing.T) {
	policy := mustResolve(t, Settings{BillingIncrement: "30m", DecisionWindowStart: "25m", DecisionWindowEnd: "28m"})
	if !policy.InWindow(created, at(55)) || policy.InWindow(created, at(29)) {
		t.Fatal("custom window not applied")
	}
}

func TestResolveRejectsInvalidSettings(t *testing.T) {
	for name, settings := range map[string]Settings{
		"window ends before it starts": {DecisionWindowStart: "59m", DecisionWindowEnd: "58m"},
		"window past increment":        {DecisionWindowEnd: "61m"},
		"tiny increment":               {BillingIncrement: "10s"},
		"bad duration":                 {BillingIncrement: "an hour"},
		"threshold without cost":       {IgnoreBillingBelowHourlyCost: "0.01"},
		"bad cost":                     {HourlyCost: "$0.03"},
		"too many decimals":            {HourlyCost: "0.0000001"},
		"negative cost":                {HourlyCost: "-1", IgnoreBillingBelowHourlyCost: "0.01"},
	} {
		if _, err := Resolve(settings); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
