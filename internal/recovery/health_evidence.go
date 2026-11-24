package recovery

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var ErrUnconfirmedHealth = errors.New("recovery trigger lacks two fresh, exact-runtime unhealthy observations")

// HealthBinding names the paired durable, read-only adapter observations.
// An operator-supplied boolean or a TCP failure cannot trigger recovery.
type HealthBinding struct {
	FirstUnitStepID  string
	FirstHealthStepID string
	SecondUnitStepID string
	SecondHealthStepID string
}

// CheckRecoveryTrigger verifies independent game observations and runtime
// identity before allowing the proxy to be drained for an in-place restart.
func CheckRecoveryTrigger(record store.OperationRecord, binding EvidenceBinding, health HealthBinding, now time.Time) (Plan, error) {
	if !validRecoveryBinding(record, binding) {
		return Plan{}, ErrUnconfirmedHealth
	}
	if health.FirstUnitStepID == "" || health.FirstHealthStepID == "" {
		return Plan{}, ErrUnconfirmedHealth
	}
	if health.SecondUnitStepID == "" || health.SecondHealthStepID == "" {
		return Plan{}, ErrUnconfirmedHealth
	}
	indices := []int{
		healthStepIndex(record, health.FirstUnitStepID),
		healthStepIndex(record, health.FirstHealthStepID),
		healthStepIndex(record, health.SecondUnitStepID),
		healthStepIndex(record, health.SecondHealthStepID),
		healthStepIndex(record, binding.DrainStepID),
	}
	for i, index := range indices {
		if index < 0 || (i > 0 && index <= indices[i-1]) {
			return Plan{}, ErrUnconfirmedHealth
		}
	}
	before, err := confirmedGameObservation(record, binding, health.FirstUnitStepID, health.FirstHealthStepID)
	if err != nil {
		return Plan{}, ErrUnconfirmedHealth
	}
	after, err := confirmedGameObservation(record, binding, health.SecondUnitStepID, health.SecondHealthStepID)
	if err != nil {
		return Plan{}, ErrUnconfirmedHealth
	}
	if before.ObservedAt.Before(record.CreatedAt) || after.ObservedAt.Before(record.CreatedAt) {
		return Plan{}, ErrUnconfirmedHealth
	}
	plan, err := Propose(before, after, now)
	if err != nil || len(plan.Steps) < 5 {
		return Plan{}, ErrUnconfirmedHealth
	}
	if plan.Steps[1] != "game.save.request" {
		return Plan{}, ErrUnconfirmedHealth
	}
	return plan, nil
}

func healthStepIndex(record store.OperationRecord, id string) int {
	for i, step := range record.Steps {
		if step.StepID == id {
			return i
		}
	}
	return -1
}

func confirmedGameObservation(record store.OperationRecord, binding EvidenceBinding, unitStepID, healthStepID string) (Observation, error) {
	unit, ok := succeededStep(record, unitStepID, "execution.unit.inspect")
	if !ok {
		return Observation{}, ErrUnconfirmedHealth
	}
	runtimeID, inspectedAt, err := checkedUnitInspect(unit.Output, binding)
	if err != nil {
		return Observation{}, ErrUnconfirmedHealth
	}
	health, ok := succeededStep(record, healthStepID, "game.health.check")
	if !ok {
		return Observation{}, ErrUnconfirmedHealth
	}
	observedAt, err := failedGameHealth(health, binding)
	if err != nil || observedAt.Before(inspectedAt) {
		return Observation{}, ErrUnconfirmedHealth
	}
	instance, unitName := externalID(binding.InstanceRef), externalID(binding.UnitRef)
	if instance == "" || unitName == "" {
		return Observation{}, ErrUnconfirmedHealth
	}
	return Observation{
		ServerID: binding.ServerID, EnvironmentID: binding.EnvironmentID,
		InstanceID: instance, UnitID: unitName, ReleaseID: binding.ReleaseID,
		RuntimeID: runtimeID, Health: "unhealthy", UnitState: "running",
		Reason: "game_check_failed", ObservedAt: observedAt,
	}, nil
}

func externalID(ref json.RawMessage) string {
	var decoded struct { ExternalID string `json:"external_id"` }
	_ = json.Unmarshal(ref, &decoded)
	return decoded.ExternalID
}

func failedGameHealth(step store.OperationStep, binding EvidenceBinding) (time.Time, error) {
	var input struct {
		ServerID string `json:"server_id"`
		EnvironmentID string `json:"environment_id"`
		InstanceRef json.RawMessage `json:"instance_ref"`
		GameReleaseID string `json:"game_release_id"`
		RequiredCheckIDs []string `json:"required_check_ids"`
	}
	if json.Unmarshal(step.Input, &input) != nil || !inputHealthMatches(input.ServerID, input.EnvironmentID, input.InstanceRef, input.GameReleaseID, binding) {
		return time.Time{}, ErrUnconfirmedHealth
	}
	if len(input.RequiredCheckIDs) == 0 {
		return time.Time{}, ErrUnconfirmedHealth
	}
	var output struct {
		ServerID string `json:"server_id"`
		EnvironmentID string `json:"environment_id"`
		InstanceRef json.RawMessage `json:"instance_ref"`
		GameReleaseID string `json:"game_release_id"`
		Health string `json:"health"`
		DiagnosticsRedacted bool `json:"diagnostics_redacted"`
		ObservedAt string `json:"observed_at"`
		CheckResults []struct {
			CheckID string `json:"check_id"`
			Result string `json:"result"`
			ObservedAt string `json:"observed_at"`
		} `json:"check_results"`
	}
	if json.Unmarshal(step.Output, &output) != nil || !inputHealthMatches(output.ServerID, output.EnvironmentID, output.InstanceRef, output.GameReleaseID, binding) {
		return time.Time{}, ErrUnconfirmedHealth
	}
	if output.Health != "unhealthy" || !output.DiagnosticsRedacted {
		return time.Time{}, ErrUnconfirmedHealth
	}
	timestamp, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	if err != nil || len(output.CheckResults) != len(input.RequiredCheckIDs) {
		return time.Time{}, ErrUnconfirmedHealth
	}
	seen := make(map[string]bool, len(input.RequiredCheckIDs))
	failures := 0
	for _, result := range output.CheckResults {
		if !checkResultExpected(result.CheckID, result.Result, input.RequiredCheckIDs) || seen[result.CheckID] {
			return time.Time{}, ErrUnconfirmedHealth
		}
		seen[result.CheckID] = true
		if result.Result == "fail" {
			failures++
		}
		at, err := time.Parse(time.RFC3339Nano, result.ObservedAt)
		if err != nil || at.After(timestamp) || timestamp.Sub(at) > 5*time.Minute {
			return time.Time{}, ErrUnconfirmedHealth
		}
	}
	if failures == 0 {
		return time.Time{}, ErrUnconfirmedHealth
	}
	return timestamp, nil
}

func inputHealthMatches(serverID, environmentID string, instance json.RawMessage, release string, binding EvidenceBinding) bool {
	if serverID != binding.ServerID || environmentID != binding.EnvironmentID {
		return false
	}
	return release == binding.ReleaseID && equalDocument(instance, binding.InstanceRef)
}

func checkResultExpected(id, result string, expected []string) bool {
	if result != "pass" && result != "fail" {
		return false
	}
	for _, name := range expected {
		if name == id {
			return true
		}
	}
	return false
}
