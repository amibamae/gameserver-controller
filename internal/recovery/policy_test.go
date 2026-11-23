package recovery

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func sample(now time.Time) Observation {
	return Observation{
		ServerID: "vanilla_no_grief", EnvironmentID: "staging",
		InstanceID: "existing-vultr-instance", UnitID: "minecraft-vanilla.service",
		ReleaseID: "paper-26.3-159", RuntimeID: "old-invocation-id",
		Health: "unhealthy", UnitState: "running",
		Reason: "fatal_current_invocation", ObservedAt: now,
	}
}

func TestInPlaceRestartPlanIsProtocolBoundAndNeverCreatesVM(t *testing.T) {
	now := time.Date(2026, 11, 23, 11, 0, 0, 0, time.UTC)
	before := sample(now.Add(-time.Minute))
	after := sample(now)
	plan, err := Propose(before, after, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"integration.proxy.backend.drain",
		"game.save.request",
		"execution.unit.restart",
		"execution.unit.inspect",
		"game.readiness.check",
		"integration.proxy.backend.register",
	}
	if !slices.Equal(plan.Steps, want) {
		t.Fatalf("recovery steps = %v; want %v", plan.Steps, want)
	}
	if plan.InstanceID != before.InstanceID || plan.PreviousRuntimeID != before.RuntimeID {
		t.Fatalf("recovery targeted a new instance: %#v", plan)
	}
	for _, step := range plan.Steps {
		if strings.HasPrefix(step, "hosting.") || strings.Contains(step, "destroy") {
			t.Fatalf("health recovery must not create or destroy a VM: %s", step)
		}
	}
}

func TestFailedProcessPlansStartNotUnsafeRestart(t *testing.T) {
	now := time.Now().UTC()
	before := sample(now.Add(-time.Minute))
	before.UnitState, before.Reason = "failed", "service_failed"
	after := before
	after.ObservedAt = now
	plan, err := Propose(before, after, now)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Steps, "execution.unit.restart") ||
		!slices.Contains(plan.Steps, "execution.unit.start") {
		t.Fatalf("failed process must be started through a durable operation: %v", plan.Steps)
	}
}

func TestUnknownStaleOrMismatchedEvidenceCannotRestart(t *testing.T) {
	now := time.Now().UTC()
	changes := []struct {
		name string
		edit func(*Observation, *Observation)
	}{
		{"unknown health", func(_, b *Observation) { b.Health = "unknown" }},
		{"healthy", func(_, b *Observation) { b.Health = "healthy" }},
		{"different VM", func(_, b *Observation) { b.InstanceID = "replacement" }},
		{"different runtime", func(_, b *Observation) { b.RuntimeID = "new" }},
		{"different release", func(_, b *Observation) { b.ReleaseID = "another" }},
		{"unknown original identity", func(a, _ *Observation) { a.UnitID = "" }},
		{"different failure", func(_, b *Observation) { b.Reason = "other" }},
		{"stale evidence", func(a, _ *Observation) { a.ObservedAt = now.Add(-10 * time.Minute) }},
		{"future evidence", func(_, b *Observation) { b.ObservedAt = now.Add(time.Minute) }},
		{"same timestamp", func(a, b *Observation) { a.ObservedAt = b.ObservedAt }},
		{"stopped deliberately", func(a, b *Observation) { a.UnitState, b.UnitState = "stopped", "stopped" }},
	}
	for _, tt := range changes {
		t.Run(tt.name, func(t *testing.T) {
			before, after := sample(now.Add(-time.Minute)), sample(now)
			tt.edit(&before, &after)
			if _, err := Propose(before, after, now); !errors.Is(err, ErrInsufficientEvidence) {
				t.Fatalf("unsafe evidence accepted: %v", err)
			}
		})
	}
}

func TestReRegisterOnlyAfterFreshDoneOnNewRuntime(t *testing.T) {
	now := time.Date(2026, 11, 23, 11, 5, 0, 0, time.UTC)
	plan, err := Propose(sample(now.Add(-3*time.Minute)), sample(now.Add(-2*time.Minute)), now)
	if err != nil {
		t.Fatal(err)
	}
	ready := Readiness{
		InstanceID: plan.InstanceID, UnitID: plan.UnitID,
		ReleaseID: plan.ReleaseID, RuntimeID: "new-invocation-id",
		ReadyLogAt: now.Add(-time.Minute),
		ObservedAt: now,
		AllRequiredChecksPassed: true,
	}
	if !MayReregister(plan, ready, now) {
		t.Fatal("fresh readiness on new Paper runtime should allow re-registration")
	}
	checks := []struct {
		name string
		edit func(*Readiness)
	}{
		{"old process Done", func(r *Readiness) { r.RuntimeID = plan.PreviousRuntimeID }},
		{"no Done", func(r *Readiness) { r.ReadyLogAt = time.Time{} }},
		{"old Done", func(r *Readiness) { r.ReadyLogAt = plan.SampledAt }},
		{"unknown health", func(r *Readiness) { r.AllRequiredChecksPassed = false }},
		{"wrong VM", func(r *Readiness) { r.InstanceID = "replacement-vm" }},
		{"wrong unit", func(r *Readiness) { r.UnitID = "another.service" }},
		{"wrong release", func(r *Readiness) { r.ReleaseID = "paper-old" }},
		{"stale readiness", func(r *Readiness) { r.ObservedAt = now.Add(-3 * time.Minute) }},
		{"future readiness", func(r *Readiness) { r.ObservedAt = now.Add(time.Minute) }},
		{"not ready when observed", func(r *Readiness) { r.ReadyLogAt = now.Add(time.Second) }},
	}
	for _, tt := range checks {
		t.Run(tt.name, func(t *testing.T) {
			modified := ready
			tt.edit(&modified)
			if MayReregister(plan, modified, now) {
				t.Fatalf("invalid readiness allowed routing: %#v", modified)
			}
		})
	}
}
