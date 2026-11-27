package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	engineR5MissingDependency = "operation store, adapter registry, schemas, workflow definitions, and resume authorizer are required"
	engineR5InvalidLimits     = "protocol version, step timeout (1ns..24h), and maximum step attempts (1..10) are required"
)

func TestEngineR5NewEngineChecksDependenciesThenLimits(t *testing.T) {
	fixture := newEngineFixture(t, "engine-r5-new", "1", nil)
	adapters, schemas := fixture.engine.adapters, fixture.engine.schemas
	definitions, authorizer := NewDefinitions(), fixture.authorizer
	type arguments struct {
		store      store.OperationStore
		adapters   adapterRuntime
		schemas    schemaRuntime
		defs       *Definitions
		authorizer ResumeAuthorizer
		timeout    time.Duration
		attempts   int
	}
	valid := func(change func(*arguments)) arguments {
		args := arguments{fixture.store, adapters, schemas, definitions, authorizer, time.Minute, 3}
		if change != nil {
			change(&args)
		}
		return args
	}
	for _, tc := range []struct {
		name string
		args arguments
		want string
	}{
		{name: "valid", args: valid(nil)},
		{name: "nil store", args: valid(func(a *arguments) { a.store = nil }), want: engineR5MissingDependency},
		{name: "nil adapters", args: valid(func(a *arguments) { a.adapters = nil }), want: engineR5MissingDependency},
		{name: "nil schemas", args: valid(func(a *arguments) { a.schemas = nil }), want: engineR5MissingDependency},
		{name: "nil definitions", args: valid(func(a *arguments) { a.defs = nil }), want: engineR5MissingDependency},
		{name: "nil authorizer", args: valid(func(a *arguments) { a.authorizer = nil }), want: engineR5MissingDependency},
		{name: "missing dependency is reported before bad limits", args: valid(func(a *arguments) { a.store, a.timeout, a.attempts = nil, 0, 0 }), want: engineR5MissingDependency},
		{name: "no protocol version", args: valid(func(a *arguments) { a.schemas = engineR5NewSchemas(schemas, "") }), want: engineR5InvalidLimits},
		{name: "zero timeout", args: valid(func(a *arguments) { a.timeout = 0 }), want: engineR5InvalidLimits},
		{name: "negative timeout", args: valid(func(a *arguments) { a.timeout = -time.Second }), want: engineR5InvalidLimits},
		{name: "shortest timeout", args: valid(func(a *arguments) { a.timeout = time.Nanosecond })},
		{name: "longest timeout", args: valid(func(a *arguments) { a.timeout = 24 * time.Hour })},
		{name: "timeout over a day", args: valid(func(a *arguments) { a.timeout = 24*time.Hour + time.Nanosecond }), want: engineR5InvalidLimits},
		{name: "zero attempts", args: valid(func(a *arguments) { a.attempts = 0 }), want: engineR5InvalidLimits},
		{name: "one attempt", args: valid(func(a *arguments) { a.attempts = 1 })},
		{name: "ten attempts", args: valid(func(a *arguments) { a.attempts = 10 })},
		{name: "eleven attempts", args: valid(func(a *arguments) { a.attempts = 11 }), want: engineR5InvalidLimits},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.args
			engine, err := NewEngine(a.store, a.adapters, a.schemas, a.defs, a.authorizer, a.timeout, a.attempts)
			engineR5ExpectErr(t, err, tc.want)
			if tc.want != "" {
				if engine != nil {
					t.Fatal("engine returned with an error")
				}
				return
			}
			if engine.stepTimeout != a.timeout || engine.maxStepAttempts != a.attempts || engine.activeOperations == nil ||
				engine.store != a.store || engine.definitions != a.defs {
				t.Fatalf("engine not configured from its arguments: %+v", engine)
			}
		})
	}
}

// engineR5UpdateCase drives updateOperation directly with one store and
// one registered definition.
type engineR5UpdateCase struct {
	name          string
	operationType string
	definition    func(*testDefinition) Definition
	lifecycle     bool
	getErr        error
	failAt        int
	wantErr       string
	wantIs        error
	wantRevision  uint64
	wantWrites    []string
	wantServer    protocolstate.ServerState
}

func engineR5Projecting(transition bool, change func(*store.LogicalServer), err error) func(*testDefinition) Definition {
	return func(base *testDefinition) Definition {
		return engineR5Projector{base, func(_ store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
			next := current.Record
			if change != nil {
				change(&next)
			}
			return next, transition, err
		}}
	}
}

func TestEngineR5UpdateOperationRoutesLifecycleProjection(t *testing.T) {
	errLoad := errors.New("server row unreadable")
	errProject := errors.New("projection refused")
	plain := func(base *testDefinition) Definition { return base }
	stopping := func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerStopping }
	renamed := func(server *store.LogicalServer) { server.DisplayName = "renamed" }
	const written = "running cur=- err=- | step-1:pending#0"
	for _, tc := range []engineR5UpdateCase{
		{name: "server.define is written directly", operationType: "server.define", wantRevision: 2, wantWrites: []string{written}},
		{name: "server.define skips a registered projector", operationType: "server.define", definition: engineR5Projecting(false, nil, errProject), wantRevision: 2, wantWrites: []string{written}},
		{name: "unregistered server operation", operationType: "server.other", wantErr: `operation "server.other" has no registered lifecycle projector`},
		{name: "server operation without projector", operationType: "server.other", definition: plain, wantErr: `operation "server.other" has no registered lifecycle projector`},
		{name: "unregistered other operation", operationType: "test.other", wantRevision: 2, wantWrites: []string{written}},
		{name: "other operation without projector", operationType: "test.other", definition: plain, wantRevision: 2, wantWrites: []string{written}},
		{name: "projector needs an atomic store", operationType: "test.other", definition: engineR5Projecting(true, stopping, nil),
			wantErr: "lifecycle workflow requires an atomic logical-server operation store"},
		{name: "server read fails", operationType: "server.other", definition: engineR5Projecting(true, stopping, nil), lifecycle: true, getErr: errLoad,
			wantErr: "load logical server before lifecycle projection: server row unreadable", wantIs: errLoad},
		{name: "projection fails", operationType: "server.other", definition: engineR5Projecting(true, stopping, errProject), lifecycle: true,
			wantErr: "project logical-server lifecycle state: projection refused", wantIs: errProject},
		{name: "change without transition", operationType: "server.other", definition: engineR5Projecting(false, renamed, nil), lifecycle: true,
			wantErr: "lifecycle projector changed the server record without a state transition"},
		{name: "no transition writes the operation only", operationType: "server.other", definition: engineR5Projecting(false, nil, nil), lifecycle: true,
			wantRevision: 2, wantWrites: []string{written}, wantServer: protocolstate.ServerReady},
		{name: "transition commits both records", operationType: "server.other", definition: engineR5Projecting(true, stopping, nil), lifecycle: true,
			wantRevision: 2, wantWrites: []string{written + " +server=stopping"}, wantServer: protocolstate.ServerStopping},
		{name: "atomic commit fails", operationType: "server.other", definition: engineR5Projecting(true, stopping, nil), lifecycle: true, failAt: 1,
			wantErr: errAdvanceRefactorWrite.Error(), wantIs: errAdvanceRefactorWrite, wantWrites: []string{"FAILED " + written + " +server=stopping"}, wantServer: protocolstate.ServerReady},
	} {
		t.Run(tc.name, func(t *testing.T) { engineR5RunUpdate(t, tc) })
	}
}

func engineR5RunUpdate(t *testing.T, tc engineR5UpdateCase) {
	t.Helper()
	env := engineR5NewEnv(t, engineR5Options{})
	recording := env.store.advanceRefactorStore
	recording.failAt = tc.failAt
	recording.server = store.StoredLogicalServer{Revision: 1, Record: store.LogicalServer{ServerID: "server-1", EnvironmentID: "staging", LifecycleState: protocolstate.ServerReady}}
	if tc.lifecycle {
		env.engine.store = engineR5LifecycleStore{advanceRefactorLifecycleStore{recording}, tc.getErr}
	}
	definitions := NewDefinitions()
	if tc.definition != nil {
		if err := definitions.Register(tc.operationType, tc.definition(env.fixture.definition)); err != nil {
			t.Fatal(err)
		}
	}
	env.engine.definitions = definitions
	current, err := recording.GetOperation(env.id)
	if err != nil {
		t.Fatal(err)
	}
	next := current.Record
	next.OperationType = tc.operationType
	next.Status = protocolstate.OperationRunning
	updated, err := env.engine.updateOperation(current.Revision, next)
	engineR5ExpectErr(t, err, tc.wantErr)
	if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
		t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
	}
	if updated.Revision != tc.wantRevision {
		t.Errorf("returned revision = %d, want %d", updated.Revision, tc.wantRevision)
	}
	if tc.wantErr == "" && updated.Record.OperationType != tc.operationType {
		t.Errorf("returned operation type = %q", updated.Record.OperationType)
	}
	if tc.wantErr != "" && updated.Record.OperationID != "" {
		t.Errorf("returned a record with an error: %+v", updated)
	}
	engineR5ExpectWrites(t, env, tc.wantWrites...)
	if tc.wantServer != "" && recording.server.Record.LifecycleState != tc.wantServer {
		t.Errorf("server state = %q, want %q", recording.server.Record.LifecycleState, tc.wantServer)
	}
}

func TestEngineR5RunRequiresContextAndOperation(t *testing.T) {
	env := engineR5NewEnv(t, engineR5Options{})
	var noContext context.Context
	for _, call := range []func() (store.StoredOperation, error){
		func() (store.StoredOperation, error) { return env.engine.Run(noContext, env.id) },
		func() (store.StoredOperation, error) { return env.engine.Run(env.ctx, "") },
	} {
		stored, err := call()
		engineR5ExpectErr(t, err, "context and operation ID are required")
		if stored.Record.OperationID != "" {
			t.Errorf("returned a record: %+v", stored)
		}
	}
	stored, err := env.engine.Run(env.ctx, "absent")
	if !errors.Is(err, store.ErrNotFound) || stored.Record.OperationID != "" {
		t.Errorf("missing operation = %+v, %v", stored, err)
	}
	if env.store.gets != 1 || len(env.store.engineR5Writes()) != 0 {
		t.Errorf("reads = %d, writes = %v", env.store.gets, env.store.engineR5Writes())
	}
}

func TestEngineR5RunStopsAtTheFirstSettledState(t *testing.T) {
	accepted := []fakeadapter.Reply{{Outcome: adapter.OutcomeAccepted, Output: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC()}}
	for _, tc := range []struct {
		name       string
		options    engineR5Options
		setup      func(*engineR5Env)
		wantStatus protocolstate.OperationStatus
		wantErr    string
		wantIs     error
		wantGets   int
	}{
		{name: "succeeds after two advances", wantStatus: protocolstate.OperationSucceeded, wantGets: 3},
		{name: "stops while waiting", options: engineR5Options{replies: accepted}, wantStatus: protocolstate.OperationWaiting, wantGets: 2},
		{name: "stops when an operator is needed", options: engineR5Options{capabilityVersion: "2"}, wantStatus: protocolstate.OperationNeedsOperator, wantGets: 2},
		{name: "returns an advance error", options: engineR5Options{seed: engineR5Seed(protocolstate.OperationNeedsOperator, "")},
			wantStatus: protocolstate.OperationNeedsOperator, wantErr: ErrNeedsOperator.Error(), wantIs: ErrNeedsOperator, wantGets: 2},
		{name: "stops when the context ends between advances", setup: func(env *engineR5Env) {
			env.store.onWrite = func(attempt int) {
				if attempt == 3 {
					env.cancel()
				}
			}
		}, wantStatus: protocolstate.OperationRunning, wantErr: context.Canceled.Error(), wantIs: context.Canceled, wantGets: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, tc.options)
			if tc.setup != nil {
				tc.setup(env)
			}
			stored, err := env.engine.Run(env.ctx, env.id)
			engineR5ExpectErr(t, err, tc.wantErr)
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
			}
			if stored.Record.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", stored.Record.Status, tc.wantStatus)
			}
			if env.store.gets != tc.wantGets {
				t.Errorf("reads = %d, want %d", env.store.gets, tc.wantGets)
			}
		})
	}
}

func TestEngineR5RunSuspendsAtItsTransitionLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempts  int
		extra     []store.OperationStep
		wantLimit int
	}{
		{name: "one attempt and one step is raised to eight", attempts: 1, wantLimit: 8},
		{name: "three attempts and one step", attempts: 3, wantLimit: 8},
		{name: "three attempts and two steps", attempts: 3, extra: []store.OperationStep{advanceRefactorPlainStep()}, wantLimit: 12},
		{name: "ten attempts and two steps", attempts: 10, extra: []store.OperationStep{advanceRefactorPlainStep()}, wantLimit: 26},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, engineR5Options{
				mutating: true, replies: engineR5Completed(tc.wantLimit + 1), extraSteps: tc.extra, maxStepAttempts: tc.attempts, forget: true,
				seed: engineR5Seed(protocolstate.OperationRunning, ""),
			})
			stored, err := env.engine.Run(env.ctx, env.id)
			engineR5ExpectErr(t, err, "")
			if env.store.gets != tc.wantLimit+1 || len(env.fixture.adapter.Calls()) != tc.wantLimit {
				t.Errorf("reads = %d, calls = %d, want %d advances", env.store.gets, len(env.fixture.adapter.Calls()), tc.wantLimit)
			}
			if stored.Record.Status != protocolstate.OperationNeedsOperator {
				t.Fatalf("status = %q", stored.Record.Status)
			}
			engineR5ExpectError(t, "transition-limit error", stored.Record.Error, protocolError("core.workflow.transition_limit", "safety_blocked",
				"Workflow advancement exceeded its bounded transition count.", env.id, false, "operator_review", true))
		})
	}
}

func TestEngineR5ResponseProblem(t *testing.T) {
	const (
		requestID      = "request-1"
		outputSchema   = "urn:test:output"
		responseSchema = "urn:gameserver-controller:9.9.9:rpc:adapter-invoke-result"
		invalidCode    = "core.adapter.invalid_response"
		invalidMessage = "The adapter returned a malformed response after dispatch."
	)
	valid := func(change func(*adapter.InvokeResponse)) adapter.InvokeResponse {
		response := adapter.InvokeResponse{JSONRPC: "2.0", ID: requestID, Result: adapter.InvokeResult{
			Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{}`), ObservedAt: time.Unix(1, 0).UTC(),
		}}
		if change != nil {
			change(&response)
		}
		return response
	}
	both := responseSchema + "," + outputSchema
	for _, tc := range []struct {
		name          string
		response      adapter.InvokeResponse
		failing       []string
		wantCode      string
		wantMessage   string
		wantValidated string
	}{
		{name: "completed", response: valid(nil), wantValidated: both},
		{name: "accepted", response: valid(func(r *adapter.InvokeResponse) { r.Result.Outcome = adapter.OutcomeAccepted }), wantValidated: both},
		{name: "unknown", response: valid(func(r *adapter.InvokeResponse) { r.Result.Outcome = adapter.OutcomeUnknown }), wantValidated: both},
		{name: "unencodable output", response: valid(func(r *adapter.InvokeResponse) { r.Result.Output = json.RawMessage(`{`) }), wantCode: invalidCode, wantMessage: invalidMessage},
		{name: "wrong JSON-RPC version", response: valid(func(r *adapter.InvokeResponse) { r.JSONRPC = "1.0" }), wantCode: invalidCode, wantMessage: invalidMessage},
		{name: "wrong request ID", response: valid(func(r *adapter.InvokeResponse) { r.ID = "other" }), wantCode: invalidCode, wantMessage: invalidMessage},
		{name: "no observation time", response: valid(func(r *adapter.InvokeResponse) { r.Result.ObservedAt = time.Time{} }), wantCode: invalidCode, wantMessage: invalidMessage},
		{name: "response schema fails", response: valid(nil), failing: []string{responseSchema}, wantCode: invalidCode, wantMessage: invalidMessage, wantValidated: responseSchema},
		{name: "output schema fails", response: valid(nil), failing: []string{outputSchema}, wantCode: invalidCode, wantMessage: invalidMessage, wantValidated: both},
		{name: "unsupported outcome", response: valid(func(r *adapter.InvokeResponse) { r.Result.Outcome = adapter.Outcome("finished") }),
			wantCode: "core.adapter.invalid_outcome", wantMessage: "The adapter returned an unsupported outcome after dispatch.", wantValidated: both},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schemas := engineR5NewSchemas(nil, "9.9.9", tc.failing...)
			engine := &Engine{schemas: schemas}
			code, message := engine.responseProblem(tc.response, requestID, adapter.Capability{OutputSchemaURI: outputSchema})
			if code != tc.wantCode || message != tc.wantMessage {
				t.Errorf("problem = %q, %q; want %q, %q", code, message, tc.wantCode, tc.wantMessage)
			}
			if got := schemas.engineR5Validated(); got != tc.wantValidated {
				t.Errorf("validated schemas = %q, want %q", got, tc.wantValidated)
			}
		})
	}
}
