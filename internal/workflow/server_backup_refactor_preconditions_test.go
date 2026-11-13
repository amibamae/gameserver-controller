package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the CheckPreconditions branches that
// server_backup_preconditions_test.go does not reach, so the step-table
// refactor of CheckPreconditions keeps their exact order and errors.

// backupRefactorPreconditionWorkflow returns a copy of the fixture workflow
// whose repository holds the server in state and whose streams verify.
func backupRefactorPreconditionWorkflow(f backupPreconditionFixture, state protocolstate.ServerState) *ServerBackupWorkflow {
	server := f.server
	server.LifecycleState = state
	workflow := *f.workflow
	workflow.servers = testBackupServerRepository{record: store.StoredLogicalServer{Record: server}}
	workflow.transfers = scriptedBackupStreams{}
	return &workflow
}

// backupRefactorOperationThrough copies the fixture operation and records
// valid outputs for every step before stepID.
func backupRefactorOperationThrough(t *testing.T, f backupPreconditionFixture, stepID string) store.OperationRecord {
	t.Helper()
	operation := f.operation
	operation.Steps = append([]store.OperationStep(nil), f.operation.Steps...)
	completeBackupThrough(t, &operation, stepID)
	return operation
}

func TestServerBackupRefactorPreconditionsCancelledContext(t *testing.T) {
	f := newBackupPreconditionFixture(t, true)
	operation := backupRefactorOperationThrough(t, f, backupInspectInstanceStep)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	workflow := backupRefactorPreconditionWorkflow(f, protocolstate.ServerReady)
	err := workflow.CheckPreconditions(ctx, operation, operation.Steps[backupStepIndex(operation, backupInspectInstanceStep)])
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckPreconditions with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestServerBackupRefactorPreconditionsUndecodableProfile(t *testing.T) {
	f := newBackupPreconditionFixture(t, true)
	operation := backupRefactorOperationThrough(t, f, backupInspectInstanceStep)
	operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_profile":`)
	workflow := backupRefactorPreconditionWorkflow(f, protocolstate.ServerReady)
	err := workflow.CheckPreconditions(context.Background(), operation, operation.Steps[backupStepIndex(operation, backupInspectInstanceStep)])
	if err == nil || err.Error() != "decode accepted backup profile" {
		t.Fatalf("CheckPreconditions with undecodable parameters = %v, want %q", err, "decode accepted backup profile")
	}
}

func TestServerBackupRefactorPreconditionsProviderInspectionInputNotBound(t *testing.T) {
	f := newBackupPreconditionFixture(t, true)
	operation := backupRefactorOperationThrough(t, f, backupInspectInstanceStep)
	// The recorded plan step stays bound to the managed instance; only the
	// step handed to CheckPreconditions names another instance.
	step := operation.Steps[backupStepIndex(operation, backupInspectInstanceStep)]
	step.Input = backupJSON(t, map[string]any{"instance_ref": json.RawMessage(backupOtherVM)})
	workflow := backupRefactorPreconditionWorkflow(f, protocolstate.ServerReady)
	err := workflow.CheckPreconditions(context.Background(), operation, step)
	want := "provider inspection is not bound to the current managed instance"
	if err == nil || err.Error() != want {
		t.Fatalf("CheckPreconditions(%s) = %v, want %q", backupInspectInstanceStep, err, want)
	}
}

func TestServerBackupRefactorPreconditionsPreparationAfterStoppingAnotherUnit(t *testing.T) {
	f := newBackupPreconditionFixture(t, true)
	operation := backupRefactorOperationThrough(t, f, backupPrepareStep)
	// The stop step is consistently bound to a unit other than the profile's.
	index := backupStepIndex(operation, backupStopRuntimeStep)
	operation.Steps[index].Input = backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupOtherUnit), "grace_period_seconds": 120})
	setBackupOutputField(t, &operation, backupStopRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit))
	workflow := backupRefactorPreconditionWorkflow(f, protocolstate.ServerStopped)
	err := workflow.CheckPreconditions(context.Background(), operation, operation.Steps[backupStepIndex(operation, backupPrepareStep)])
	want := "backup preparation requires the exact runtime to be confirmed stopped"
	if err == nil || err.Error() != want {
		t.Fatalf("CheckPreconditions(%s) = %v, want %q", backupPrepareStep, err, want)
	}
}

// TestServerBackupRefactorPreconditionsExactMessages pins the full text of
// every step's first rejection, where the older tests match a substring.
func TestServerBackupRefactorPreconditionsExactMessages(t *testing.T) {
	capture := backupDataStepID("capture", "world")
	upload := backupDataStepID("upload", "world")
	readback := backupDataStepID("readback", "world")
	tests := []struct {
		resume bool
		step   string
		state  protocolstate.ServerState
		want   string
	}{
		{true, backupInspectInstanceStep, protocolstate.ServerStarting, "provider inspection requires the original ready, degraded, or stopped lifecycle state"},
		{true, backupInspectRuntimeStep, protocolstate.ServerDegraded, "runtime inspection requires the exact active provider instance and original lifecycle state"},
		{true, backupDescribeDataStep, protocolstate.ServerDegraded, "data-set description requires the exact instance, runtime state, and trusted release"},
		{true, backupSaveGameStep, protocolstate.ServerDegraded, "offline backup save requires the exact ready or degraded running server and declared data sets"},
		{true, backupStopRuntimeStep, protocolstate.ServerDegraded, "runtime stop requires a confirmed game save and exact ready or degraded server evidence"},
		{true, backupPrepareStep, protocolstate.ServerReady, "backup preparation requires a stopped runtime"},
		{true, capture, protocolstate.ServerStopped, "capture requires backing_up state and exact trusted data-set evidence"},
		{true, upload, protocolstate.ServerStarting, "storage upload requires the server to remain stopped during backup"},
		{true, readback, protocolstate.ServerStarting, "storage readback requires the server to remain stopped during backup"},
		{true, backupValidateStep, protocolstate.ServerStarting, "game backup validation requires exact completed storage readbacks"},
		{true, backupStartRuntimeStep, protocolstate.ServerStopped, "runtime resume requires a fully validated backup of the exact data generation"},
		{true, backupVerifyRuntimeStep, protocolstate.ServerBackingUp, "runtime verification requires the exact completed start command"},
		{true, backupReadinessStep, protocolstate.ServerBackingUp, "readiness checks require the exact running runtime and managed instance"},
		{false, backupVerifyStoppedStep, protocolstate.ServerStarting, "final stopped-state inspection requires the validated backup and exact execution unit"},
	}
	for _, test := range tests {
		t.Run(test.step, func(t *testing.T) {
			f := newBackupPreconditionFixture(t, test.resume)
			operation := backupRefactorOperationThrough(t, f, test.step)
			workflow := backupRefactorPreconditionWorkflow(f, test.state)
			err := workflow.CheckPreconditions(context.Background(), operation, operation.Steps[backupStepIndex(operation, test.step)])
			if err == nil || err.Error() != test.want {
				t.Fatalf("CheckPreconditions(%s) = %v, want %q", test.step, err, test.want)
			}
		})
	}
}
