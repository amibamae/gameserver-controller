package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// Shared helpers for the round-3 server.backup characterization tests. They
// build a fully recorded backup on top of the precondition fixture so the
// completion, input-resolution, stream, reconciliation and lifecycle rules
// can be pinned against single-field mutations.

// backupR3Streams is a transfer broker that fails verification for the step
// IDs in fail, records what it was asked to issue, and returns issue's
// transfers (or issueErr).
type backupR3Streams struct {
	fail     map[string]error
	issue    func(store.OperationStep, []ServerBackupStreamExpectation, string) ([]adapter.StreamTransfer, error)
	verified *[]string
}

func (s backupR3Streams) IssueStepStreamTransfers(_ context.Context, _ store.OperationRecord, step store.OperationStep, expectations []ServerBackupStreamExpectation, direction string) ([]adapter.StreamTransfer, error) {
	if s.issue == nil {
		return nil, nil
	}
	return s.issue(step, expectations, direction)
}

func (s backupR3Streams) VerifyStepStreams(_ context.Context, _ store.OperationRecord, step store.OperationStep, _ []ServerBackupStreamExpectation) error {
	if s.verified != nil {
		*s.verified = append(*s.verified, step.StepID)
	}
	return s.fail[step.StepID]
}

// backupR3Schemas wraps a schema runtime and rejects the listed schema IDs.
type backupR3Schemas struct {
	schemaRuntime
	reject map[string]bool
}

func (s backupR3Schemas) ValidateJSON(id string, raw []byte) error {
	if s.reject[id] {
		return errors.New("rejected by test schema")
	}
	return s.schemaRuntime.ValidateJSON(id, raw)
}

// backupR3Invoker answers adapter invocations with respond and records them.
type backupR3Invoker struct {
	backupDescriptorRuntime
	respond  func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
	requests *[]adapter.InvokeRequest
}

func (r backupR3Invoker) Invoke(_ context.Context, _ string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if r.requests != nil {
		*r.requests = append(*r.requests, request)
	}
	return r.respond(request)
}

// backupR3Cleaner is a transfer broker that also records cleanups.
type backupR3Cleaner struct {
	noopServerBackupTransferBroker
	cleaned *[]string
}

func (c backupR3Cleaner) CleanupOperation(operationID string) error {
	*c.cleaned = append(*c.cleaned, operationID)
	return errors.New("cleanup failure is ignored")
}

type backupR3Fixture struct {
	backupPreconditionFixture
	complete store.OperationRecord
}

// backupR3Workflow returns a copy of the fixture workflow whose repository
// holds server and whose streams are streams.
func (f backupR3Fixture) backupR3Workflow(server store.LogicalServer, streams ServerBackupTransferBroker) *ServerBackupWorkflow {
	workflow := *f.workflow
	workflow.servers = testBackupServerRepository{record: store.StoredLogicalServer{Record: server}}
	workflow.transfers = streams
	return &workflow
}

// backupR3Copy returns a deep-enough copy of operation for mutation.
func backupR3Copy(operation store.OperationRecord) store.OperationRecord {
	operation.Steps = append([]store.OperationStep(nil), operation.Steps...)
	return operation
}

// newBackupR3Fixture plans a backup of a ready server and records valid
// outputs for every step. The validation step's input is resolved through
// ResolveStepInput, as the engine does.
func newBackupR3Fixture(t *testing.T, resume bool) backupR3Fixture {
	t.Helper()
	f := newBackupPreconditionFixture(t, resume)
	operation := backupR3Copy(f.operation)
	last := backupVerifyStoppedStep
	if resume {
		last = backupReadinessStep
	}
	completeBackupThrough(t, &operation, last)
	workflow := backupR3Fixture{backupPreconditionFixture: f}.backupR3Workflow(f.server, backupR3Streams{})
	index := backupStepIndex(operation, backupValidateStep)
	resolved, err := workflow.ResolveStepInput(context.Background(), operation, operation.Steps[index])
	if err != nil {
		t.Fatalf("resolve validation input: %v", err)
	}
	operation.Steps[index].ResolvedInput = resolved
	if resume {
		succeedBackupStep(&operation, backupReadinessStep, backupJSON(t, map[string]any{
			"server_id": "server-1", "environment_id": "staging", "instance_ref": json.RawMessage(backupTestInstance),
			"game_release_id": "paper-release", "observed_at": backupNow(),
			"check_results": []any{map[string]any{"check_id": "paper.started", "check_version": "1", "result": "pass", "observed_at": backupNow()}},
		}))
	} else {
		succeedBackupStep(&operation, backupVerifyStoppedStep, backupJSON(t, map[string]any{
			"unit_ref": json.RawMessage(backupTestUnit), "state": "stopped", "observed_at": backupNow(),
		}))
	}
	return backupR3Fixture{backupPreconditionFixture: f, complete: operation}
}

// backupR3SetInputField sets (or with nil deletes) one field of the step's
// effective input: its resolved input when it has one, else its plan input.
func backupR3SetInputField(t *testing.T, operation *store.OperationRecord, stepID, field string, value any) {
	t.Helper()
	index := backupStepIndex(*operation, stepID)
	step := &operation.Steps[index]
	target := &step.Input
	if len(step.ResolvedInput) > 0 {
		target = &step.ResolvedInput
	}
	input := decodeObject(*target)
	if value == nil {
		delete(input, field)
	} else {
		input[field] = value
	}
	*target = backupJSON(t, input)
}

// backupR3SetStep changes one recorded step.
func backupR3SetStep(operation *store.OperationRecord, stepID string, change func(*store.OperationStep)) {
	change(&operation.Steps[backupStepIndex(*operation, stepID)])
}

// backupR3RemoveStep drops a step from the record.
func backupR3RemoveStep(operation *store.OperationRecord, stepID string) {
	index := backupStepIndex(*operation, stepID)
	operation.Steps = append(operation.Steps[:index:index], operation.Steps[index+1:]...)
}

// backupR3Pending marks a step as not yet run.
func backupR3Pending(operation *store.OperationRecord, stepID string) {
	backupR3SetStep(operation, stepID, func(step *store.OperationStep) { step.Status = protocolstate.StepPending })
}

// backupR3ErrText is the error text, or "<nil>".
func backupR3ErrText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// backupR3Mutation is a named change to a recorded backup.
type backupR3Mutation struct {
	name   string
	change func(*testing.T, *store.OperationRecord)
}

// backupR3Field mutates one output field of stepID.
func backupR3Field(stepID, field string, value any) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, operation *store.OperationRecord) {
		setBackupOutputField(t, operation, stepID, field, value)
	}
}

// backupR3InputField mutates one effective input field of stepID.
func backupR3InputField(stepID, field string, value any) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, operation *store.OperationRecord) {
		backupR3SetInputField(t, operation, stepID, field, value)
	}
}

const (
	backupR3OtherDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	backupR3OtherTarget = `{"server_id":"server-1","environment_id":"staging","instance_ref":` + backupOtherVM + `}`
)

var (
	backupR3Capture  = backupDataStepID("capture", "world")
	backupR3Upload   = backupDataStepID("upload", "world")
	backupR3Readback = backupDataStepID("readback", "world")
)
