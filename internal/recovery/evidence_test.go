package recovery

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	testInstance = `{"adapter_id":"hosting.vultr-staging","environment_id":"staging","resource_kind":"instance","external_id":"test-vm"}`
	testUnit     = `{"adapter_id":"execution.staging","environment_id":"staging","resource_kind":"systemd-unit","external_id":"minecraft-vanilla.service"}`
	testTarget   = `{"server_id":"recovery-test-paper","environment_id":"staging","instance_ref":` + testInstance + `}`
)

func recoveryEvidenceFixture(t *testing.T) (store.OperationRecord, EvidenceBinding, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	created := now.Add(-2 * time.Minute)
	inspectTime := created.Add(10 * time.Second).Format(time.RFC3339Nano)
	drainTime := created.Add(25 * time.Second).Format(time.RFC3339Nano)
	saveTime := created.Add(40 * time.Second).Format(time.RFC3339Nano)
	restartedAt := created.Add(time.Minute).Format(time.RFC3339Nano)
	newInspectTime := created.Add(70 * time.Second).Format(time.RFC3339Nano)
	unit := json.RawMessage(testUnit)
	instance := json.RawMessage(testInstance)
	binding := EvidenceBinding{
		ServerID: "recovery-test-paper", EnvironmentID: "staging",
		InstanceRef: instance, UnitRef: unit, ReleaseID: "paper-test-release",
		InspectStepID: "inspect-old", DrainStepID: "drain-proxy",
		SaveStepID: "save-paper", RestartStepID: "restart-paper",
	}
	makeStep := func(id, cap, output string) store.OperationStep {
		return store.OperationStep{StepID: id, CapabilityName: cap,
			Status: protocolstate.StepSucceeded, Output: json.RawMessage(output)}
	}
	inspection := `{"unit_ref":` + testUnit + `,"target":` + testTarget +
		`,"release_ref":"paper-test-release","runtime_id":"old-invocation","state":"running","observed_at":"` + inspectTime + `"}`
	drain := `{"target":` + testTarget +
		`,"request_outcome":"confirmed","registration_state":"draining","routability":"not_routable","observed_at":"` + drainTime + `"}`
	save := `{"target":` + testTarget +
		`,"release_id":"paper-test-release","outcome":"confirmed","observed_at":"` + saveTime +
		`","saved_at":"` + saveTime + `"}`
	restartInput := `{"unit_ref":` + testUnit + `,"target":` + testTarget +
		`,"release_id":"paper-test-release","expected_runtime_id":"old-invocation","save_confirmation":` +
		save + `,"save_step_id":"save-paper","force":false,"grace_period_seconds":120}`
	restartOutput := `{"unit_ref":` + testUnit + `,"target":` + testTarget +
		`,"release_id":"paper-test-release","previous_runtime_id":"old-invocation","runtime_id":"new-invocation",` +
		`"outcome":"completed","state":"running","clean_stop_confirmed":true,"force_used":false,` +
		`"persistent_data_preserved":true,"observed_at":"` + restartedAt + `"}`
	newInspection := `{"unit_ref":` + testUnit + `,"target":` + testTarget +
		`,"release_ref":"paper-test-release","runtime_id":"new-invocation","state":"running","observed_at":"` + newInspectTime + `"}`
	record := store.OperationRecord{
		OperationID: "recovery-1", OperationType: "server.recover",
		ProtocolVersion: "0.21.0-draft", CreatedAt: created,
		Target: store.OperationTarget{ServerID: binding.ServerID, EnvironmentID: binding.EnvironmentID, InstanceRef: instance},
		Steps: []store.OperationStep{
			makeStep("inspect-old", "execution.unit.inspect", inspection),
			makeStep("drain-proxy", "integration.proxy.backend.drain", drain),
			makeStep("save-paper", "game.save.request", save),
			{StepID: "restart-paper", CapabilityName: "execution.unit.restart",
				Status: protocolstate.StepSucceeded, ResolvedInput: json.RawMessage(restartInput),
				Output: json.RawMessage(restartOutput)},
			makeStep("inspect-new", "execution.unit.inspect", newInspection),
		},
	}
	return record, binding, now
}

func copyOperation(t *testing.T, record store.OperationRecord) store.OperationRecord {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var cloned store.OperationRecord
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestRecoveryProofRequiresExactDurableSequence(t *testing.T) {
	record, binding, now := recoveryEvidenceFixture(t)
	got, err := CheckRestartDispatch(record, binding, now)
	if err != nil || got.RuntimeID != "old-invocation" {
		t.Fatalf("restart proof = %+v, %v", got, err)
	}
	next, err := CheckNewRuntimeProof(record, binding, "inspect-new", now)
	if err != nil || next != "new-invocation" {
		t.Fatalf("new runtime proof = %q, %v", next, err)
	}
}

func TestRecoveryRestartFailsClosedWithoutSpecificEvidence(t *testing.T) {
	base, binding, now := recoveryEvidenceFixture(t)
	cases := []struct {
		name   string
		change func(*store.OperationRecord)
	}{
		{"wrong operation type", func(r *store.OperationRecord) { r.OperationType = "server.start" }},
		{"different provider instance", func(r *store.OperationRecord) {
			r.Target.InstanceRef = json.RawMessage(strings.ReplaceAll(testInstance, "test-vm", "another-instance"))
		}},
		{"missing completed inspect", func(r *store.OperationRecord) { r.Steps[0].Status = protocolstate.StepOutcomeUnknown }},
		{"wrong initial runtime", func(r *store.OperationRecord) {
			r.Steps[0].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[0].Output), "old-invocation", "another-runtime"))
		}},
		{"unknown inspected state", func(r *store.OperationRecord) {
			r.Steps[0].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[0].Output), `"running"`, `"unknown"`))
		}},
		{"wrong inspected release", func(r *store.OperationRecord) {
			r.Steps[0].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[0].Output), "paper-test-release", "other-release"))
		}},
		{"missing drain", func(r *store.OperationRecord) { r.Steps[1].Status = protocolstate.StepPending }},
		{"routable during restart", func(r *store.OperationRecord) {
			r.Steps[1].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[1].Output), "not_routable", "routable"))
		}},
		{"drain not confirmed", func(r *store.OperationRecord) {
			r.Steps[1].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[1].Output), `"confirmed"`, `"accepted"`))
		}},
		{"save not completed", func(r *store.OperationRecord) { r.Steps[2].Status = protocolstate.StepWaiting }},
		{"save rejected", func(r *store.OperationRecord) {
			r.Steps[2].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[2].Output), `"confirmed"`, `"rejected"`))
		}},
		{"save from different instance", func(r *store.OperationRecord) {
			r.Steps[2].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[2].Output), "test-vm", "another-instance"))
		}},
		{"save confirmation differs from durable output", func(r *store.OperationRecord) {
			r.Steps[3].ResolvedInput = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].ResolvedInput), `"outcome":"confirmed"`, `"outcome":"rejected"`))
		}},
		{"restart targets different incarnation", func(r *store.OperationRecord) {
			r.Steps[3].ResolvedInput = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].ResolvedInput), `"expected_runtime_id":"old-invocation"`, `"expected_runtime_id":"unknown"`))
		}},
		{"force restart", func(r *store.OperationRecord) {
			r.Steps[3].ResolvedInput = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].ResolvedInput), `"force":false`, `"force":true`))
		}},
		{"unbounded grace period", func(r *store.OperationRecord) {
			r.Steps[3].ResolvedInput = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].ResolvedInput), `"grace_period_seconds":120`, `"grace_period_seconds":3600`))
		}},
		{"missing materialized input", func(r *store.OperationRecord) { r.Steps[3].ResolvedInput = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := copyOperation(t, base)
			tc.change(&record)
			if proof, err := CheckRestartDispatch(record, binding, now); err == nil {
				t.Fatalf("unsafe restart proof accepted: %+v", proof)
			}
		})
	}
}

func TestRecoverySaveMustBeFreshAndAfterObservedRuntime(t *testing.T) {
	record, binding, now := recoveryEvidenceFixture(t)
	for name, date := range map[string]time.Time{
		"stale":          now.Add(-4 * time.Minute),
		"future":         now.Add(time.Minute),
		"before inspect": record.CreatedAt.Add(2 * time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			copy := copyOperation(t, record)
			var save map[string]any
			_ = json.Unmarshal(copy.Steps[2].Output, &save)
			save["saved_at"] = date.Format(time.RFC3339Nano)
			copy.Steps[2].Output, _ = json.Marshal(save)
			copy.Steps[3].ResolvedInput = json.RawMessage(strings.ReplaceAll(
				string(copy.Steps[3].ResolvedInput), `"saved_at":"`+record.CreatedAt.Add(40*time.Second).Format(time.RFC3339Nano)+`"`,
				`"saved_at":"`+date.Format(time.RFC3339Nano)+`"`))
			if _, err := CheckRestartDispatch(copy, binding, now); err == nil {
				t.Fatal("unsafe timestamp accepted")
			}
		})
	}
}

func TestRecoveryNeverAcceptsOldOrForcedRuntime(t *testing.T) {
	base, binding, now := recoveryEvidenceFixture(t)
	for _, tt := range []struct {
		name   string
		change func(*store.OperationRecord)
	}{
		{"restart unresolved", func(r *store.OperationRecord) { r.Steps[3].Status = protocolstate.StepOutcomeUnknown }},
		{"restart used force", func(r *store.OperationRecord) {
			r.Steps[3].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].Output), `"force_used":false`, `"force_used":true`))
		}},
		{"save not preserved", func(r *store.OperationRecord) {
			r.Steps[3].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].Output), `"persistent_data_preserved":true`, `"persistent_data_preserved":false`))
		}},
		{"old runtime persisted", func(r *store.OperationRecord) {
			r.Steps[3].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[3].Output), `"runtime_id":"new-invocation"`, `"runtime_id":"old-invocation"`))
		}},
		{"unit inspection stale runtime", func(r *store.OperationRecord) {
			r.Steps[4].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[4].Output), `"runtime_id":"new-invocation"`, `"runtime_id":"old-invocation"`))
		}},
		{"unit inspection absent", func(r *store.OperationRecord) { r.Steps[4].Status = protocolstate.StepPending }},
		{"unit inspection wrong host", func(r *store.OperationRecord) {
			r.Steps[4].Output = json.RawMessage(strings.ReplaceAll(string(r.Steps[4].Output), "test-vm", "other-vm"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := copyOperation(t, base)
			tt.change(&record)
			if runtime, err := CheckNewRuntimeProof(record, binding, "inspect-new", now); err == nil {
				t.Fatalf("unsafe runtime accepted: %s", runtime)
			}
		})
	}
}

func TestRecoverySafetyErrorNeverReturnsAdapterLog(t *testing.T) {
	secret := "player-chat-or-world-token"
	value := DescribeSafetyFailure(errors.New(secret))
	if strings.Contains(value, secret) || !strings.HasPrefix(value, "minecraft_recovery_rejected:") {
		t.Fatal("recovery safety error leaks untrusted diagnostic data")
	}
}
