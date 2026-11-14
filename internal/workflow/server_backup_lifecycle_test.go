package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupSaveRejectionAndUnknownOutcomeDoNotProjectStopping(t *testing.T) {
	workflow := &ServerBackupWorkflow{schemas: acceptAllTestSchemas{version: "0.21.0-draft"}}
	for _, test := range []struct {
		name       string
		saveStatus protocolstate.StepStatus
		output     json.RawMessage
		wantResult ResolutionState
	}{
		{
			name:       "rejected save",
			saveStatus: protocolstate.StepSucceeded,
			output:     json.RawMessage(`{"outcome":"rejected","reason_code":"save-rejected","release_id":"paper-release","target":{"server_id":"server-1","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}},"observed_at":"2026-11-07T09:00:00Z"}`),
			wantResult: ResolutionDefinitiveError,
		},
		{
			name:       "unknown save",
			saveStatus: protocolstate.StepOutcomeUnknown,
			output:     json.RawMessage(`{"outcome":"unknown"}`),
			wantResult: ResolutionNeedsOperator,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation, server, stopStep := backupSaveLifecycleFixture(test.saveStatus, test.output)
			projected, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: server})
			if err != nil || changed || projected.LifecycleState != protocolstate.ServerReady || projected.DesiredState != "running" {
				t.Fatalf("pre-stop lifecycle projection = %#v, changed=%v, err=%v; want ready/running unchanged", projected, changed, err)
			}
			resolution, err := workflow.HandlePreconditionFailure(context.Background(), operation, stopStep, errors.New("stop precondition did not pass"))
			if err != nil || resolution.State != test.wantResult {
				t.Fatalf("stop precondition resolution = %#v, err=%v; want %s", resolution, err, test.wantResult)
			}
		})
	}
}

func TestBackupStopPreconditionPassesBeforeReadyToStoppingClaimProjection(t *testing.T) {
	server := backupLifecycleServer(protocolstate.ServerReady)
	instance := server.InstanceRef
	unit := json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`)
	gameTarget := json.RawMessage(`{"server_id":"server-1","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}}`)
	profile := ServerBackupProfile{
		ProfileID: "profile-1", GameReleaseID: "paper-release", GameName: "Paper", GameVersion: "1.21",
		ConsistencyMode: "offline", DataSets: []ServerBackupDataSet{{
			DataSetID: "world", RelativePaths: []string{"world"}, Required: true,
			SupportedConsistencyModes: []string{"offline"}, MaximumBytes: 1024,
		}}, ValidationChecks: []ReadinessRequirement{{CheckID: "backup.world", CheckVersion: "1"}},
		StorageAdapterID: "storage.fake", StorageProfileID: server.StorageProfile, StorageContainer: "backups",
		UnitRef: unit,
	}
	now := "2026-11-07T09:00:00Z"
	inspectInput, _ := json.Marshal(map[string]any{"instance_ref": instance})
	instanceOutput, _ := json.Marshal(map[string]any{"instance_ref": instance, "state": "active", "observed_at": now})
	runtimeInput, _ := json.Marshal(map[string]any{"unit_ref": unit})
	runtimeOutput, _ := json.Marshal(map[string]any{"unit_ref": unit, "state": "running", "observed_at": now})
	dataSets := []any{map[string]any{
		"data_set_id": "world", "relative_paths": []string{"world"}, "required": true,
		"supported_consistency_modes": []string{"offline"},
	}}
	describeInput, _ := json.Marshal(map[string]any{"target": json.RawMessage(gameTarget), "release_id": profile.GameReleaseID})
	describeOutput, _ := json.Marshal(map[string]any{
		"target": json.RawMessage(gameTarget), "release_id": profile.GameReleaseID,
		"data_sets": dataSets, "observed_at": now,
	})
	saveInput, _ := json.Marshal(map[string]any{"target": json.RawMessage(gameTarget), "release_id": profile.GameReleaseID, "reason": "backup"})
	saveOutput, _ := json.Marshal(map[string]any{
		"target": json.RawMessage(gameTarget), "release_id": profile.GameReleaseID,
		"outcome": "confirmed", "observed_at": now, "saved_at": now,
	})
	stopInput, _ := json.Marshal(map[string]any{"unit_ref": unit, "grace_period_seconds": backupGracePeriodSeconds})
	operation := store.OperationRecord{
		OperationID: "backup-before-stop", OperationType: "server.backup", ProtocolVersion: "0.21.0-draft",
		Target:          store.OperationTarget{ServerID: server.ServerID, EnvironmentID: server.EnvironmentID},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.backup", Parameters: json.RawMessage(`{"backup_profile":"profile-1"}`)},
		Steps: []store.OperationStep{
			{StepID: backupInspectInstanceStep, Status: protocolstate.StepSucceeded, Input: inspectInput, Output: instanceOutput},
			{StepID: backupInspectRuntimeStep, Status: protocolstate.StepSucceeded, Input: runtimeInput, Output: runtimeOutput},
			{StepID: backupDescribeDataStep, Status: protocolstate.StepSucceeded, Input: describeInput, Output: describeOutput},
			{StepID: backupSaveGameStep, Status: protocolstate.StepSucceeded, Input: saveInput, Output: saveOutput},
			{StepID: backupStopRuntimeStep, Status: protocolstate.StepPending, Input: stopInput},
		},
	}
	serverRecord := store.StoredLogicalServer{Record: server}
	workflow := &ServerBackupWorkflow{
		servers:  testBackupServerRepository{record: serverRecord},
		profiles: staticServerBackupProfileResolver{profile: profile},
		schemas:  acceptAllTestSchemas{version: operation.ProtocolVersion},
	}
	stopStep := operation.Steps[4]
	if err := workflow.CheckPreconditions(context.Background(), operation, stopStep); err != nil {
		t.Fatalf("stop precondition rejected a ready server before the claim transition: %v", err)
	}
	projected, changed, err := workflow.ProjectLifecycle(operation, serverRecord)
	if err != nil || changed || projected.LifecycleState != protocolstate.ServerReady {
		t.Fatalf("pending stop projected an early transition: %#v, changed=%v, err=%v", projected, changed, err)
	}
	operation.Steps[4].Status = protocolstate.StepRunning
	projected, changed, err = workflow.ProjectLifecycle(operation, serverRecord)
	if err != nil || !changed || projected.LifecycleState != protocolstate.ServerStopping {
		t.Fatalf("claimed stop projection = %#v, changed=%v, err=%v; want stopping", projected, changed, err)
	}
}

func TestBackupNoResumeRequiresExactFinalStoppedInspection(t *testing.T) {
	workflow := &ServerBackupWorkflow{schemas: acceptAllTestSchemas{version: "0.21.0-draft"}}
	operation := store.OperationRecord{
		OperationID: "backup-no-resume", OperationType: "server.backup", Status: protocolstate.OperationFailed,
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		Steps: []store.OperationStep{{
			StepID: backupVerifyStoppedStep, Status: protocolstate.StepOutcomeUnknown, Attempt: 1,
			Input: json.RawMessage(`{"unit_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}}`),
		}},
	}
	server := backupLifecycleServer(protocolstate.ServerBackingUp)
	server.DataGenerationID = "generation-original"
	server.DataGenerationStatus = "known"
	projected, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: server})
	if err != nil || !changed || projected.LifecycleState != protocolstate.ServerFailed || projected.DataGenerationStatus != "unknown" {
		t.Fatalf("uncertain final inspection projection = %#v, changed=%v, err=%v; want failed/unknown", projected, changed, err)
	}
	if backupFinalRuntimeStopped(operation) {
		t.Fatal("unknown final inspection was treated as proof that the exact runtime stopped")
	}

	operation.Status = protocolstate.OperationFailed
	operation.Steps[0].Status = protocolstate.StepSucceeded
	operation.Steps[0].Output = json.RawMessage(`{"unit_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"},"state":"stopped","observed_at":"2026-11-07T09:00:00Z"}`)
	if !backupFinalRuntimeStopped(operation) {
		t.Fatal("exact stopped inspection was not accepted")
	}
	projected, changed, err = workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: server})
	if err != nil || !changed || projected.LifecycleState != protocolstate.ServerStopped || projected.DataGenerationID != "generation-original" || projected.DataGenerationStatus != "known" {
		t.Fatalf("confirmed final stop projection = %#v, changed=%v, err=%v; want stopped/original generation/known", projected, changed, err)
	}
}

func TestBackupResumeRotatesGenerationAndFailsClosedUntilReadiness(t *testing.T) {
	workflow := &ServerBackupWorkflow{schemas: acceptAllTestSchemas{version: "0.21.0-draft"}}
	operation := store.OperationRecord{
		OperationID: "backup-resume", OperationType: "server.backup", Status: protocolstate.OperationRunning,
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		Steps: []store.OperationStep{
			{StepID: backupStopRuntimeStep, Status: protocolstate.StepSucceeded, Attempt: 1},
			{StepID: backupStartRuntimeStep, Status: protocolstate.StepRunning, Attempt: 1},
			{StepID: backupReadinessStep, Status: protocolstate.StepPending},
		},
	}
	server := backupLifecycleServer(protocolstate.ServerBackingUp)
	server.DataGenerationID = "generation-original"
	server.DataGenerationStatus = "known"
	starting, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: server})
	if err != nil || !changed || starting.LifecycleState != protocolstate.ServerStarting ||
		starting.DataGenerationID == "generation-original" || starting.DataGenerationStatus != "unknown" {
		t.Fatalf("resume projection = %#v, changed=%v, err=%v; want starting with a new unknown generation", starting, changed, err)
	}

	operation.Status = protocolstate.OperationFailed
	failed, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: starting})
	if err != nil || !changed || failed.LifecycleState != protocolstate.ServerFailed || failed.DataGenerationStatus != "unknown" {
		t.Fatalf("failed resume projection = %#v, changed=%v, err=%v; want failed/unknown", failed, changed, err)
	}

	operation.Status = protocolstate.OperationSucceeded
	operation.Steps[2].Status = protocolstate.StepSucceeded
	validating := starting
	validating.LifecycleState = protocolstate.ServerValidating
	operation.Result = backupReadyResult(operation, validating)
	ready, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: validating})
	if err != nil || !changed || ready.LifecycleState != protocolstate.ServerReady || ready.DataGenerationStatus != "known" {
		t.Fatalf("readiness-confirmed resume projection = %#v, changed=%v, err=%v; want ready/known", ready, changed, err)
	}
}

type acceptAllTestSchemas struct{ version string }

type staticServerBackupProfileResolver struct{ profile ServerBackupProfile }

func (resolver staticServerBackupProfileResolver) ResolveServerBackup(context.Context, store.LogicalServer, string) (ServerBackupProfile, error) {
	return resolver.profile, nil
}

type testBackupServerRepository struct {
	store.LifecycleOperationStore
	record store.StoredLogicalServer
}

func (repository testBackupServerRepository) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	if repository.record.Record.EnvironmentID != environmentID || repository.record.Record.ServerID != serverID {
		return store.StoredLogicalServer{}, store.ErrNotFound
	}
	return repository.record, nil
}

func (schemas acceptAllTestSchemas) ProtocolVersion() string { return schemas.version }

func (schemas acceptAllTestSchemas) HasSchema(string) bool { return true }

func (schemas acceptAllTestSchemas) ValidateJSON(_ string, raw []byte) error {
	if !json.Valid(raw) {
		return errors.New("invalid JSON")
	}
	return nil
}

func backupSaveLifecycleFixture(saveStatus protocolstate.StepStatus, saveOutput json.RawMessage) (store.OperationRecord, store.LogicalServer, store.OperationStep) {
	server := backupLifecycleServer(protocolstate.ServerReady)
	server.DesiredState = "running"
	instance := server.InstanceRef
	inspectInput, _ := json.Marshal(map[string]any{"instance_ref": instance})
	saveInput := json.RawMessage(`{"target":{"server_id":"server-1","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}},"release_id":"paper-release"}`)
	operation := store.OperationRecord{
		OperationID: "backup-save", OperationType: "server.backup", Status: protocolstate.OperationRunning,
		Target: store.OperationTarget{ServerID: server.ServerID, EnvironmentID: server.EnvironmentID},
		Steps: []store.OperationStep{
			{StepID: backupInspectInstanceStep, Input: inspectInput},
			{StepID: backupSaveGameStep, Status: saveStatus, Input: saveInput, Output: saveOutput},
			{StepID: backupStopRuntimeStep, Status: protocolstate.StepPending},
		},
	}
	return operation, server, operation.Steps[2]
}

func backupLifecycleServer(state protocolstate.ServerState) store.LogicalServer {
	return store.LogicalServer{
		ServerID: "server-1", EnvironmentID: "staging", LifecycleState: state,
		InstanceRef:   json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`),
		GameAdapterID: "game.fake", ExecutionAdapterID: "execution.fake", StorageProfile: "storage-profile",
		DataGenerationID: "generation-1", DataGenerationStatus: "known",
	}
}

func backupReadyResult(operation store.OperationRecord, server store.LogicalServer) json.RawMessage {
	manifest := map[string]any{
		"manifest_version": backupManifestVersion, "backup_id": operation.OperationID,
		"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID,
		"data_generation_id": server.DataGenerationID, "source_instance_ref": server.InstanceRef,
		"source_operation_id": operation.OperationID,
	}
	result := map[string]any{
		"operation_type": "server.backup",
		"operation_data": map[string]any{
			"backup_id": operation.OperationID, "environment_id": operation.Target.EnvironmentID,
			"data_generation_id": server.DataGenerationID, "manifest": manifest,
			"final_lifecycle_state": "ready", "readiness_assertion": map[string]any{
				"server_id": server.ServerID, "environment_id": server.EnvironmentID,
				"instance_ref": server.InstanceRef,
			},
		},
	}
	encoded, _ := json.Marshal(result)
	return encoded
}

func TestBackupLifecycleProjectionPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		state     protocolstate.ServerState
		status    protocolstate.OperationStatus
		step      store.OperationStep
		want      protocolstate.ServerState
		desired   string
		wantError string
	}{
		{name: "degraded stop claimed", state: protocolstate.ServerDegraded, step: store.OperationStep{StepID: backupStopRuntimeStep, Status: protocolstate.StepRunning}, want: protocolstate.ServerStopping, desired: "stopped"},
		{name: "missing prepare is treated as nonpending", state: protocolstate.ServerStopped, want: protocolstate.ServerBackingUp, desired: "stopped"},
		{name: "bad stop evidence takes precedence over cancellation", state: protocolstate.ServerStopping, status: protocolstate.OperationCancelled, step: store.OperationStep{StepID: backupStopRuntimeStep, Status: protocolstate.StepSucceeded}, wantError: "backup stop step lacks exact stopped-unit evidence"},
		{name: "start claim takes precedence over terminal success", state: protocolstate.ServerBackingUp, status: protocolstate.OperationSucceeded, step: store.OperationStep{StepID: backupStartRuntimeStep, Status: protocolstate.StepRunning}, want: protocolstate.ServerStarting, desired: "running"},
		{name: "missing readiness is treated as nonpending", state: protocolstate.ServerStarting, status: protocolstate.OperationFailed, want: protocolstate.ServerValidating, desired: "running"},
		{name: "validating failure keeps desired state", state: protocolstate.ServerValidating, status: protocolstate.OperationCancelled, want: protocolstate.ServerDegraded, desired: "running"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := backupLifecycleServer(tc.state)
			server.DesiredState = "running"
			operation := store.OperationRecord{
				OperationType: "server.backup", OperationID: "backup-projection", Status: tc.status,
				Target: store.OperationTarget{ServerID: server.ServerID, EnvironmentID: server.EnvironmentID},
				Steps:  []store.OperationStep{tc.step},
			}
			workflow := &ServerBackupWorkflow{}
			got, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: server})
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError || changed || got.ServerID != "" {
					t.Fatalf("got %+v, changed %v, error %v; want %q", got, changed, err, tc.wantError)
				}
				return
			}
			if err != nil || !changed || got.LifecycleState != tc.want || got.DesiredState != tc.desired {
				t.Fatalf("got %s/%s, changed %v, error %v; want %s/%s", got.LifecycleState, got.DesiredState, changed, err, tc.want, tc.desired)
			}
		})
	}
}
