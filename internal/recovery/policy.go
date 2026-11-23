// Package recovery plans conservative recovery on an existing game server.
// It never calls hosting providers or dispatches mutations; lifecycle workflows
// must durably accept and authorize each planned step before execution.
package recovery

import (
	"errors"
	"time"
)

const (
	maxEvidenceAge  = 5 * time.Minute
	maxReadinessAge = 2 * time.Minute
)

// Observation identifies one game-health sample from an exact managed runtime.
// An unknown or missing field is not evidence permitting an automatic restart.
type Observation struct {
	ServerID      string
	EnvironmentID string
	InstanceID    string
	UnitID        string
	ReleaseID     string
	RuntimeID     string
	Health        string
	UnitState     string
	Reason        string
	ObservedAt    time.Time
}

// Plan is a sequence of existing protocol operations on one exact VM.
// These steps are advisory until the controller's durable workflow implements
// them; no provider create/destroy operation is ever returned.
type Plan struct {
	InstanceID        string
	UnitID            string
	ReleaseID         string
	PreviousRuntimeID string
	SampledAt         time.Time
	Steps             []string
}

var ErrInsufficientEvidence = errors.New("recovery requires two fresh, matching, confirmed failures on one managed runtime")

// Propose uses two observations from the same process to prevent an ambiguous
// transient check or old log line from triggering a game restart.
func Propose(before, after Observation, now time.Time) (Plan, error) {
	if !sameRuntime(before, after) || !freshFailures(before, after, now) {
		return Plan{}, ErrInsufficientEvidence
	}
	steps := []string{"integration.proxy.backend.drain"}
	switch {
	case after.UnitState == "running":
		// execution.unit.restart requires a confirmed game.save.request step
		// bound to the current runtime and a bounded graceful stop.
		steps = append(steps, "game.save.request", "execution.unit.restart")
	case after.UnitState == "failed" && after.Reason == "service_failed":
		// An already-dead process does not need to be killed or restarted.
		// Reconcile the logical lifecycle state before server.start.
		steps = append(steps, "execution.unit.start")
	default:
		return Plan{}, ErrInsufficientEvidence
	}
	steps = append(steps, "execution.unit.inspect", "game.readiness.check",
		"integration.proxy.backend.register")
	return Plan{
		InstanceID: after.InstanceID, UnitID: after.UnitID,
		ReleaseID: after.ReleaseID, PreviousRuntimeID: after.RuntimeID,
		SampledAt: after.ObservedAt, Steps: steps,
	}, nil
}

func sameRuntime(before, after Observation) bool {
	if before.ServerID == "" || before.EnvironmentID == "" || before.InstanceID == "" {
		return false
	}
	if before.UnitID == "" || before.ReleaseID == "" || before.RuntimeID == "" {
		return false
	}
	return before.ServerID == after.ServerID &&
		before.EnvironmentID == after.EnvironmentID &&
		before.InstanceID == after.InstanceID &&
		before.UnitID == after.UnitID &&
		before.ReleaseID == after.ReleaseID &&
		before.RuntimeID == after.RuntimeID
}

func freshFailures(before, after Observation, now time.Time) bool {
	if before.Health != "unhealthy" || after.Health != "unhealthy" {
		return false
	}
	if before.UnitState != after.UnitState || before.Reason != after.Reason {
		return false
	}
	if before.ObservedAt.IsZero() || after.ObservedAt.Before(before.ObservedAt) {
		return false
	}
	return !after.ObservedAt.After(now) &&
		now.Sub(before.ObservedAt) <= maxEvidenceAge &&
		after.ObservedAt.After(before.ObservedAt)
}

// Readiness is a game-adapter observation, not a proxy reachability probe.
// The caller must independently validate its source and all required checks.
type Readiness struct {
	InstanceID              string
	UnitID                  string
	ReleaseID               string
	RuntimeID               string
	ObservedAt              time.Time
	ReadyLogAt              time.Time
	AllRequiredChecksPassed bool
}

// MayReregister forbids proxy routing before the NEW runtime's fresh Done
// marker and exact game readiness checks. A legacy registration must stay
// drained while this is false.
func MayReregister(plan Plan, ready Readiness, now time.Time) bool {
	if plan.InstanceID == "" || plan.PreviousRuntimeID == "" {
		return false
	}
	if ready.InstanceID != plan.InstanceID || ready.UnitID != plan.UnitID {
		return false
	}
	if ready.ReleaseID != plan.ReleaseID || ready.RuntimeID == "" {
		return false
	}
	if ready.RuntimeID == plan.PreviousRuntimeID || !ready.AllRequiredChecksPassed {
		return false
	}
	if ready.ReadyLogAt.IsZero() || ready.ObservedAt.Before(ready.ReadyLogAt) {
		return false
	}
	if !ready.ReadyLogAt.After(plan.SampledAt) {
		return false
	}
	if ready.ObservedAt.After(now) || now.Sub(ready.ObservedAt) > maxReadinessAge {
		return false
	}
	return true
}
