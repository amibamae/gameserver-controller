package workflow

import (
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type backupProjectionCase struct {
	name      string
	state     protocolstate.ServerState
	status    protocolstate.OperationStatus
	step      store.OperationStep
	want      protocolstate.ServerState
	desired   string
	wantError string
}

func TestBackupLifecycleProjectionPrecedence(t *testing.T) {
	tests := []backupProjectionCase{
		{name: "degraded stop claimed", state: protocolstate.ServerDegraded, step: store.OperationStep{StepID: backupStopRuntimeStep, Status: protocolstate.StepRunning}, want: protocolstate.ServerStopping, desired: "stopped"},
		{name: "missing prepare is treated as nonpending", state: protocolstate.ServerStopped, want: protocolstate.ServerBackingUp, desired: "stopped"},
		{name: "bad stop evidence takes precedence over cancellation", state: protocolstate.ServerStopping, status: protocolstate.OperationCancelled, step: store.OperationStep{StepID: backupStopRuntimeStep, Status: protocolstate.StepSucceeded}, wantError: "backup stop step lacks exact stopped-unit evidence"},
		{name: "start claim takes precedence over terminal success", state: protocolstate.ServerBackingUp, status: protocolstate.OperationSucceeded, step: store.OperationStep{StepID: backupStartRuntimeStep, Status: protocolstate.StepRunning}, want: protocolstate.ServerStarting, desired: "running"},
		{name: "missing readiness is treated as nonpending", state: protocolstate.ServerStarting, status: protocolstate.OperationFailed, want: protocolstate.ServerValidating, desired: "running"},
		{name: "validating failure keeps desired state", state: protocolstate.ServerValidating, status: protocolstate.OperationCancelled, want: protocolstate.ServerDegraded, desired: "running"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { checkBackupLifecycleProjection(t, tc) })
	}
}

func checkBackupLifecycleProjection(t *testing.T, tc backupProjectionCase) {
	t.Helper()
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
}
