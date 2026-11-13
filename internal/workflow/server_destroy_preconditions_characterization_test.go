package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the exact error text and check order of
// server.destroy CheckPreconditions, including the rules the substring table
// in server_destroy_preconditions_test.go does not reach.

// destroyStoreOverride serves the fixture store but can rewrite the loaded
// logical server or fail the capacity listing.
type destroyStoreOverride struct {
	*store.BoltStore
	server  func(store.StoredLogicalServer) store.StoredLogicalServer
	listErr error
}

func (s *destroyStoreOverride) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	stored, err := s.BoltStore.GetLogicalServer(environmentID, serverID)
	if err == nil && s.server != nil {
		stored = s.server(stored)
	}
	return stored, err
}

func (s *destroyStoreOverride) ListCapacityReservations() ([]store.CapacityReservation, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.BoltStore.ListCapacityReservations()
}

func destroyWorkflowOverStore(fixture serverDestroyFixture, servers *destroyStoreOverride) *ServerDestroyWorkflow {
	workflow := *fixture.workflow
	servers.BoltStore = fixture.base.store
	workflow.servers = servers
	return &workflow
}

func destroyWithLifecycle(state protocolstate.ServerState) *destroyStoreOverride {
	return &destroyStoreOverride{server: func(stored store.StoredLogicalServer) store.StoredLogicalServer {
		stored.Record.LifecycleState = state
		return stored
	}}
}

func destroyCompletedProviderStep(operation *store.OperationRecord, outcome, state, instance string) {
	started := time.Now().UTC()
	operation.Steps[2].Status = protocolstate.StepSucceeded
	operation.Steps[2].Attempt = 1
	operation.Steps[2].StartedAt = &started
	operation.Steps[2].Output = json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":%q,"state":%q,"observed_at":%q}`,
		instance, outcome, state, started.Format(time.RFC3339Nano)))
}

func TestServerDestroyPreconditionsExactMessages(t *testing.T) {
	const (
		required      = "server.destroy operation and context are required"
		parameters    = "server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID"
		unbound       = "server.destroy plan is not bound to the current managed instance"
		backup        = "verified backup does not bind the exact server, environment, storage profile, instance, and current data generation"
		provider      = "server.destroy requires a successful exact-provider inspection in active or stopped state"
		initial       = "server.destroy requires a stopped or failed server and exact initial provider inspection"
		runtime       = "runtime inspection requires a stopped or failed server and successful provider inspection"
		lifecycle     = "provider destruction requires a stopped, failed, or already-destroying server"
		inspections   = "provider destruction requires successful exact-instance and runtime inspections"
		unitStopped   = "provider destruction requires the exact execution unit to be stopped or absent"
		unexplained   = "new destroy dispatch cannot start from an unexplained destroying state"
		dispatched    = "provider absence verification requires a dispatched destroy and destroying lifecycle state"
		exactComplete = "provider absence verification requires the exact completed destroy output"
	)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	type change func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride
	tests := []struct {
		name       string
		ctx        context.Context
		done, step int
		change     change
		want       string
		// authorized is the number of approval checks the precondition adds.
		authorized int
	}{
		{name: "initial inspection", done: 0, step: 0},
		{name: "runtime inspection", done: 1, step: 1},
		{name: "provider destruction", done: 2, step: 2, authorized: 1},
		{name: "nil workflow is rejected first", done: 0, step: 0, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return nil
		}, want: required},
		{name: "another operation type", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.OperationType = "server.stop"
			return nil
		}, want: required},
		{name: "ended context", ctx: ended, done: 0, step: 0, want: context.Canceled.Error()},
		{name: "snapshot for another operation type", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.RequestSnapshot.OperationType = "server.stop"
			return nil
		}, want: "server.destroy operation and loaded protocol version are required"},
		{name: "undecodable parameters", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.RequestSnapshot.Parameters = json.RawMessage(`{"approval_id":7}`)
			return nil
		}, want: "decode server.destroy parameters"},
		{name: "parameters without an approval", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.RequestSnapshot.Parameters = json.RawMessage(fmt.Sprintf(`{"expected_instance_ref":%s,"backup_profile":%q,"verified_backup_id":%q}`,
				startTestInstance, destroyTestProfile, destroyTestBackupID))
			return nil
		}, want: parameters},
		{name: "unknown server", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Target.ServerID = "server-missing"
			return nil
		}, want: store.ErrLogicalServerNotFound.Error()},
		{name: "target instance changed", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
			return nil
		}, want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "unknown data generation", done: 0, step: 0, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return &destroyStoreOverride{server: func(stored store.StoredLogicalServer) store.StoredLogicalServer {
				stored.Record.DataGenerationStatus = "unknown"
				return stored
			}}
		}, want: "server.destroy requires a known current data generation"},
		{name: "initial inspection plan unbound", done: 0, step: 0, change: func(t *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			setStepInput(t, &o.Steps[0], "instance_ref", json.RawMessage(otherDestroyInstance))
			return nil
		}, want: unbound},
		{name: "destroy plan unbound", done: 0, step: 0, change: func(t *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			setStepInput(t, &o.Steps[2], "instance_ref", json.RawMessage(otherDestroyInstance))
			return nil
		}, want: unbound},
		{name: "unbound plan is reported before the backup", done: 0, step: 0, change: func(t *testing.T, f *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			setStepInput(t, &o.Steps[3], "instance_ref", json.RawMessage(otherDestroyInstance))
			f.backups.backup.IntegrityVerified = false
			return nil
		}, want: unbound},
		{name: "backup from another generation", done: 2, step: 2, change: func(_ *testing.T, f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			f.backups.backup.DataGenerationID = "stale-generation"
			return nil
		}, want: backup},
		{name: "backup is reported before the provider inspection", done: 0, step: 1, change: func(_ *testing.T, f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			f.backups.backup.IntegrityVerified = false
			return nil
		}, want: backup},
		{name: "unknown step before the provider inspection", done: 0, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) *destroyStoreOverride {
			s.StepID = "reboot-instance"
			return nil
		}, want: provider},
		{name: "unknown step after the provider inspection", done: 1, step: 1, change: func(_ *testing.T, _ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) *destroyStoreOverride {
			s.StepID = "reboot-instance"
			return nil
		}, want: `unsupported server.destroy step "reboot-instance"`},
		{name: "initial inspection ignores a missing provider inspection", done: 0, step: 0},
		{name: "provider inspection reported failed", done: 1, step: 1, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[0].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "failed", time.Now().UTC())
			return nil
		}, want: provider},
		{name: "provider inspection not succeeded", done: 1, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[0].Status = protocolstate.StepRunning
			return nil
		}, want: provider},
		{name: "initial inspection on a running server", done: 0, step: 0, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerReady)
		}, want: initial},
		{name: "initial inspection on a failed server", done: 0, step: 0, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerFailed)
		}},
		{name: "initial inspection already succeeded", done: 1, step: 0, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, s *store.OperationStep) *destroyStoreOverride {
			*s = o.Steps[0]
			return nil
		}, want: initial},
		{name: "runtime inspection on a running server", done: 1, step: 1, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerReady)
		}, want: runtime},
		{name: "provider destruction on a running server", done: 2, step: 2, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerReady)
		}, want: lifecycle},
		{name: "provider destruction on a failed server", done: 2, step: 2, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerFailed)
		}, authorized: 1},
		{name: "lifecycle is reported before the runtime inspection", done: 1, step: 2, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerReady)
		}, want: lifecycle},
		{name: "provider destruction before the runtime inspection", done: 1, step: 2, want: inspections},
		{name: "provider destruction step unbound", done: 2, step: 2, change: func(t *testing.T, _ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) *destroyStoreOverride {
			setStepInput(t, s, "instance_ref", json.RawMessage(otherDestroyInstance))
			return nil
		}, want: inspections},
		{name: "runtime unit absent", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"absent","observed_at":%q}`, startTestUnit, time.Now().UTC().Format(time.RFC3339Nano)))
			return nil
		}, authorized: 1},
		{name: "runtime unit running", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, time.Now().UTC().Format(time.RFC3339Nano)))
			return nil
		}, want: unitStopped},
		{name: "runtime observation stale", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)))
			return nil
		}, want: unitStopped},
		{name: "runtime planned for another unit", done: 2, step: 2, change: func(t *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			setStepInput(t, &o.Steps[1], "unit_ref", json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"other-unit"}`))
			return nil
		}, want: unitStopped},
		{name: "new dispatch from destroying", done: 2, step: 2, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: unexplained},
		{name: "retried dispatch from destroying", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, _ *store.OperationRecord, s *store.OperationStep) *destroyStoreOverride {
			s.Attempt = 1
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, authorized: 1},
		{name: "runtime is reported before an unexplained destroying state", done: 2, step: 2, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			o.Steps[1].Output = json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, time.Now().UTC().Format(time.RFC3339Nano)))
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: unitStopped},
		{name: "approval revoked", done: 2, step: 2, change: func(_ *testing.T, f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			f.authorizer.err = errors.New("approval revoked")
			return nil
		}, want: "revalidate exact server.destroy approval before provider call: approval revoked", authorized: 1},
		{name: "unexplained destroying state is reported before the approval", done: 2, step: 2, change: func(_ *testing.T, f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			f.authorizer.err = errors.New("approval revoked")
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: unexplained},
		{name: "capacity listing fails after the approval", done: 2, step: 2, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return &destroyStoreOverride{listErr: errors.New("capacity store unavailable")}
		}, want: "list managed VM capacity reservations: capacity store unavailable", authorized: 1},
		{name: "approval is reported before the capacity claim", done: 2, step: 2, change: func(_ *testing.T, f *serverDestroyFixture, _ *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			f.authorizer.err = errors.New("approval revoked")
			return &destroyStoreOverride{listErr: errors.New("capacity store unavailable")}
		}, want: "revalidate exact server.destroy approval before provider call: approval revoked", authorized: 1},
		{name: "verify absence on a stopped server", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "completed", "destroyed", startTestInstance)
			return nil
		}, want: dispatched},
		{name: "verify absence before the destroy succeeded", done: 2, step: 3, change: func(*testing.T, *serverDestroyFixture, *store.OperationRecord, *store.OperationStep) *destroyStoreOverride {
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: dispatched},
		{name: "verify absence after a completed destroy", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "completed", "destroyed", startTestInstance)
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}},
		{name: "verify absence after an accepted destroy", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "accepted", "destroyed", startTestInstance)
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: exactComplete},
		{name: "verify absence while still destroying", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "completed", "destroying", startTestInstance)
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: exactComplete},
		{name: "verify absence of another instance", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "completed", "destroyed", otherDestroyInstance)
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: exactComplete},
		{name: "verify absence with an undecodable destroy output", done: 2, step: 3, change: func(_ *testing.T, _ *serverDestroyFixture, o *store.OperationRecord, _ *store.OperationStep) *destroyStoreOverride {
			destroyCompletedProviderStep(o, "completed", "destroyed", startTestInstance)
			o.Steps[2].Output = json.RawMessage(`{"instance_ref":`)
			return destroyWithLifecycle(protocolstate.ServerDestroying)
		}, want: exactComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newServerDestroyFixture(t)
			operation := destroyOperationAfter(fixture, test.done)
			step := operation.Steps[test.step]
			workflow := fixture.workflow
			if test.change != nil {
				if servers := test.change(t, &fixture, &operation, &step); servers != nil {
					workflow = destroyWorkflowOverStore(fixture, servers)
				}
			}
			if test.name == "nil workflow is rejected first" {
				workflow = nil
			}
			ctx := test.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			before := fixture.authorizer.calls
			err := workflow.CheckPreconditions(ctx, operation, step)
			if test.want == "" && err != nil {
				t.Fatalf("CheckPreconditions = %v, want nil", err)
			}
			if test.want != "" && (err == nil || err.Error() != test.want) {
				t.Fatalf("CheckPreconditions = %v, want exactly %q", err, test.want)
			}
			if got := fixture.authorizer.calls - before; got != test.authorized {
				t.Fatalf("approval checks = %d, want %d", got, test.authorized)
			}
		})
	}
}

func TestServerDestroyPreconditionsMissingContext(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	operation := destroyOperationAfter(fixture, 0)
	var missing context.Context
	err := fixture.workflow.CheckPreconditions(missing, operation, operation.Steps[0])
	if err == nil || err.Error() != "server.destroy operation and context are required" {
		t.Fatalf("CheckPreconditions = %v, want the required-context error", err)
	}
}
