package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/simulation"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// destroyFaultState is what a destroy fault test pins: the operation, the
// logical server, the managed-VM slot and how often destruction was sent.
type destroyFaultState struct {
	Operation    protocolstate.OperationStatus
	ErrorCode    string
	DestroyCalls int
	Lifecycle    protocolstate.ServerState
	Capacity     store.CapacityState
}

func destroyFaultSnapshot(t *testing.T, fixture serverDestroyFixture) destroyFaultState {
	t.Helper()
	stored, err := fixture.base.store.GetOperation(fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := fixture.base.store.GetCapacityReservation("start-test-capacity")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, call := range fixture.base.hosting.Calls() {
		if call.Params.RequestContext.OperationID == fixture.operation.Record.OperationID && call.Params.Capability == "hosting.instance.destroy" {
			calls++
		}
	}
	return destroyFaultState{Operation: stored.Record.Status, ErrorCode: errorCode(stored.Record.Error), DestroyCalls: calls,
		Lifecycle: server.Record.LifecycleState, Capacity: reservation.State}
}

// destroyInspection is one scripted exact-instance inspection: the state
// the provider reports, or a failed call when err is set.
type destroyInspection struct {
	state string
	err   error
}

// scriptDestroyFault scripts the exact inspections in call order, a stopped
// runtime, and the destroy call's reply. Observation times advance one
// second per scripted reply so every observation is fresh and ordered.
func scriptDestroyFault(t *testing.T, fixture serverDestroyFixture, destroy fakeadapter.Reply, inspections ...destroyInspection) {
	t.Helper()
	base := time.Now().UTC().Add(time.Minute)
	replies := make([]fakeadapter.Reply, 0, len(inspections))
	for i, inspection := range inspections {
		at := base.Add(time.Duration(2+i) * time.Second)
		if i == 0 {
			at = base
		}
		replies = append(replies, fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, ObservedAt: at, Err: inspection.err,
			Output: instanceInspectionOutput(json.RawMessage(startTestInstance), inspection.state, at)})
	}
	unit := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, base.Add(time.Second).Format(time.RFC3339Nano)))
	scripts := map[*fakeadapter.Scripted]map[string][]fakeadapter.Reply{
		fixture.base.hosting:   {"hosting.instance.inspect": replies, "hosting.instance.destroy": {destroy}},
		fixture.base.execution: {"execution.unit.inspect": {{Outcome: adapter.OutcomeCompleted, Output: unit, ObservedAt: base.Add(time.Second)}}},
	}
	for fake, byCapability := range scripts {
		for capability, script := range byCapability {
			if err := fake.SetScript(capability, script...); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// completedDestroyReply is a provider reply that claims the instance is
// already destroyed.
func completedDestroyReply() fakeadapter.Reply {
	at := time.Now().UTC().Add(time.Minute + 1500*time.Millisecond)
	output := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":"completed","state":"destroyed","observed_at":%q}`, startTestInstance, at.Format(time.RFC3339Nano)))
	return fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: at}
}

// TestFaultUncertainDestructionIsNeverReportedDestroyed drives server.destroy
// through unclear provider answers. Destruction may be recorded only after
// the exact provider inspection confirms the instance absent; anything less
// must leave the server and its managed-VM slot undestroyed, and destruction
// must never be sent twice.
func TestFaultUncertainDestructionIsNeverReportedDestroyed(t *testing.T) {
	timeout := fakeadapter.Reply{Err: simulation.ErrTimeout}
	active, destroyed := destroyInspection{state: "active"}, destroyInspection{state: "destroyed"}
	tests := []struct {
		name        string
		destroy     fakeadapter.Reply
		inspections []destroyInspection
		want        destroyFaultState
	}{
		{name: "destroy timed out and the instance is still active", destroy: timeout, inspections: []destroyInspection{active, active},
			want: destroyFaultState{Operation: protocolstate.OperationNeedsOperator, ErrorCode: "core.reconciliation.operator_required",
				DestroyCalls: 1, Lifecycle: protocolstate.ServerDestroying, Capacity: store.CapacityDestroyOutcomeUnknown}},
		{name: "destroy timed out and the provider inspection failed", destroy: timeout,
			inspections: []destroyInspection{active, {err: simulation.ErrNetwork}},
			want: destroyFaultState{Operation: protocolstate.OperationNeedsOperator, ErrorCode: "core.reconciliation.operator_required",
				DestroyCalls: 1, Lifecycle: protocolstate.ServerDestroying, Capacity: store.CapacityDestroyOutcomeUnknown}},
		{name: "destroy timed out and the instance is still destroying", destroy: timeout, inspections: []destroyInspection{active, {state: "destroying"}},
			want: destroyFaultState{Operation: protocolstate.OperationWaiting, DestroyCalls: 1,
				Lifecycle: protocolstate.ServerDestroying, Capacity: store.CapacityDestroyOutcomeUnknown}},
		{name: "provider claimed destroyed but the absence check still sees the instance", destroy: completedDestroyReply(),
			inspections: []destroyInspection{active, active},
			want: destroyFaultState{Operation: protocolstate.OperationFailed, ErrorCode: "core.workflow.postcondition_unmet",
				DestroyCalls: 1, Lifecycle: protocolstate.ServerFailed, Capacity: store.CapacityDestroyOutcomeUnknown}},
		{name: "destroy timed out and the provider confirms absence", destroy: timeout, inspections: []destroyInspection{active, destroyed, destroyed},
			want: destroyFaultState{Operation: protocolstate.OperationSucceeded, DestroyCalls: 1,
				Lifecycle: protocolstate.ServerDestroyed, Capacity: store.CapacityDestroyed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServerDestroyFixture(t)
			scriptDestroyFault(t, fixture, test.destroy, test.inspections...)
			if _, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID); err != nil {
				t.Fatal(err)
			}
			if got := destroyFaultSnapshot(t, fixture); got != test.want {
				t.Errorf("destroy outcome:\n  got  %+v\n  want %+v", got, test.want)
			}
		})
	}
}

// faultBackupResolver answers the destroy workflow's backup lookups with a
// fixed backup or error.
type faultBackupResolver struct {
	backup VerifiedServerBackup
	err    error
}

func (r *faultBackupResolver) ResolveVerifiedServerBackup(context.Context, store.LogicalServer, string, string) (VerifiedServerBackup, error) {
	return r.backup, r.err
}

// advanceDestroyThroughInspections advances the destroy operation until both
// read-only inspections succeeded, so the next step is provider destruction.
func advanceDestroyThroughInspections(t *testing.T, fixture serverDestroyFixture) {
	t.Helper()
	id := fixture.operation.Record.OperationID
	for range 6 {
		stored, err := fixture.engine.Advance(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if operationStep(stored.Record, destroyInspectRuntimeStep).Status == protocolstate.StepSucceeded {
			return
		}
	}
	t.Fatal("destroy inspections did not complete")
}

// TestFaultBackupThatIsNotVerifiedBlocksDestruction withdraws, fails or
// stales the verified backup after the destroy plan was accepted and its
// inspections passed. The immediate pre-destruction check must refuse to
// send destruction and leave the server untouched.
func TestFaultBackupThatIsNotVerifiedBlocksDestruction(t *testing.T) {
	tests := []struct {
		name   string
		change func(*faultBackupResolver)
	}{
		{name: "backup integrity is no longer verified", change: func(r *faultBackupResolver) { r.backup.IntegrityVerified = false }},
		{name: "backup verification is uncertain", change: func(r *faultBackupResolver) { r.err = errors.New("storage readback unavailable") }},
		{name: "backup belongs to an older data generation", change: func(r *faultBackupResolver) { r.backup.DataGenerationID = "generation-stale" }},
		{name: "backup was taken from another instance", change: func(r *faultBackupResolver) {
			r.backup.SourceInstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"other-vm"}`)
		}},
	}
	want := destroyFaultState{Operation: protocolstate.OperationNeedsOperator, ErrorCode: "core.safety.precondition_unmet",
		Lifecycle: protocolstate.ServerStopped, Capacity: store.CapacityActive}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServerDestroyFixture(t)
			scriptDestroyFault(t, fixture, completedDestroyReply(), destroyInspection{state: "active"}, destroyInspection{state: "destroyed"})
			advanceDestroyThroughInspections(t, fixture)
			resolver := &faultBackupResolver{backup: fixture.backups.backup}
			test.change(resolver)
			fixture.workflow.backups = resolver

			stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if got := destroyFaultSnapshot(t, fixture); got != want {
				t.Errorf("destroy with an unverified backup:\n  got  %+v\n  want %+v", got, want)
			}
			if step := operationStep(stored.Record, destroyProviderStep); step.Status != protocolstate.StepPending || step.Attempt != 0 {
				t.Errorf("provider destroy step = %s/%d, want pending/0", step.Status, step.Attempt)
			}
		})
	}
}
