package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/simulation"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const provisionLostVM = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-lost-response"}`

// acceptProvisionForFaults accepts the planned provision with its capacity
// slot reserved and returns an engine for it.
func acceptProvisionForFaults(t *testing.T, f provisionPlanFixture) *Engine {
	t.Helper()
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	f.operation.Steps = steps
	if _, _, err := f.store.AcceptOperation(f.operation, testProof(46), []string{store.LogicalServerLockScope(provisionTestEnv, provisionTestServer)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ReserveCapacity(store.CapacityReservation{ProviderAdapterID: "hosting.fake", OperationID: f.operation.OperationID,
		ServerID: provisionTestServer, EnvironmentID: provisionTestEnv, CapacityGroup: f.profile.CapacityGroup, Provider: f.profile.Provider,
		ResourceKey: provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)}, f.profile.CapacityLimit, f.profile.CapacityGroupLimit); err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.provision", f.workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(f.store, f.registry, f.schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// provisionLookupReply is the provider inventory answer for the create
// step's resource key.
func provisionLookupReply(t *testing.T, f provisionPlanFixture, complete bool, matches ...string) fakeadapter.Reply {
	t.Helper()
	found := make([]any, 0, len(matches))
	for _, match := range matches {
		found = append(found, map[string]any{"instance_ref": json.RawMessage(match), "state": "creating"})
	}
	output, err := json.Marshal(map[string]any{"resource_key": provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint),
		"inventory_complete": complete, "matches": found, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	return fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: time.Now().UTC()}
}

// provisionCallCount counts the hosting calls to capability.
func provisionCallCount(f provisionPlanFixture, capability string) int {
	count := 0
	for _, call := range f.hosting.Calls() {
		if call.Params.Capability == capability {
			count++
		}
	}
	return count
}

// provisionFaultState is what the provision fault test pins.
type provisionFaultState struct {
	Operation   protocolstate.OperationStatus
	Step        protocolstate.StepStatus
	ErrorCode   string
	Creates     int
	Lookups     int
	Capacity    store.CapacityState
	InstanceRef string
}

func provisionFaultSnapshot(t *testing.T, f provisionPlanFixture, stored store.StoredOperation) provisionFaultState {
	t.Helper()
	reservation, err := f.store.GetCapacityReservation(f.operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	create := operationStep(stored.Record, provisionCreateStep)
	return provisionFaultState{Operation: stored.Record.Status, Step: create.Status, ErrorCode: errorCode(stored.Record.Error),
		Creates: provisionCallCount(f, "hosting.instance.create"), Lookups: provisionCallCount(f, "hosting.instance.find_by_resource_key"),
		Capacity: reservation.State, InstanceRef: string(stepField(create.Output, "instance_ref"))}
}

// TestFaultLostCreateResponseIsReconciledByResourceKey loses the provider's
// answer to a VM creation. The controller must record the outcome as
// unknown, keep the capacity slot claimed, and then look the VM up by its
// resource key instead of creating again. Only one exact match completes
// the step; an incomplete, empty or failed lookup goes to an operator.
func TestFaultLostCreateResponseIsReconciledByResourceKey(t *testing.T) {
	needsOperator := provisionFaultState{Operation: protocolstate.OperationNeedsOperator, Step: protocolstate.StepOutcomeUnknown,
		ErrorCode: "core.reconciliation.operator_required", Creates: 1, Lookups: 1, Capacity: store.CapacityCreateOutcomeUnknown}
	tests := []struct {
		name   string
		lookup func(*testing.T, provisionPlanFixture) fakeadapter.Reply
		want   provisionFaultState
	}{
		{name: "the VM was created and is found once", lookup: func(t *testing.T, f provisionPlanFixture) fakeadapter.Reply {
			return provisionLookupReply(t, f, true, provisionLostVM)
		}, want: provisionFaultState{Operation: protocolstate.OperationRunning, Step: protocolstate.StepSucceeded, Creates: 1, Lookups: 1,
			Capacity: store.CapacityCreateOutcomeUnknown, InstanceRef: provisionLostVM}},
		{name: "the provider inventory is incomplete", lookup: func(t *testing.T, f provisionPlanFixture) fakeadapter.Reply {
			return provisionLookupReply(t, f, false, provisionLostVM)
		}, want: needsOperator},
		{name: "no VM is found", lookup: func(t *testing.T, f provisionPlanFixture) fakeadapter.Reply {
			return provisionLookupReply(t, f, true)
		}, want: needsOperator},
		{name: "the lookup itself times out", lookup: func(*testing.T, provisionPlanFixture) fakeadapter.Reply {
			return fakeadapter.Reply{Err: simulation.ErrTimeout}
		}, want: needsOperator},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newProvisionPlanFixture(t)
			engine := acceptProvisionForFaults(t, f)
			scripts := map[string][]fakeadapter.Reply{
				"hosting.instance.create":               {{Err: simulation.ErrTimeout}, {Err: simulation.ErrTimeout}},
				"hosting.instance.find_by_resource_key": {test.lookup(t, f)},
			}
			for capability, replies := range scripts {
				if err := f.hosting.SetScript(capability, replies...); err != nil {
					t.Fatal(err)
				}
			}

			unknown, err := engine.Advance(context.Background(), f.operation.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			wantUnknown := provisionFaultState{Operation: protocolstate.OperationReconciling, Step: protocolstate.StepOutcomeUnknown,
				ErrorCode: "core.adapter.outcome_unknown", Creates: 1, Capacity: store.CapacityCreateOutcomeUnknown}
			if got := provisionFaultSnapshot(t, f, unknown); got != wantUnknown {
				t.Errorf("after the lost response:\n  got  %+v\n  want %+v", got, wantUnknown)
			}
			reconciled, err := engine.Advance(context.Background(), f.operation.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if got := provisionFaultSnapshot(t, f, reconciled); got != test.want {
				t.Errorf("after reconciliation:\n  got  %+v\n  want %+v", got, test.want)
			}
		})
	}
}
