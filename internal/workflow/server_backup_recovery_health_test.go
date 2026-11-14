package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const recoveryTestObservedAt = "2026-11-14T00:00:00Z"

type backupRecoveryTestRuntime struct {
	backupDescriptorRuntime
	state string
	unit  json.RawMessage
}

func (r backupRecoveryTestRuntime) Invoke(_ context.Context, _ string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return adapter.InvokeResponse{
		JSONRPC: "2.0", ID: request.ID,
		Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: mustJSON(stopUnitInspection{
			State: r.state, UnitRef: r.unit, ObservedAt: recoveryTestObservedAt,
		})},
	}, nil
}

func backupRecoveryTestFixture(stepID, state string) (*ServerBackupWorkflow, store.OperationRecord, store.OperationStep) {
	const version = "0.21.0-draft"
	const unitAdapter = "execution"
	const uri = "urn:gameserver-controller:" + version + ":execution:"
	unit := json.RawMessage(`{"name":"server.service"}`)
	input := mustJSON(map[string]json.RawMessage{"unit_ref": unit})
	runtime := backupRecoveryTestRuntime{
		backupDescriptorRuntime: backupDescriptorRuntime{version: version, descriptors: map[string]adapter.Descriptor{
			unitAdapter: {AdapterID: unitAdapter, Category: adapter.CategoryExecution, Capabilities: []adapter.Capability{
				{Name: "execution.unit.inspect", Version: "1", InputSchemaURI: uri + "systemd-unit-inspect-input", OutputSchemaURI: uri + "systemd-unit-inspect-output"},
			}},
		}},
		state: state, unit: unit,
	}
	w := &ServerBackupWorkflow{adapters: runtime, schemas: acceptAllTestSchemas{version: version}}
	inspect := store.OperationStep{
		StepID: backupInspectRuntimeStep, AdapterID: unitAdapter, CapabilityName: "execution.unit.inspect", CapabilityVersion: "1", Input: input,
	}
	verify := inspect
	verify.StepID = backupVerifyRuntimeStep
	operation := store.OperationRecord{OperationID: "backup-1", ProtocolVersion: version, Steps: []store.OperationStep{inspect, verify}}
	step := store.OperationStep{StepID: stepID, AdapterID: unitAdapter, Input: input, ExternalOperationRef: json.RawMessage(`{"id":"attempt"}`)}
	return w, operation, step
}

func TestBackupRecoveryRuntimeResolution(t *testing.T) {
	for _, tc := range []struct {
		step, observed string
		want           ResolutionState
	}{
		{backupStopRuntimeStep, "stopped", ResolutionCompleted},
		{backupStopRuntimeStep, "running", ResolutionSafeToRetry},
		{backupStopRuntimeStep, "stopping", ResolutionPending},
		{backupStopRuntimeStep, "absent", ResolutionNeedsOperator},
		{backupStartRuntimeStep, "running", ResolutionCompleted},
		{backupStartRuntimeStep, "stopped", ResolutionSafeToRetry},
		{backupStartRuntimeStep, "absent", ResolutionSafeToRetry},
		{backupStartRuntimeStep, "starting", ResolutionPending},
		{backupStartRuntimeStep, "failed", ResolutionDefinitiveError},
		{backupStartRuntimeStep, "unknown", ResolutionNeedsOperator},
	} {
		t.Run(tc.step+"/"+tc.observed, func(t *testing.T) {
			w, operation, step := backupRecoveryTestFixture(tc.step, tc.observed)
			got, err := w.Reconcile(context.Background(), operation, step)
			if err != nil || got.State != tc.want || !equalJSON(got.ExternalOperationRef, step.ExternalOperationRef) {
				t.Fatalf("Reconcile = %+v, %v; want %s and original external reference", got, err, tc.want)
			}
			if tc.want == ResolutionCompleted {
				want := `{"unit_ref":{"name":"server.service"},"outcome":"completed","state":"` + tc.observed + `","observed_at":"` + recoveryTestObservedAt + `"}`
				if string(got.Output) != want {
					t.Fatalf("output = %s; want %s", got.Output, want)
				}
			}
			if tc.want == ResolutionDefinitiveError {
				want := protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after backup resume was attempted.", operation.OperationID, false, "operator_review", true)
				if !equalJSON(got.Error, want) {
					t.Fatalf("error = %s; want %s", got.Error, want)
				}
			}
		})
	}
}

func TestBackupValidationRejectionRequiresBoundFailures(t *testing.T) {
	for _, tc := range []struct {
		name, generation string
		results          []map[string]string
		want             bool
	}{
		{"definitive", "generation-1", []map[string]string{{"result": "fail", "observed_at": recoveryTestObservedAt}}, true},
		{"wrong generation", "generation-2", []map[string]string{{"result": "fail", "observed_at": recoveryTestObservedAt}}, false},
		{"unknown after definitive", "generation-1", []map[string]string{
			{"result": "fail", "observed_at": recoveryTestObservedAt}, {"result": "unknown", "observed_at": recoveryTestObservedAt},
		}, false},
		{"malformed check timestamp", "generation-1", []map[string]string{{"result": "fail", "observed_at": "invalid"}}, false},
		{"no failing check", "generation-1", []map[string]string{{"result": "pass", "observed_at": recoveryTestObservedAt}}, false},
		{"empty checks", "generation-1", []map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := store.OperationRecord{
				OperationID: "backup-1", Target: store.OperationTarget{EnvironmentID: "test"},
				Steps: []store.OperationStep{{
					StepID: backupValidateStep, Status: protocolstate.StepSucceeded,
					Input: json.RawMessage(`{"backup_id":"backup-1","environment_id":"test","manifest":{"data_generation_id":"generation-1"}}`),
					Output: mustJSON(map[string]any{
						"validation_result": "invalid", "backup_id": "backup-1", "environment_id": "test",
						"data_generation_id": tc.generation, "observed_at": recoveryTestObservedAt, "check_results": tc.results,
					}),
				}},
			}
			got, err := (&ServerBackupWorkflow{}).HandlePreconditionFailure(context.Background(), op, store.OperationStep{StepID: backupStartRuntimeStep}, nil)
			want := ResolutionNeedsOperator
			if tc.want {
				want = ResolutionDefinitiveError
			}
			if err != nil || got.State != want {
				t.Fatalf("HandlePreconditionFailure = %+v, %v; want %s", got, err, want)
			}
			if tc.want {
				wantError := protocolError("core.safety.backup_precondition_unmet", "safety_blocked", "Game-specific backup validation did not confirm every required check.", op.OperationID, false, "inspect_operation", false)
				if !equalJSON(got.Error, wantError) {
					t.Fatalf("error = %s; want %s", got.Error, wantError)
				}
			}
		})
	}
}
