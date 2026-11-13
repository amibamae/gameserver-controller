package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests call ServerBackupWorkflow.CheckPreconditions directly on an
// in-memory backup whose earlier steps carry valid recorded outputs, and pin
// the reason each step's evidence rule rejects a broken copy.

const (
	backupTestUnit     = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`
	backupTestInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`
	backupOtherUnit    = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-2"}`
	backupOtherVM      = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-2"}`
	backupTestDigest   = "4e07408562bedb8b60ce05c1decfe3ad16b72230967de01f640b7e4729b49fce"
)

// scriptedBackupStreams verifies every stream unless err is set.
type scriptedBackupStreams struct {
	noopServerBackupTransferBroker
	err error
}

func (s scriptedBackupStreams) VerifyStepStreams(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation) error {
	return s.err
}

type backupPreconditionFixture struct {
	workflow  *ServerBackupWorkflow
	operation store.OperationRecord
	server    store.LogicalServer
}

// newBackupPreconditionFixture plans a backup of a ready server, resuming it
// afterwards when resume is set.
func newBackupPreconditionFixture(t *testing.T, resume bool) backupPreconditionFixture {
	t.Helper()
	workflow, operation := newBackupPlanFixture(t, resume)
	steps, err := workflow.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	operation.BackupInitialLifecycleState = protocolstate.ServerReady
	stored, err := workflow.servers.GetLogicalServer("staging", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	return backupPreconditionFixture{workflow: workflow, operation: operation, server: stored.Record}
}

func backupNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func backupGameTarget() json.RawMessage {
	return mustJSON(map[string]any{"server_id": "server-1", "environment_id": "staging", "instance_ref": json.RawMessage(backupTestInstance)})
}

func backupJSON(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// succeedBackupStep records a successful output for one step.
func succeedBackupStep(operation *store.OperationRecord, stepID string, output json.RawMessage) {
	started := time.Now().UTC()
	for i := range operation.Steps {
		if operation.Steps[i].StepID == stepID {
			operation.Steps[i].Status = protocolstate.StepSucceeded
			operation.Steps[i].Attempt = 1
			operation.Steps[i].StartedAt = &started
			operation.Steps[i].Output = output
			return
		}
	}
	panic("no step " + stepID)
}

func backupStepIndex(operation store.OperationRecord, stepID string) int {
	for i, step := range operation.Steps {
		if step.StepID == stepID {
			return i
		}
	}
	panic("no step " + stepID)
}

// completeBackupThrough records valid outputs for every step before stepID.
func completeBackupThrough(t *testing.T, operation *store.OperationRecord, stepID string) {
	t.Helper()
	capture := backupDataStepID("capture", "world")
	upload := backupDataStepID("upload", "world")
	readback := backupDataStepID("readback", "world")
	backupID := operation.OperationID
	objectRef := json.RawMessage(`{"adapter_id":"storage.fake","container":"backups","environment_id":"staging","key":"server-1/` + backupID + `/world","storage_profile_id":"storage-profile","version_id":"v1"}`)
	outputs := []struct {
		id     string
		output func() json.RawMessage
	}{
		{backupInspectInstanceStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"instance_ref": json.RawMessage(backupTestInstance), "state": "active", "observed_at": backupNow()})
		}},
		{backupInspectRuntimeStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupTestUnit), "state": "running", "observed_at": backupNow()})
		}},
		{backupDescribeDataStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"target": backupGameTarget(), "release_id": "paper-release", "observed_at": backupNow(),
				"data_sets": []any{map[string]any{"data_set_id": "world", "relative_paths": []string{"world"}, "required": true, "supported_consistency_modes": []string{"offline"}}}})
		}},
		{backupSaveGameStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"target": backupGameTarget(), "release_id": "paper-release", "outcome": "confirmed", "observed_at": backupNow(), "saved_at": backupNow()})
		}},
		{backupStopRuntimeStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupTestUnit), "outcome": "completed", "state": "stopped", "observed_at": backupNow()})
		}},
		{backupPrepareStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"target": backupGameTarget(), "release_id": "paper-release", "backup_id": backupID,
				"data_generation_id": "generation-1", "data_set_ids": []string{"world"}, "consistency_mode": "offline", "outcome": "prepared",
				"preparation_id": "preparation-1", "consistency_evidence": "runtime stopped", "observed_at": backupNow()})
		}},
		{capture, func() json.RawMessage {
			return backupJSON(t, map[string]any{"target": backupGameTarget(), "release_id": "paper-release", "backup_id": backupID,
				"data_generation_id": "generation-1", "preparation_id": "preparation-1", "data_set_id": "world",
				"capture_id": backupCaptureID(backupID, "world"), "consistency_mode": "offline", "outcome": "captured",
				"stream_completed": true, "size_bytes": 32, "checksum": map[string]any{"algorithm": "sha256", "digest": backupTestDigest}, "observed_at": backupNow()})
		}},
		{upload, func() json.RawMessage {
			return backupJSON(t, map[string]any{"target": stepField(operation.Steps[backupStepIndex(*operation, upload)].Input, "target"),
				"upload_state": "uploaded", "object_ref": objectRef, "bytes_received": 32, "observed_at": backupNow()})
		}},
		{readback, func() json.RawMessage {
			return backupJSON(t, map[string]any{"object_ref": objectRef,
				"transfer_state": "complete", "bytes_sent": 32, "computed_checksum": map[string]any{"algorithm": "sha256", "digest": backupTestDigest}, "observed_at": backupNow()})
		}},
		{backupValidateStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"backup_id": backupID, "environment_id": "staging", "data_generation_id": "generation-1",
				"validation_result": "valid", "observed_at": backupNow(),
				"check_results": []any{map[string]any{"check_id": "paper.backup.archive", "check_version": "1", "result": "pass", "observed_at": backupNow()}}})
		}},
		{backupStartRuntimeStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupTestUnit), "outcome": "completed", "state": "running", "observed_at": backupNow()})
		}},
		{backupVerifyRuntimeStep, func() json.RawMessage {
			return backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupTestUnit), "state": "running", "observed_at": backupNow()})
		}},
	}
	for _, step := range outputs {
		if step.id == capture {
			// The engine resolves the preparation ID into the capture input.
			index := backupStepIndex(*operation, capture)
			input := decodeObject(operation.Steps[index].Input)
			input["preparation_id"] = "preparation-1"
			operation.Steps[index].ResolvedInput = backupJSON(t, input)
		}
		if step.id == readback {
			// The engine binds the readback to the uploaded object version.
			index := backupStepIndex(*operation, readback)
			operation.Steps[index].ResolvedInput = backupJSON(t, map[string]any{"object_ref": objectRef})
		}
		if step.id == stepID {
			return
		}
		if operationStep(*operation, step.id).StepID == "" {
			continue // a step this plan does not include
		}
		succeedBackupStep(operation, step.id, step.output())
	}
	if operationStep(*operation, stepID).StepID == "" {
		t.Fatalf("the plan has no step %q", stepID)
	}
}

// checkBackupStep runs the precondition for stepID with the server in state.
func (f backupPreconditionFixture) checkBackupStep(operation store.OperationRecord, stepID string, state protocolstate.ServerState, change func(*store.LogicalServer), streams error) error {
	server := f.server
	server.LifecycleState = state
	if change != nil {
		change(&server)
	}
	workflow := *f.workflow
	workflow.servers = testBackupServerRepository{record: store.StoredLogicalServer{Record: server}}
	workflow.transfers = scriptedBackupStreams{err: streams}
	step := operation.Steps[backupStepIndex(operation, stepID)]
	return workflow.CheckPreconditions(context.Background(), operation, step)
}

func setBackupOutputField(t *testing.T, operation *store.OperationRecord, stepID, field string, value any) {
	t.Helper()
	index := backupStepIndex(*operation, stepID)
	output := decodeObject(operation.Steps[index].Output)
	if value == nil {
		delete(output, field)
	} else {
		output[field] = value
	}
	operation.Steps[index].Output = backupJSON(t, output)
}

func TestServerBackupPreconditionsBeforeTheDataSteps(t *testing.T) {
	capture := backupDataStepID("capture", "world")
	unitRef, instanceRef := json.RawMessage(backupOtherUnit), json.RawMessage(backupOtherVM)
	tests := []struct {
		name   string
		step   string
		state  protocolstate.ServerState
		change func(*testing.T, *store.OperationRecord)
		server func(*store.LogicalServer)
		want   string
	}{
		{name: "provider inspection", step: backupInspectInstanceStep, state: protocolstate.ServerReady},
		{name: "provider inspection of a degraded server", step: backupInspectInstanceStep, state: protocolstate.ServerDegraded},
		{name: "provider inspection while starting", step: backupInspectInstanceStep, state: protocolstate.ServerStarting,
			want: "provider inspection requires the original ready, degraded, or stopped lifecycle state"},
		{name: "server with a network profile", step: backupInspectInstanceStep, state: protocolstate.ServerReady,
			server: func(s *store.LogicalServer) { s.NetworkProfile = "velocity" }, want: "server.backup is blocked until configured proxy drain is integrated"},
		{name: "plan bound to another instance", step: backupInspectInstanceStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				o.Steps[0].Input = backupJSON(t, map[string]any{"instance_ref": instanceRef})
			}, want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "target of another instance", step: backupInspectInstanceStep, state: protocolstate.ServerReady,
			change: func(_ *testing.T, o *store.OperationRecord) { o.Target.InstanceRef = instanceRef }, want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "another operation type", step: backupInspectInstanceStep, state: protocolstate.ServerReady,
			change: func(_ *testing.T, o *store.OperationRecord) { o.OperationType = "server.stop" }, want: "server.backup operation and context are required"},

		{name: "runtime inspection", step: backupInspectRuntimeStep, state: protocolstate.ServerReady},
		{name: "runtime inspection before the provider inspection", step: backupInspectRuntimeStep, state: protocolstate.ServerReady,
			change: func(_ *testing.T, o *store.OperationRecord) { o.Steps[0].Status = protocolstate.StepPending },
			want:   "runtime inspection requires the exact active provider instance and original lifecycle state"},
		{name: "runtime inspection of a stopped instance", step: backupInspectRuntimeStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupInspectInstanceStep, "state", "stopped")
			},
			want: "runtime inspection requires the exact active provider instance"},
		{name: "runtime inspection after the lifecycle changed", step: backupInspectRuntimeStep, state: protocolstate.ServerDegraded,
			want: "runtime inspection requires the exact active provider instance and original lifecycle state"},

		{name: "data description", step: backupDescribeDataStep, state: protocolstate.ServerReady},
		{name: "data description of a stopped runtime", step: backupDescribeDataStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupInspectRuntimeStep, "state", "stopped")
			},
			want: "data-set description requires the exact instance, runtime state, and trusted release"},
		{name: "data description of another unit", step: backupDescribeDataStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupInspectRuntimeStep, "unit_ref", unitRef)
			},
			want: "data-set description requires the exact instance, runtime state, and trusted release"},
		{name: "data description of another release", step: backupDescribeDataStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				o.Steps[2].Input = backupJSON(t, map[string]any{"target": backupGameTarget(), "release_id": "other-release"})
			}, want: "data-set description requires the exact instance, runtime state, and trusted release"},

		{name: "game save", step: backupSaveGameStep, state: protocolstate.ServerReady},
		{name: "game save without a data description", step: backupSaveGameStep, state: protocolstate.ServerReady,
			change: func(_ *testing.T, o *store.OperationRecord) { o.Steps[2].Status = protocolstate.StepPending },
			want:   "offline backup save requires the exact ready or degraded running server and declared data sets"},
		{name: "game save with another data set", step: backupSaveGameStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupDescribeDataStep, "data_sets", []any{map[string]any{"data_set_id": "nether", "relative_paths": []string{"world"}, "required": true, "supported_consistency_modes": []string{"offline"}}})
			}, want: "offline backup save requires the exact ready or degraded running server and declared data sets"},
		{name: "game save of a stopped server", step: backupSaveGameStep, state: protocolstate.ServerReady,
			change: func(_ *testing.T, o *store.OperationRecord) {
				o.BackupInitialLifecycleState = protocolstate.ServerStopped
			},
			want: "offline backup save requires the exact ready or degraded running server"},

		{name: "runtime stop", step: backupStopRuntimeStep, state: protocolstate.ServerReady},
		{name: "runtime stop already stopping", step: backupStopRuntimeStep, state: protocolstate.ServerStopping},
		{name: "runtime stop after the lifecycle changed", step: backupStopRuntimeStep, state: protocolstate.ServerDegraded,
			want: "runtime stop requires a confirmed game save and exact ready or degraded server evidence"},
		{name: "runtime stop without a confirmed save", step: backupStopRuntimeStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupSaveGameStep, "outcome", "outcome_unknown")
			},
			want: "runtime stop requires a confirmed game save"},
		{name: "runtime stop of a save without a save time", step: backupStopRuntimeStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupSaveGameStep, "saved_at", nil)
			},
			want: "runtime stop requires a confirmed game save"},
		{name: "runtime stop bound to another unit", step: backupStopRuntimeStep, state: protocolstate.ServerReady,
			change: func(t *testing.T, o *store.OperationRecord) {
				index := backupStepIndex(*o, backupStopRuntimeStep)
				o.Steps[index].Input = backupJSON(t, map[string]any{"unit_ref": unitRef, "grace_period_seconds": 120})
			}, want: "runtime stop is not bound to the trusted execution unit"},

		{name: "preparation", step: backupPrepareStep, state: protocolstate.ServerStopped},
		{name: "preparation after the claim", step: backupPrepareStep, state: protocolstate.ServerBackingUp},
		{name: "preparation of a running server", step: backupPrepareStep, state: protocolstate.ServerReady, want: "backup preparation requires a stopped runtime"},
		{name: "preparation of an unconfirmed stop", step: backupPrepareStep, state: protocolstate.ServerStopped,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupStopRuntimeStep, "outcome", "outcome_unknown")
			},
			want: "backup preparation requires exact provider, runtime, and data-set evidence"},
		{name: "preparation without a data description", step: backupPrepareStep, state: protocolstate.ServerStopped,
			change: func(_ *testing.T, o *store.OperationRecord) { o.Steps[2].Status = protocolstate.StepPending },
			want:   "backup preparation requires exact provider, runtime, and data-set evidence"},

		{name: "capture", step: capture, state: protocolstate.ServerBackingUp},
		{name: "capture while stopped", step: capture, state: protocolstate.ServerStopped,
			want: "capture requires backing_up state and exact trusted data-set evidence"},
		{name: "capture without a preparation", step: capture, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupPrepareStep, "outcome", "rejected")
			},
			want: "capture requires an exact completed preparation for the data set"},
		{name: "capture after a preparation of other data sets", step: capture, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupPrepareStep, "data_set_ids", []string{"nether"})
			},
			want: "capture requires an exact completed preparation for the data set"},
		{name: "capture bound to another preparation", step: capture, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				index := backupStepIndex(*o, capture)
				input := decodeObject(o.Steps[index].ResolvedInput)
				input["preparation_id"] = "preparation-2"
				o.Steps[index].ResolvedInput = backupJSON(t, input)
			}, want: "capture input is not bound to the exact preparation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBackupPreconditionFixture(t, true)
			operation := f.operation
			operation.Steps = append([]store.OperationStep(nil), f.operation.Steps...)
			completeBackupThrough(t, &operation, test.step)
			if test.change != nil {
				test.change(t, &operation)
			}
			err := f.checkBackupStep(operation, test.step, test.state, test.server, nil)
			if test.want == "" {
				if err != nil {
					t.Fatalf("CheckPreconditions(%s) = %v, want nil", test.step, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CheckPreconditions(%s) = %v, want an error containing %q", test.step, err, test.want)
			}
		})
	}
}

func TestServerBackupPreconditionsForTheStorageSteps(t *testing.T) {
	upload := backupDataStepID("upload", "world")
	readback := backupDataStepID("readback", "world")
	streamFailure := errors.New("stream digest differs")
	tests := []struct {
		name    string
		step    string
		state   protocolstate.ServerState
		change  func(*testing.T, *store.OperationRecord)
		streams error
		want    string
	}{
		{name: "upload", step: upload, state: protocolstate.ServerBackingUp},
		{name: "upload while starting", step: upload, state: protocolstate.ServerStarting,
			want: "storage upload requires the server to remain stopped during backup"},
		{name: "upload of an incomplete capture", step: upload, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupDataStepID("capture", "world"), "stream_completed", false)
			}, want: "storage upload requires a complete bounded capture"},
		{name: "upload of an oversized capture", step: upload, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupDataStepID("capture", "world"), "size_bytes", 65)
			}, want: "storage upload requires a complete bounded capture"},
		{name: "upload whose capture stream fails verification", step: upload, state: protocolstate.ServerBackingUp, streams: streamFailure,
			want: "controller-side stream verification failed for " + backupDataStepID("capture", "world") + ": stream digest differs"},

		{name: "readback", step: readback, state: protocolstate.ServerBackingUp},
		{name: "readback while starting", step: readback, state: protocolstate.ServerStarting,
			want: "storage readback requires the server to remain stopped during backup"},
		{name: "readback of a partial upload", step: readback, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, upload, "upload_state", "partial")
			},
			want: "storage readback requires an exact completed upload"},
		{name: "readback of a short upload", step: readback, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) { setBackupOutputField(t, o, upload, "bytes_received", 31) },
			want:   "storage readback requires an exact completed upload"},
		{name: "readback of an object in another container", step: readback, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, upload, "object_ref", json.RawMessage(`{"adapter_id":"storage.fake","container":"other","environment_id":"staging","key":"server-1/backup-plan-true/world","storage_profile_id":"storage-profile"}`))
			}, want: "storage readback requires an exact completed upload"},
		{name: "readback whose upload stream fails verification", step: readback, state: protocolstate.ServerBackingUp, streams: streamFailure,
			want: "controller-side stream verification failed for " + upload + ": stream digest differs"},

		{name: "unknown step", step: "", state: protocolstate.ServerBackingUp, want: `unsupported server.backup step "reboot"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBackupPreconditionFixture(t, true)
			operation := f.operation
			operation.Steps = append([]store.OperationStep(nil), f.operation.Steps...)
			step := test.step
			if step == "" {
				completeBackupThrough(t, &operation, readback)
				operation.Steps = append(operation.Steps, store.OperationStep{StepID: "reboot", Status: protocolstate.StepPending})
				step = "reboot"
			} else {
				completeBackupThrough(t, &operation, step)
			}
			if test.change != nil {
				test.change(t, &operation)
			}
			err := f.checkBackupStep(operation, step, test.state, nil, test.streams)
			if test.want == "" {
				if err != nil {
					t.Fatalf("CheckPreconditions(%s) = %v, want nil", step, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CheckPreconditions(%s) = %v, want an error containing %q", step, err, test.want)
			}
		})
	}
}

func TestServerBackupPreconditionsAfterTheReadback(t *testing.T) {
	failedCheck := []any{map[string]any{"check_id": "paper.backup.archive", "check_version": "1", "result": "fail", "observed_at": "2026-11-07T09:00:00Z"}}
	tests := []struct {
		name    string
		resume  bool
		step    string
		state   protocolstate.ServerState
		change  func(*testing.T, *store.OperationRecord)
		streams error
		want    string
	}{
		{name: "validation", resume: true, step: backupValidateStep, state: protocolstate.ServerBackingUp},
		{name: "validation while starting", resume: true, step: backupValidateStep, state: protocolstate.ServerStarting,
			want: "game backup validation requires exact completed storage readbacks"},
		{name: "validation of a short readback", resume: true, step: backupValidateStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupDataStepID("readback", "world"), "bytes_sent", 31)
			}, want: "game backup validation requires exact completed storage readbacks"},
		{name: "validation of a readback with another checksum", resume: true, step: backupValidateStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupDataStepID("readback", "world"), "computed_checksum", map[string]any{"algorithm": "sha256", "digest": strings.Repeat("0", 64)})
			}, want: "game backup validation requires exact completed storage readbacks"},
		{name: "validation whose readback stream fails verification", resume: true, step: backupValidateStep, state: protocolstate.ServerBackingUp,
			streams: errors.New("stream digest differs"),
			want:    "controller-side stream verification failed for " + backupDataStepID("readback", "world")},

		{name: "runtime resume", resume: true, step: backupStartRuntimeStep, state: protocolstate.ServerBackingUp},
		{name: "runtime resume while stopped", resume: true, step: backupStartRuntimeStep, state: protocolstate.ServerStopped,
			want: "runtime resume requires a fully validated backup of the exact data generation"},
		{name: "runtime resume after a failed check", resume: true, step: backupStartRuntimeStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupValidateStep, "check_results", failedCheck)
			},
			want: "runtime resume requires a fully validated backup"},
		{name: "runtime resume after validating another generation", resume: true, step: backupStartRuntimeStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupValidateStep, "data_generation_id", "generation-0")
			}, want: "runtime resume requires a fully validated backup of the exact data generation"},
		{name: "runtime resume whose validation stream fails verification", resume: true, step: backupStartRuntimeStep, state: protocolstate.ServerBackingUp,
			streams: errors.New("stream digest differs"),
			want:    "controller-side stream verification failed for " + backupValidateStep},

		{name: "runtime verification", resume: true, step: backupVerifyRuntimeStep, state: protocolstate.ServerStarting},
		{name: "runtime verification while backing up", resume: true, step: backupVerifyRuntimeStep, state: protocolstate.ServerBackingUp,
			want: "runtime verification requires the exact completed start command"},
		{name: "runtime verification after an unconfirmed start", resume: true, step: backupVerifyRuntimeStep, state: protocolstate.ServerStarting,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupStartRuntimeStep, "state", "starting")
			},
			want: "runtime verification requires the exact completed start command"},

		{name: "readiness", resume: true, step: backupReadinessStep, state: protocolstate.ServerStarting},
		{name: "readiness of a stopped runtime", resume: true, step: backupReadinessStep, state: protocolstate.ServerStarting,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupVerifyRuntimeStep, "state", "stopped")
			},
			want: "readiness checks require the exact running runtime and managed instance"},
		{name: "readiness of another unit", resume: true, step: backupReadinessStep, state: protocolstate.ServerStarting,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupVerifyRuntimeStep, "unit_ref", json.RawMessage(backupOtherUnit))
			}, want: "readiness checks require the exact running runtime and managed instance"},
		{name: "readiness while backing up", resume: true, step: backupReadinessStep, state: protocolstate.ServerBackingUp,
			want: "readiness checks require the exact running runtime and managed instance"},

		{name: "final stopped inspection", step: backupVerifyStoppedStep, state: protocolstate.ServerBackingUp},
		{name: "final stopped inspection while starting", step: backupVerifyStoppedStep, state: protocolstate.ServerStarting,
			want: "final stopped-state inspection requires the validated backup and exact execution unit"},
		{name: "final stopped inspection after a failed check", step: backupVerifyStoppedStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				setBackupOutputField(t, o, backupValidateStep, "check_results", failedCheck)
			},
			want: "final stopped-state inspection requires the validated backup"},
		{name: "final stopped inspection of another unit", step: backupVerifyStoppedStep, state: protocolstate.ServerBackingUp,
			change: func(t *testing.T, o *store.OperationRecord) {
				index := backupStepIndex(*o, backupVerifyStoppedStep)
				o.Steps[index].Input = backupJSON(t, map[string]any{"unit_ref": json.RawMessage(backupOtherUnit)})
			}, want: "final stopped-state inspection requires the validated backup and exact execution unit"},

		{name: "step without an identifier", resume: true, step: backupValidateStep, state: protocolstate.ServerBackingUp,
			change: func(_ *testing.T, o *store.OperationRecord) {
				o.Steps[backupStepIndex(*o, backupValidateStep)].StepID = ""
			},
			want: "backup workflow step is missing an identifier"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBackupPreconditionFixture(t, test.resume)
			operation := f.operation
			operation.Steps = append([]store.OperationStep(nil), f.operation.Steps...)
			completeBackupThrough(t, &operation, test.step)
			index := backupStepIndex(operation, test.step)
			if test.change != nil {
				test.change(t, &operation)
			}
			server := f.server
			server.LifecycleState = test.state
			workflow := *f.workflow
			workflow.servers = testBackupServerRepository{record: store.StoredLogicalServer{Record: server}}
			workflow.transfers = scriptedBackupStreams{err: test.streams}
			err := workflow.CheckPreconditions(context.Background(), operation, operation.Steps[index])
			if test.want == "" {
				if err != nil {
					t.Fatalf("CheckPreconditions(%s) = %v, want nil", test.step, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CheckPreconditions(%s) = %v, want an error containing %q", test.step, err, test.want)
			}
		})
	}
}
