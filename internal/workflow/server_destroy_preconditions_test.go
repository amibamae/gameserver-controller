package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests call CheckPreconditions directly on in-memory operation states,
// so each rule that guards a destroy step is pinned by the reason it gives.

const otherDestroyInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"other-vm"}`

// destroyOperationAfter returns the accepted destroy operation with its first
// done steps succeeded, each with a fresh recorded output.
func destroyOperationAfter(fixture serverDestroyFixture, done int) store.OperationRecord {
	operation := fixture.operation.Record
	operation.Steps = append([]store.OperationStep(nil), operation.Steps...)
	started := time.Now().UTC()
	outputs := []json.RawMessage{
		instanceInspectionOutput(json.RawMessage(startTestInstance), "stopped", started),
		json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, started.Format(time.RFC3339Nano))),
	}
	for index := 0; index < done; index++ {
		operation.Steps[index].Status = protocolstate.StepSucceeded
		operation.Steps[index].Attempt = 1
		operation.Steps[index].StartedAt = &started
		operation.Steps[index].Output = outputs[index]
	}
	return operation
}

func setStepInput(t *testing.T, step *store.OperationStep, field string, value json.RawMessage) {
	t.Helper()
	var input map[string]json.RawMessage
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatal(err)
	}
	input[field] = value
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	step.Input = encoded
}

func wantDestroyPrecondition(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("CheckPreconditions = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("CheckPreconditions = %v, want an error containing %q", err, want)
	}
}

func TestServerDestroyPreconditionsBeforeProviderDestruction(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	tests := []struct {
		name string
		ctx  context.Context
		// done is how many steps have succeeded; step is the one checked.
		done, step int
		change     func(*serverDestroyFixture, *store.OperationRecord, *store.OperationStep)
		want       string
	}{
		{"initial inspection", context.Background(), 0, 0, nil, ""},
		{"missing context", missing, 0, 0, nil, "server.destroy operation and context are required"},
		{"another operation type", context.Background(), 0, 0, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.OperationType = "server.stop"
		}, "server.destroy operation and context are required"},
		{"another protocol version", context.Background(), 0, 0, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.ProtocolVersion = "0.0.1"
		}, "server.destroy operation and context are required"},
		{"ended context", ended, 0, 0, nil, context.Canceled.Error()},
		{"unknown server", context.Background(), 0, 0, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Target.ServerID = "server-missing"
		}, "logical server not found"},
		{"target instance changed", context.Background(), 0, 0, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
		}, "operation target does not match the current logical-server instance"},
		{"plan bound to another instance", context.Background(), 0, 0, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			setStepInput(t, &o.Steps[3], "instance_ref", json.RawMessage(otherDestroyInstance))
		}, "plan is not bound to the current managed instance"},
		{"backup no longer verified", context.Background(), 0, 0, func(f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) {
			f.backups.backup.IntegrityVerified = false
		}, "verified backup does not bind the exact server"},
		{"initial inspection already running", context.Background(), 0, 0, func(_ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) {
			s.Status = protocolstate.StepRunning
		}, "requires a stopped or failed server and exact initial provider inspection"},
		{"initial inspection of another instance", context.Background(), 0, 0, func(_ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) {
			setStepInput(t, s, "instance_ref", json.RawMessage(otherDestroyInstance))
		}, "requires a stopped or failed server and exact initial provider inspection"},
		{"runtime inspection", context.Background(), 1, 1, nil, ""},
		{"runtime inspection before the provider inspection", context.Background(), 0, 1, nil, "requires a successful exact-provider inspection"},
		{"provider reported the instance destroyed", context.Background(), 1, 1, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[0].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", time.Now().UTC())
		}, "requires a successful exact-provider inspection"},
		{"provider inspected another instance", context.Background(), 1, 1, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[0].Output = instanceInspectionOutput(json.RawMessage(otherDestroyInstance), "active", time.Now().UTC())
		}, "requires a successful exact-provider inspection"},
		{"stale provider inspection", context.Background(), 1, 1, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[0].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "active", time.Now().UTC().Add(-time.Hour))
		}, "requires a successful exact-provider inspection"},
		{"destroy before the runtime inspection", context.Background(), 1, 2, nil, "requires successful exact-instance and runtime inspections"},
		{"destroy step bound to another instance", context.Background(), 2, 2, func(_ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) {
			setStepInput(t, s, "instance_ref", json.RawMessage(otherDestroyInstance))
		}, "requires successful exact-instance and runtime inspections"},
		{"runtime still running", context.Background(), 2, 2, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, time.Now().UTC().Format(time.RFC3339Nano)))
		}, "exact execution unit to be stopped or absent"},
		{"runtime inspected another unit", context.Background(), 2, 2, func(_ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"unit","external_id":"other-unit"},"state":"stopped","observed_at":%q}`,
				time.Now().UTC().Format(time.RFC3339Nano)))
		}, "exact execution unit to be stopped or absent"},
		{"approval revoked before the provider call", context.Background(), 2, 2, func(f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) {
			f.authorizer.err = errors.New("approval revoked")
		}, "revalidate exact server.destroy approval before provider call: approval revoked"},
		{"verify absence before any destroy", context.Background(), 2, 3, nil, "requires a dispatched destroy and destroying lifecycle state"},
		{"unknown step", context.Background(), 2, 2, func(_ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) {
			s.StepID = "reboot-instance"
		}, `unsupported server.destroy step "reboot-instance"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServerDestroyFixture(t)
			operation := destroyOperationAfter(fixture, test.done)
			step := operation.Steps[test.step]
			if test.change != nil {
				test.change(&fixture, &operation, &step)
			}
			wantDestroyPrecondition(t, fixture.workflow.CheckPreconditions(test.ctx, operation, step), test.want)
		})
	}
}

func TestServerDestroyPreconditionClaimsCapacityBeforeTheProviderCall(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	operation := destroyOperationAfter(fixture, 2)
	if err := fixture.workflow.CheckPreconditions(context.Background(), operation, operation.Steps[2]); err != nil {
		t.Fatalf("destroy precondition = %v, want nil", err)
	}
	if fixture.authorizer.calls != 2 {
		t.Fatalf("approval checks = %d, want planning and the provider precondition", fixture.authorizer.calls)
	}
	reservation, err := fixture.base.store.GetCapacityReservation("start-test-capacity")
	if err != nil || reservation.State != store.CapacityDestroyOutcomeUnknown {
		t.Fatalf("reservation = %#v, %v; want it claimed as destroy outcome unknown", reservation, err)
	}
	// A retried precondition finds the slot already claimed and still passes.
	if err := fixture.workflow.CheckPreconditions(context.Background(), operation, operation.Steps[2]); err != nil {
		t.Fatalf("retried destroy precondition = %v, want nil", err)
	}
}

func TestServerDestroyPreconditionsWhileDestroying(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	scriptAcceptedDestroy(t, fixture)
	waiting, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil || waiting.Record.Status != protocolstate.OperationWaiting {
		t.Fatalf("accepted destroy = %s, %v; want waiting", waiting.Record.Status, err)
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerDestroying {
		t.Fatalf("server = %s, %v; want destroying", server.Record.LifecycleState, err)
	}
	completedDestroy := func(outcome, state, instance string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":%q,"state":%q,"observed_at":%q}`, instance, outcome, state, time.Now().UTC().Format(time.RFC3339Nano)))
	}
	tests := []struct {
		name   string
		step   int
		change func(*store.OperationRecord, *store.OperationStep)
		want   string
	}{
		{"retried destroy from the destroying state", 2, nil, ""},
		{"new destroy dispatch while destroying", 2, func(_ *store.OperationRecord, s *store.OperationStep) { s.Attempt = 0 }, "cannot start from an unexplained destroying state"},
		{"runtime inspection while destroying", 1, nil, "runtime inspection requires a stopped or failed server"},
		{"initial inspection while destroying", 0, func(_ *store.OperationRecord, s *store.OperationStep) { s.Status = protocolstate.StepPending }, "requires a stopped or failed server"},
		{"verify absence before the destroy succeeded", 3, nil, "requires a dispatched destroy and destroying lifecycle state"},
		{"verify absence after a completed destroy", 3, func(o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[2].Status, o.Steps[2].Output = protocolstate.StepSucceeded, completedDestroy("completed", "destroyed", startTestInstance)
		}, ""},
		{"verify absence after an accepted destroy", 3, func(o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[2].Status, o.Steps[2].Output = protocolstate.StepSucceeded, completedDestroy("accepted", "destroyed", startTestInstance)
		}, "requires the exact completed destroy output"},
		{"verify absence while the instance is still destroying", 3, func(o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[2].Status, o.Steps[2].Output = protocolstate.StepSucceeded, completedDestroy("completed", "destroying", startTestInstance)
		}, "requires the exact completed destroy output"},
		{"verify absence of another instance", 3, func(o *store.OperationRecord, _ *store.OperationStep) {
			o.Steps[2].Status, o.Steps[2].Output = protocolstate.StepSucceeded, completedDestroy("completed", "destroyed", otherDestroyInstance)
		}, "requires the exact completed destroy output"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := waiting.Record
			operation.Steps = append([]store.OperationStep(nil), waiting.Record.Steps...)
			step := operation.Steps[test.step]
			if test.change != nil {
				test.change(&operation, &step)
			}
			wantDestroyPrecondition(t, fixture.workflow.CheckPreconditions(context.Background(), operation, step), test.want)
		})
	}
}
