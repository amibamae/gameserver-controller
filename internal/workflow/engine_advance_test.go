package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin, for each way one Advance can go, the ordered list of
// durable writes the engine makes, what it returns, and how many adapter
// calls it makes, so advanceLocked, reconcile and preflight can be
// reorganized safely.

var errAdvanceRefactorWrite = errors.New("injected store write failure")

// advanceRefactorStore is an in-memory operation store that records every
// write attempt in order. It does no transition validation, so a test can
// seed any persisted state, and it can fail or observe a chosen attempt.
type advanceRefactorStore struct {
	mu       sync.Mutex
	current  store.StoredOperation
	server   store.StoredLogicalServer
	writes   []string
	attempts int
	failAt   int
	onWrite  func(attempt int)
}

func (s *advanceRefactorStore) AcceptOperation(store.OperationRecord, store.IdempotencyProof, []string) (store.StoredOperation, bool, error) {
	return store.StoredOperation{}, false, errors.New("not supported")
}

func (s *advanceRefactorStore) GetOperation(operationID string) (store.StoredOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if operationID != s.current.Record.OperationID {
		return store.StoredOperation{}, store.ErrNotFound
	}
	return advanceRefactorClone(s.current), nil
}

func (s *advanceRefactorStore) UpdateOperation(expectedRevision uint64, next store.OperationRecord) (store.StoredOperation, error) {
	return s.write(expectedRevision, next, "")
}

func (s *advanceRefactorStore) write(expectedRevision uint64, next store.OperationRecord, suffix string) (store.StoredOperation, error) {
	s.mu.Lock()
	s.attempts++
	attempt := s.attempts
	hook := s.onWrite
	s.mu.Unlock()
	if hook != nil {
		hook(attempt)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	summary := advanceRefactorSummary(next) + suffix
	if attempt == s.failAt {
		s.writes = append(s.writes, "FAILED "+summary)
		return store.StoredOperation{}, errAdvanceRefactorWrite
	}
	if expectedRevision != s.current.Revision {
		s.writes = append(s.writes, "CONFLICT "+summary)
		return store.StoredOperation{}, store.ErrRevisionConflict
	}
	s.current = advanceRefactorClone(store.StoredOperation{Record: next, Revision: expectedRevision + 1})
	s.writes = append(s.writes, summary)
	return advanceRefactorClone(s.current), nil
}

func (s *advanceRefactorStore) ListRecoverableOperations() ([]store.StoredOperation, error) {
	return nil, nil
}
func (s *advanceRefactorStore) GetResourceLock(string) (store.ResourceLock, error) {
	return store.ResourceLock{}, store.ErrNotFound
}
func (s *advanceRefactorStore) ListResourceLocks() ([]store.ResourceLock, error) { return nil, nil }
func (s *advanceRefactorStore) Close() error                                     { return nil }

// advanceRefactorLifecycleStore adds atomic logical-server commits, which
// server.* operations require.
type advanceRefactorLifecycleStore struct{ *advanceRefactorStore }

func (s advanceRefactorLifecycleStore) GetLogicalServer(string, string) (store.StoredLogicalServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server, nil
}

func (s advanceRefactorLifecycleStore) ListLogicalServers() ([]store.StoredLogicalServer, error) {
	server, err := s.GetLogicalServer("", "")
	return []store.StoredLogicalServer{server}, err
}

func (s advanceRefactorLifecycleStore) UpdateOperationAndLogicalServer(expectedOperationRevision uint64, operation store.OperationRecord, expectedServerRevision uint64, server store.LogicalServer) (store.StoredOperation, store.StoredLogicalServer, error) {
	updated, err := s.write(expectedOperationRevision, operation, " +server="+string(server.LifecycleState))
	if err != nil {
		return store.StoredOperation{}, store.StoredLogicalServer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.server = store.StoredLogicalServer{Record: server, Revision: expectedServerRevision + 1}
	return updated, s.server, nil
}

// advanceRefactorBackupStore also captures backup lifecycle context.
type advanceRefactorBackupStore struct {
	advanceRefactorLifecycleStore
	captureErr error
}

func (s advanceRefactorBackupStore) CaptureBackupInitialState(operationID string, expectedRevision uint64) (store.StoredOperation, error) {
	if s.captureErr != nil {
		return store.StoredOperation{}, s.captureErr
	}
	current, err := s.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	next := current.Record
	s.mu.Lock()
	next.BackupInitialLifecycleState = s.server.Record.LifecycleState
	s.mu.Unlock()
	return s.write(expectedRevision, next, " (capture)")
}

func advanceRefactorClone(value store.StoredOperation) store.StoredOperation {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var clone store.StoredOperation
	if err := json.Unmarshal(encoded, &clone); err != nil {
		panic(err)
	}
	return clone
}

// advanceRefactorSummary renders every field the engine writes.
func advanceRefactorSummary(record store.OperationRecord) string {
	var b strings.Builder
	current := "-"
	if record.CurrentStepID != nil {
		current = *record.CurrentStepID
	}
	fmt.Fprintf(&b, "%s cur=%s err=%s", record.Status, current, advanceRefactorError(record.Error))
	if len(record.Result) > 0 {
		b.WriteString(" result")
	}
	if record.BackupInitialLifecycleState != "" {
		fmt.Fprintf(&b, " backup=%s", record.BackupInitialLifecycleState)
	}
	for _, step := range record.Steps {
		fmt.Fprintf(&b, " | %s:%s#%d", step.StepID, step.Status, step.Attempt)
		if len(step.Error) > 0 {
			fmt.Fprintf(&b, " err=%s", advanceRefactorError(step.Error))
		}
		if len(step.ResolvedInput) > 0 {
			fmt.Fprintf(&b, " in=%s", step.ResolvedInput)
		}
		if len(step.Output) > 0 {
			fmt.Fprintf(&b, " out=%s", step.Output)
		}
		if len(step.ExternalOperationRef) > 0 {
			fmt.Fprintf(&b, " ext=%s", step.ExternalOperationRef)
		}
	}
	return b.String()
}

func advanceRefactorError(value json.RawMessage) string {
	if len(value) == 0 {
		return "-"
	}
	var decoded struct {
		Code     string `json:"code"`
		Category string `json:"category"`
		Message  string `json:"message"`
		Recovery struct {
			Action           string `json:"action"`
			OperatorRequired bool   `json:"operator_required"`
		} `json:"recovery"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "undecodable"
	}
	return fmt.Sprintf("%s/%s/%s/%t(%s)", decoded.Code, decoded.Category, decoded.Recovery.Action, decoded.Recovery.OperatorRequired, decoded.Message)
}

// advanceRefactorEnv is one engine wired to a recording store.
type advanceRefactorEnv struct {
	fixture engineFixture
	store   *advanceRefactorStore
	engine  *Engine
	ctx     context.Context
	cancel  context.CancelFunc
}

type advanceRefactorCase struct {
	name              string
	capabilityVersion string
	replies           []fakeadapter.Reply
	extraSteps        []store.OperationStep
	backup            string // "", "lifecycle" (no capture), "capture"
	captureErr        error
	seed              func(*store.OperationRecord)
	setup             func(*advanceRefactorEnv)
	operationID       string

	wantStatus string
	wantErr    string
	wantWrites []string
	wantCalls  int
}

func advanceRefactorRun(t *testing.T, tc advanceRefactorCase) {
	t.Helper()
	version := tc.capabilityVersion
	if version == "" {
		version = "1"
	}
	fixture := newEngineFixtureForOperation(t, "advance-refactor", version, tc.replies, true, testOperationType, false, tc.extraSteps...)
	seeded := advanceRefactorClone(fixture.operation)
	seeded.Record.CreatedAt, seeded.Record.UpdatedAt, seeded.Record.StartedAt = time.Time{}, time.Time{}, nil
	if tc.seed != nil {
		tc.seed(&seeded.Record)
	}
	recording := &advanceRefactorStore{current: seeded}
	recording.server = store.StoredLogicalServer{Revision: 1, Record: store.LogicalServer{
		ServerID: "server-1", EnvironmentID: "staging", LifecycleState: protocolstate.ServerReady,
	}}
	var operationStore store.OperationStore = recording
	switch tc.backup {
	case "lifecycle":
		operationStore = advanceRefactorLifecycleStore{recording}
	case "capture":
		operationStore = advanceRefactorBackupStore{advanceRefactorLifecycleStore{recording}, tc.captureErr}
	}
	definitions := NewDefinitions()
	if err := definitions.Register(testOperationType, fixture.definition); err != nil {
		t.Fatal(err)
	}
	backupDefinition := &projectingTestDefinition{testDefinition: &testDefinition{resolution: Resolution{State: ResolutionNeedsOperator}, operationType: "server.backup"}}
	if err := definitions.Register("server.backup", backupDefinition); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(operationStore, fixture.engine.adapters, fixture.engine.schemas, definitions, fixture.authorizer, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := &advanceRefactorEnv{fixture: fixture, store: recording, engine: engine, ctx: ctx, cancel: cancel}
	if tc.setup != nil {
		tc.setup(env)
	}
	operationID := tc.operationID
	if operationID == "" {
		operationID = seeded.Record.OperationID
	}
	stored, advanceErr := engine.Advance(env.ctx, operationID)
	gotErr := ""
	if advanceErr != nil {
		gotErr = advanceErr.Error()
	}
	if gotErr != tc.wantErr {
		t.Errorf("error = %q, want %q", gotErr, tc.wantErr)
	}
	if got := string(stored.Record.Status); got != tc.wantStatus {
		t.Errorf("returned status = %q, want %q", got, tc.wantStatus)
	}
	if calls := len(fixture.adapter.Calls()); calls != tc.wantCalls {
		t.Errorf("adapter calls = %d, want %d", calls, tc.wantCalls)
	}
	recording.mu.Lock()
	writes := append([]string(nil), recording.writes...)
	recording.mu.Unlock()
	if strings.Join(writes, "\n") != strings.Join(tc.wantWrites, "\n") {
		t.Errorf("durable writes:\n  %s\nwant:\n  %s", strings.Join(writes, "\n  "), strings.Join(tc.wantWrites, "\n  "))
	}
}

func advanceRefactorSeed(operation protocolstate.OperationStatus, current string, steps ...protocolstate.StepStatus) func(*store.OperationRecord) {
	return func(record *store.OperationRecord) {
		record.Status = operation
		if current != "" {
			record.CurrentStepID = stringPointer(current)
		}
		for i, status := range steps {
			record.Steps[i].Status = status
			if status != protocolstate.StepPending {
				record.Steps[i].Attempt = 1
			}
			if status == protocolstate.StepSucceeded {
				record.Steps[i].Output = json.RawMessage(testExternalRef)
			}
		}
	}
}

// advanceRefactorBoundStep is a second step whose input comes from step-1.
func advanceRefactorBoundStep(capabilityVersion string) store.OperationStep {
	return store.OperationStep{
		StepID: "step-2", StepType: "hosting.instance.wait", Status: protocolstate.StepPending,
		AdapterID: testAdapterID, CapabilityName: testCapability, CapabilityVersion: capabilityVersion,
		Input:         json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance"}`),
		InputBindings: []inputbinding.Binding{{TargetPointer: "/external_id", SourceStepID: "step-1", SourcePointer: "/external_id"}},
	}
}

// advanceRefactorPlainStep is a second unbound step.
func advanceRefactorPlainStep() store.OperationStep {
	return store.OperationStep{
		StepID: "step-2", StepType: "hosting.instance.wait", Status: protocolstate.StepPending,
		AdapterID: testAdapterID, CapabilityName: testCapability, CapabilityVersion: "1",
		Input: json.RawMessage(testExternalRef),
	}
}

// advanceRefactorCancelDefinition cancels the run's context while checking
// preconditions, as if the caller gave up during the check.
type advanceRefactorCancelDefinition struct {
	*testDefinition
	cancel context.CancelFunc
}

func (d advanceRefactorCancelDefinition) CheckPreconditions(ctx context.Context, record store.OperationRecord, step store.OperationStep) error {
	d.cancel()
	return d.testDefinition.CheckPreconditions(ctx, record, step)
}

func advanceRefactorReplace(env *advanceRefactorEnv, definition Definition) {
	definitions := NewDefinitions()
	if err := definitions.Register(testOperationType, definition); err != nil {
		panic(err)
	}
	env.engine.definitions = definitions
}

const (
	advanceRefactorRunning   = "running cur=- err=- | step-1:pending#0"
	advanceRefactorClaimed   = "running cur=step-1 err=- | step-1:running#1"
	advanceRefactorSucceeded = "running cur=- err=- | step-1:succeeded#1 out=" + testExternalRef
	advanceRefactorDone      = "succeeded cur=- err=- result | step-1:succeeded#1 out=" + testExternalRef
	advanceRefactorNotFound  = "operation not found"
)

func advanceRefactorSuspended(code, message, steps string) string {
	return "needs_operator cur=- err=" + code + "/safety_blocked/operator_review/true(" + message + ")" + steps
}

func TestAdvanceRefactorGuardsBeforeAnyWrite(t *testing.T) {
	for _, tc := range []advanceRefactorCase{
		{name: "missing operation", operationID: "absent", wantErr: advanceRefactorNotFound},
		{name: "terminal operation", seed: advanceRefactorSeed(protocolstate.OperationSucceeded, "", protocolstate.StepSucceeded), wantStatus: "succeeded"},
		{name: "needs operator", seed: advanceRefactorSeed(protocolstate.OperationNeedsOperator, ""), wantStatus: "needs_operator", wantErr: ErrNeedsOperator.Error()},
		{name: "unregistered type", seed: func(r *store.OperationRecord) { r.OperationType = "test.unregistered" }, wantStatus: "pending", wantErr: ErrNotRecoverable.Error() + ": test.unregistered"},
		{name: "cancelled context", setup: func(env *advanceRefactorEnv) { env.cancel() }, wantStatus: "pending", wantErr: context.Canceled.Error()},
		{name: "cancellation requested", seed: advanceRefactorSeed(protocolstate.OperationCancellationRequested, ""), wantStatus: "cancellation_requested", wantErr: ErrNotRecoverable.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorBackupInitialState(t *testing.T) {
	backup := func(status protocolstate.OperationStatus) func(*store.OperationRecord) {
		return func(r *store.OperationRecord) {
			r.OperationType = "server.backup"
			r.RequestSnapshot.OperationType = "server.backup"
			r.Status = status
		}
	}
	const backupSteps = " | step-1:pending#0"
	for _, tc := range []advanceRefactorCase{
		{name: "no capture store while pending", backup: "lifecycle", seed: backup(protocolstate.OperationPending), wantStatus: "pending",
			wantErr: "operation store cannot persist backup lifecycle context; no workflow step was dispatched"},
		{name: "no capture store while running", backup: "lifecycle", seed: backup(protocolstate.OperationRunning), wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.backup.initial_state_unavailable", "The controller cannot durably capture the server state before backup dispatch.", backupSteps)}},
		{name: "capture fails while pending", backup: "capture", captureErr: errors.New("boom"), seed: backup(protocolstate.OperationPending), wantStatus: "pending",
			wantErr: "backup initial lifecycle state could not be persisted; no workflow step was dispatched"},
		{name: "capture fails while running", backup: "capture", captureErr: errors.New("boom"), seed: backup(protocolstate.OperationRunning), wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.backup.initial_state_unavailable", "The server's initial lifecycle state could not be durably captured before backup dispatch.", backupSteps)}},
		{name: "capture then dispatch", backup: "capture", seed: backup(protocolstate.OperationPending), wantStatus: "running", wantCalls: 1,
			wantWrites: []string{
				"pending cur=- err=- backup=ready | step-1:pending#0 (capture)",
				"running cur=- err=- backup=ready | step-1:pending#0 +server=stopping",
				"running cur=step-1 err=- backup=ready | step-1:running#1",
				"running cur=- err=- backup=ready | step-1:succeeded#1 out=" + testExternalRef,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorStartAndCompatibility(t *testing.T) {
	for _, tc := range []advanceRefactorCase{
		{name: "happy path", wantStatus: "running", wantCalls: 1, wantWrites: []string{advanceRefactorRunning, advanceRefactorClaimed, advanceRefactorSucceeded}},
		{name: "start write fails", setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(),
			wantWrites: []string{"FAILED " + advanceRefactorRunning}},
		{name: "protocol version not loaded", seed: func(r *store.OperationRecord) { r.ProtocolVersion = "0.0.0-other" }, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorRunning, advanceRefactorSuspended("core.compatibility.protocol_version_unavailable", "The operation's recorded protocol version is not loaded by this controller.", " | step-1:pending#0")}},
		{name: "invalid step order", extraSteps: []store.OperationStep{advanceRefactorPlainStep()},
			seed: advanceRefactorSeed(protocolstate.OperationPending, "", protocolstate.StepPending, protocolstate.StepSucceeded), wantStatus: "needs_operator",
			wantWrites: []string{
				advanceRefactorRunning + " | step-2:succeeded#1 out=" + testExternalRef,
				advanceRefactorSuspended("core.workflow.invalid_step_order", "The persisted workflow steps are not in a safe sequential order.", " | step-1:pending#0 | step-2:succeeded#1 out="+testExternalRef),
			}},
		{name: "context cancelled during start write", setup: func(env *advanceRefactorEnv) { env.store.onWrite = func(int) { env.cancel() } },
			wantStatus: "running", wantErr: context.Canceled.Error(), wantWrites: []string{advanceRefactorRunning}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorResumesWaitingOrReconcilingOperation(t *testing.T) {
	failedStep := "step-1:failed#1"
	for _, tc := range []advanceRefactorCase{
		{name: "waiting with pending step", seed: func(r *store.OperationRecord) {
			r.Status = protocolstate.OperationWaiting
			r.Error = protocolError("core.test.stale", "internal", "stale", r.OperationID, false, "reconcile", false)
		}, wantStatus: "running", wantCalls: 1, wantWrites: []string{advanceRefactorRunning, advanceRefactorClaimed, advanceRefactorSucceeded}},
		{name: "waiting with pending step, write fails", seed: advanceRefactorSeed(protocolstate.OperationWaiting, ""),
			setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(), wantWrites: []string{"FAILED " + advanceRefactorRunning}},
		{name: "reconciling with every step complete", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "step-1", protocolstate.StepSucceeded),
			wantStatus: "succeeded", wantWrites: []string{advanceRefactorSucceeded, advanceRefactorDone}},
		{name: "reconciling with every step complete keeps the error", seed: func(r *store.OperationRecord) {
			advanceRefactorSeed(protocolstate.OperationReconciling, "", protocolstate.StepSucceeded)(r)
			r.Error = protocolError("core.test.stale", "internal", "stale", r.OperationID, false, "reconcile", false)
		}, wantStatus: "succeeded", wantWrites: []string{
			"running cur=- err=core.test.stale/internal/reconcile/false(stale) | step-1:succeeded#1 out=" + testExternalRef, advanceRefactorDone}},
		{name: "reconciling with every step complete, write fails", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "", protocolstate.StepSucceeded),
			setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(), wantWrites: []string{"FAILED " + advanceRefactorSucceeded}},
		{name: "reconciling with a failed step", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "", protocolstate.StepFailed), wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.workflow.incomplete_steps", "The workflow cannot succeed while any step remains unresolved.", " | "+failedStep)}},
		{name: "waiting step reconciles", seed: advanceRefactorSeed(protocolstate.OperationWaiting, "step-1", protocolstate.StepWaiting), wantStatus: "needs_operator",
			wantWrites: []string{"needs_operator cur=step-1 err=core.reconciliation.operator_required/safety_blocked/operator_review/true(Automatic reconciliation cannot safely determine the external outcome.) | step-1:waiting#1"}},
		{name: "reconciling running step is marked unknown first", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "step-1", protocolstate.StepRunning), wantStatus: "needs_operator",
			wantWrites: []string{
				"reconciling cur=step-1 err=core.adapter.outcome_unknown/internal/reconcile/false(The previous adapter invocation has no recorded response.) | step-1:outcome_unknown#1 err=core.adapter.outcome_unknown/internal/reconcile/false(The previous adapter invocation has no recorded response.)",
				"needs_operator cur=step-1 err=core.reconciliation.operator_required/safety_blocked/operator_review/true(Automatic reconciliation cannot safely determine the external outcome.) | step-1:outcome_unknown#1 err=core.adapter.outcome_unknown/internal/reconcile/false(The previous adapter invocation has no recorded response.)",
			}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

const advanceRefactorUnknown = "core.adapter.outcome_unknown/internal/reconcile/false(The previous adapter invocation has no recorded response.)"

func TestAdvanceRefactorRunningOperationWithActiveStep(t *testing.T) {
	unknownWrite := "reconciling cur=step-1 err=" + advanceRefactorUnknown + " | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown
	for _, tc := range []advanceRefactorCase{
		{name: "running step becomes unknown and reconciles", seed: advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepRunning), wantStatus: "needs_operator",
			wantWrites: []string{unknownWrite, "needs_operator cur=step-1 err=core.reconciliation.operator_required/safety_blocked/operator_review/true(Automatic reconciliation cannot safely determine the external outcome.) | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown}},
		{name: "running step, unknown write fails", seed: advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepRunning),
			setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(), wantWrites: []string{"FAILED " + unknownWrite}},
		{name: "running step without current pointer", seed: advanceRefactorSeed(protocolstate.OperationRunning, "", protocolstate.StepRunning), wantStatus: "needs_operator",
			wantWrites: []string{"reconciling cur=- err=" + advanceRefactorUnknown + " | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown,
				"needs_operator cur=- err=core.reconciliation.operator_required/safety_blocked/operator_review/true(Automatic reconciliation cannot safely determine the external outcome.) | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown}},
		{name: "unknown step reconciles", seed: advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown), wantStatus: "needs_operator",
			wantWrites: []string{"needs_operator cur=step-1 err=core.reconciliation.operator_required/safety_blocked/operator_review/true(Automatic reconciliation cannot safely determine the external outcome.) | step-1:outcome_unknown#1"}},
		{name: "succeeded current step is not active", seed: advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepSucceeded), wantStatus: "succeeded",
			wantWrites: []string{"succeeded cur=- err=- result | step-1:succeeded#1 out=" + testExternalRef}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorSelectsNextStep(t *testing.T) {
	for _, tc := range []advanceRefactorCase{
		{name: "failed step without dispatch target", seed: advanceRefactorSeed(protocolstate.OperationRunning, "", protocolstate.StepFailed), wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.workflow.incomplete_steps", "The workflow contains unresolved steps without a current dispatch target.", " | step-1:failed#1")}},
		{name: "all steps complete", seed: advanceRefactorSeed(protocolstate.OperationRunning, "", protocolstate.StepSucceeded), wantStatus: "succeeded",
			wantWrites: []string{advanceRefactorDone}},
		{name: "second step dispatches after the first", extraSteps: []store.OperationStep{advanceRefactorPlainStep()},
			seed: advanceRefactorSeed(protocolstate.OperationRunning, "", protocolstate.StepSucceeded), wantStatus: "running", wantCalls: 1,
			wantWrites: []string{
				"running cur=step-2 err=- | step-1:succeeded#1 out=" + testExternalRef + " | step-2:running#1",
				"running cur=- err=- | step-1:succeeded#1 out=" + testExternalRef + " | step-2:succeeded#1 out=" + testExternalRef,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorResolvesBoundInput(t *testing.T) {
	first := "step-1:succeeded#1 out=" + testExternalRef
	resolved := `{"adapter_id":"hosting.fake","environment_id":"staging","external_id":"instance-1","resource_kind":"instance"}`
	running := advanceRefactorSeed(protocolstate.OperationRunning, "", protocolstate.StepSucceeded)
	for _, tc := range []advanceRefactorCase{
		{name: "resolved input is persisted before the claim", extraSteps: []store.OperationStep{advanceRefactorBoundStep("1")}, seed: running, wantStatus: "running", wantCalls: 1,
			wantWrites: []string{
				"running cur=- err=- | " + first + " | step-2:pending#0 in=" + resolved,
				"running cur=step-2 err=- | " + first + " | step-2:running#1 in=" + resolved,
				"running cur=- err=- | " + first + " | step-2:succeeded#1 in=" + resolved + " out=" + testExternalRef,
			}},
		{name: "resolved input write fails", extraSteps: []store.OperationStep{advanceRefactorBoundStep("1")}, seed: running,
			setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(),
			wantWrites: []string{"FAILED running cur=- err=- | " + first + " | step-2:pending#0 in=" + resolved}},
		{name: "binding source is missing", extraSteps: []store.OperationStep{advanceRefactorBoundStep("1")}, seed: func(r *store.OperationRecord) {
			running(r)
			r.Steps[0].Output = json.RawMessage(`{"adapter_id":"hosting.fake"}`)
		}, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.workflow.input_binding_unavailable", "A workflow input could not be safely resolved from an earlier completed step.",
				` | step-1:succeeded#1 out={"adapter_id":"hosting.fake"} | step-2:pending#0`)}},
		{name: "resolved input fails preflight", extraSteps: []store.OperationStep{advanceRefactorBoundStep("2")}, seed: running, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorSuspended("core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.",
				" | "+first+" | step-2:pending#0")}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorChecksBeforeTheClaim(t *testing.T) {
	capabilityUnavailable := advanceRefactorSuspended("core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.", " | step-1:pending#0")
	for _, tc := range []advanceRefactorCase{
		{name: "capability version unavailable", capabilityVersion: "2", wantStatus: "needs_operator", wantWrites: []string{advanceRefactorRunning, capabilityUnavailable}},
		{name: "precondition unmet", setup: func(env *advanceRefactorEnv) { env.fixture.definition.preconditionErr = errors.New("no") }, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorRunning, advanceRefactorSuspended("core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.", " | step-1:pending#0")}},
		{name: "context cancelled during preconditions", setup: func(env *advanceRefactorEnv) {
			advanceRefactorReplace(env, advanceRefactorCancelDefinition{env.fixture.definition, env.cancel})
		}, wantStatus: "running", wantErr: context.Canceled.Error(), wantWrites: []string{advanceRefactorRunning}},
		{name: "operation deadline passed", seed: func(r *store.OperationRecord) {
			past := time.Now().Add(-time.Hour).UTC()
			r.Deadline = &past
		}, wantStatus: "failed", wantWrites: []string{advanceRefactorRunning,
			"failed cur=- err=core.operation.deadline_exceeded/timeout/inspect_operation/false(The operation deadline expired before adapter dispatch.) result | step-1:pending#0"}},
		{name: "claim write fails", setup: func(env *advanceRefactorEnv) { env.store.failAt = 2 }, wantErr: errAdvanceRefactorWrite.Error(),
			wantWrites: []string{advanceRefactorRunning, "FAILED " + advanceRefactorClaimed}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

// advanceRefactorHandlerDefinition turns a failed precondition into the
// configured resolution.
type advanceRefactorHandlerDefinition struct {
	*testDefinition
	handled Resolution
}

func (d advanceRefactorHandlerDefinition) HandlePreconditionFailure(context.Context, store.OperationRecord, store.OperationStep, error) (Resolution, error) {
	return d.handled, nil
}

// advanceRefactorAdapters rewrites the registered adapter descriptor.
type advanceRefactorAdapters struct {
	adapterRuntime
	rewrite func(adapter.Descriptor) adapter.Descriptor
}

func (a advanceRefactorAdapters) Descriptor(adapterID string) (adapter.Descriptor, error) {
	descriptor, err := a.adapterRuntime.Descriptor(adapterID)
	if err != nil {
		return descriptor, err
	}
	return a.rewrite(descriptor), nil
}

func advanceRefactorResolve(resolution Resolution) func(*advanceRefactorEnv) {
	return func(env *advanceRefactorEnv) { env.fixture.definition.resolution = resolution }
}

func advanceRefactorDefinitiveError(operationID string) json.RawMessage {
	return protocolError("hosting.test.rejected", "provider_failure", "Rejected by the provider.", operationID, false, "inspect_operation", false)
}

const (
	advanceRefactorOtherRef  = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"instance-2"}`
	advanceRefactorUnknownOn = " | step-1:outcome_unknown#1"
)

func advanceRefactorReconcileSuspended(code, message, steps string) string {
	return "needs_operator cur=step-1 err=" + code + "/safety_blocked/operator_review/true(" + message + ")" + steps
}

func TestAdvanceRefactorReconcile(t *testing.T) {
	unknown := advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown)
	reconcilingUnknown := advanceRefactorSeed(protocolstate.OperationReconciling, "step-1", protocolstate.StepOutcomeUnknown)
	withRef := func(r *store.OperationRecord) {
		unknown(r)
		r.Steps[0].ExternalOperationRef = json.RawMessage(testExternalRef)
	}
	invalidResult := func(message, steps string) []string {
		return []string{advanceRefactorReconcileSuspended("core.reconciliation.invalid_result", message, steps)}
	}
	for _, tc := range []advanceRefactorCase{
		{name: "capability unavailable", capabilityVersion: "2", seed: unknown, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorReconcileSuspended("core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.", advanceRefactorUnknownOn)}},
		{name: "context cancelled during unknown write", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "step-1", protocolstate.StepRunning),
			setup: func(env *advanceRefactorEnv) { env.store.onWrite = func(int) { env.cancel() } }, wantStatus: "reconciling", wantErr: context.Canceled.Error(),
			wantWrites: []string{"reconciling cur=step-1 err=" + advanceRefactorUnknown + " | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown}},
		{name: "unknown write fails", seed: advanceRefactorSeed(protocolstate.OperationReconciling, "step-1", protocolstate.StepRunning),
			setup: func(env *advanceRefactorEnv) { env.store.failAt = 1 }, wantErr: errAdvanceRefactorWrite.Error(),
			wantWrites: []string{"FAILED reconciling cur=step-1 err=" + advanceRefactorUnknown + " | step-1:outcome_unknown#1 err=" + advanceRefactorUnknown}},
		{name: "reconcile error", seed: unknown, setup: func(env *advanceRefactorEnv) {
			env.fixture.definition.reconcileFn = func(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error) {
				return Resolution{}, errors.New("unreachable provider")
			}
		}, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorReconcileSuspended("core.reconciliation.unavailable", "The external outcome could not be reconciled automatically.", advanceRefactorUnknownOn)}},
		{name: "invalid external reference", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionPending, ExternalOperationRef: json.RawMessage(`{"x":1}`)}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciliation returned an invalid or conflicting external operation reference.", advanceRefactorUnknownOn)},
		{name: "conflicting external reference", seed: withRef, setup: advanceRefactorResolve(Resolution{State: ResolutionPending, ExternalOperationRef: json.RawMessage(advanceRefactorOtherRef)}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciliation returned an invalid or conflicting external operation reference.", advanceRefactorUnknownOn+" ext="+testExternalRef)},
		{name: "new external reference then pending", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionPending, ExternalOperationRef: json.RawMessage(testExternalRef)}),
			wantStatus: "waiting", wantWrites: []string{
				"running cur=step-1 err=- | step-1:outcome_unknown#1 ext=" + testExternalRef,
				"waiting cur=step-1 err=- | step-1:waiting#1 ext=" + testExternalRef,
			}},
		{name: "new external reference write fails", seed: unknown, setup: func(env *advanceRefactorEnv) {
			env.fixture.definition.resolution = Resolution{State: ResolutionPending, ExternalOperationRef: json.RawMessage(testExternalRef)}
			env.store.failAt = 1
		}, wantErr: errAdvanceRefactorWrite.Error(), wantWrites: []string{"FAILED running cur=step-1 err=- | step-1:outcome_unknown#1 ext=" + testExternalRef}},
		{name: "same external reference is not rewritten", seed: withRef, setup: advanceRefactorResolve(Resolution{State: ResolutionPending, ExternalOperationRef: json.RawMessage(testExternalRef)}),
			wantStatus: "waiting", wantWrites: []string{"waiting cur=step-1 err=- | step-1:waiting#1 ext=" + testExternalRef}},
		{name: "completed without object output", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionCompleted, Output: json.RawMessage(`[]`)}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciliation did not provide a valid completed output.", advanceRefactorUnknownOn)},
		{name: "completed output fails schema", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionCompleted, Output: json.RawMessage(`{"unexpected":true}`)}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciled output failed the recorded capability schema.", advanceRefactorUnknownOn)},
		{name: "completed while running", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionCompleted, Output: json.RawMessage(testExternalRef)}),
			wantStatus: "running", wantWrites: []string{advanceRefactorSucceeded}},
		{name: "completed while reconciling", seed: func(r *store.OperationRecord) {
			reconcilingUnknown(r)
			r.Error = protocolError("core.adapter.outcome_unknown", "internal", "stale", r.OperationID, false, "reconcile", false)
			r.Steps[0].Error = r.Error
		}, setup: advanceRefactorResolve(Resolution{State: ResolutionCompleted, Output: json.RawMessage(testExternalRef)}),
			wantStatus: "running", wantWrites: []string{advanceRefactorSucceeded}},
		{name: "pending", seed: func(r *store.OperationRecord) {
			reconcilingUnknown(r)
			r.Steps[0].Error = protocolError("core.adapter.outcome_unknown", "internal", "stale", r.OperationID, false, "reconcile", false)
		}, setup: advanceRefactorResolve(Resolution{State: ResolutionPending}),
			wantStatus: "waiting", wantWrites: []string{"waiting cur=step-1 err=- | step-1:waiting#1"}},
		{name: "safe retry at the attempt limit", seed: func(r *store.OperationRecord) {
			unknown(r)
			r.Steps[0].Attempt = 3
		}, setup: advanceRefactorResolve(Resolution{State: ResolutionSafeToRetry}), wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorReconcileSuspended("core.operation.retry_limit", "The safe retry limit for this step has been reached.", " | step-1:outcome_unknown#3")}},
		{name: "safe retry with unmet precondition", seed: unknown, setup: func(env *advanceRefactorEnv) {
			env.fixture.definition.resolution = Resolution{State: ResolutionSafeToRetry}
			env.fixture.definition.preconditionErr = errors.New("no")
		}, wantStatus: "needs_operator",
			wantWrites: []string{advanceRefactorReconcileSuspended("core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.", advanceRefactorUnknownOn)}},
		{name: "safe retry redispatches", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionSafeToRetry}), wantStatus: "running", wantCalls: 1,
			wantWrites: []string{"running cur=step-1 err=- | step-1:running#2", "running cur=- err=- | step-1:succeeded#2 out=" + testExternalRef}},
		{name: "safe retry from reconciling", seed: reconcilingUnknown, setup: advanceRefactorResolve(Resolution{State: ResolutionSafeToRetry}), wantStatus: "running", wantCalls: 1,
			wantWrites: []string{"running cur=step-1 err=- | step-1:running#2", "running cur=- err=- | step-1:succeeded#2 out=" + testExternalRef}},
		{name: "safe retry claim fails", seed: unknown, setup: func(env *advanceRefactorEnv) {
			env.fixture.definition.resolution = Resolution{State: ResolutionSafeToRetry}
			env.store.failAt = 1
		}, wantErr: errAdvanceRefactorWrite.Error(), wantWrites: []string{"FAILED running cur=step-1 err=- | step-1:running#2"}},
		{name: "definitive error invalid", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionDefinitiveError, Error: json.RawMessage(`{"code":""}`)}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciliation returned an invalid error record.", advanceRefactorUnknownOn)},
		{name: "definitive error", seed: unknown, setup: func(env *advanceRefactorEnv) {
			env.fixture.definition.resolution = Resolution{State: ResolutionDefinitiveError, Error: advanceRefactorDefinitiveError("advance-refactor")}
		}, wantStatus: "failed", wantWrites: []string{
			"failed cur=- err=hosting.test.rejected/provider_failure/inspect_operation/false(Rejected by the provider.) result | step-1:failed#1 err=hosting.test.rejected/provider_failure/inspect_operation/false(Rejected by the provider.)"}},
		{name: "unsupported resolution state", seed: unknown, setup: advanceRefactorResolve(Resolution{State: ResolutionState("maybe")}),
			wantStatus: "needs_operator", wantWrites: invalidResult("Reconciliation returned an unsupported resolution state.", advanceRefactorUnknownOn)},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorPreconditionFailureHandler(t *testing.T) {
	rejected := "hosting.test.rejected/provider_failure/inspect_operation/false(Rejected by the provider.)"
	handler := func(env *advanceRefactorEnv) {
		env.fixture.definition.preconditionErr = errors.New("no")
		advanceRefactorReplace(env, advanceRefactorHandlerDefinition{env.fixture.definition, Resolution{State: ResolutionDefinitiveError, Error: advanceRefactorDefinitiveError("advance-refactor")}})
	}
	for _, tc := range []advanceRefactorCase{
		{name: "pending step fails definitively", setup: handler, wantStatus: "failed",
			wantWrites: []string{advanceRefactorRunning, "failed cur=- err=" + rejected + " result | step-1:pending#0"}},
		{name: "safe retry fails definitively", seed: advanceRefactorSeed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown), setup: func(env *advanceRefactorEnv) {
			handler(env)
			env.fixture.definition.resolution = Resolution{State: ResolutionSafeToRetry}
		}, wantStatus: "failed", wantWrites: []string{"failed cur=- err=" + rejected + " result | step-1:outcome_unknown#1"}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}

func TestAdvanceRefactorPreflight(t *testing.T) {
	unavailable := []string{advanceRefactorRunning, advanceRefactorSuspended("core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.", " | step-1:pending#0")}
	rewrite := func(change func(*adapter.Descriptor)) func(*advanceRefactorEnv) {
		return func(env *advanceRefactorEnv) {
			env.engine.adapters = advanceRefactorAdapters{env.engine.adapters, func(d adapter.Descriptor) adapter.Descriptor {
				d.Capabilities = append([]adapter.Capability(nil), d.Capabilities...)
				change(&d)
				return d
			}}
		}
	}
	for _, tc := range []advanceRefactorCase{
		{name: "incomplete plan", seed: func(r *store.OperationRecord) { r.Steps[0].CapabilityName = "" }, wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "non-object input", seed: func(r *store.OperationRecord) { r.Steps[0].Input = json.RawMessage(`[]`) }, wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "unknown adapter", seed: func(r *store.OperationRecord) { r.Steps[0].AdapterID = "hosting.missing" }, wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "descriptor names another adapter", setup: rewrite(func(d *adapter.Descriptor) { d.AdapterID = "hosting.other" }), wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "adapter lacks protocol version", setup: rewrite(func(d *adapter.Descriptor) { d.ProtocolVersions = nil }), wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "unknown capability", seed: func(r *store.OperationRecord) { r.Steps[0].CapabilityName = "hosting.fake.missing" }, wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "input schema missing", setup: rewrite(func(d *adapter.Descriptor) { d.Capabilities[0].InputSchemaURI = "urn:missing" }), wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "output schema missing", setup: rewrite(func(d *adapter.Descriptor) { d.Capabilities[0].OutputSchemaURI = "urn:missing" }), wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "input fails schema", seed: func(r *store.OperationRecord) { r.Steps[0].Input = json.RawMessage(`{"unexpected":true}`) }, wantStatus: "needs_operator", wantWrites: unavailable},
		{name: "later capability matches", setup: rewrite(func(d *adapter.Descriptor) {
			other := d.Capabilities[0]
			other.Name = "hosting.fake.other"
			d.Capabilities = append([]adapter.Capability{other}, d.Capabilities...)
		}), wantStatus: "running", wantCalls: 1, wantWrites: []string{advanceRefactorRunning, advanceRefactorClaimed, advanceRefactorSucceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) { advanceRefactorRun(t, tc) })
	}
}
