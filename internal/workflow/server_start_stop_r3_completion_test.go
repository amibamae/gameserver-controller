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

// TestStartStopR3Constructors pins the constructor refusals of both workflows.
func TestStartStopR3Constructors(t *testing.T) {
	f := newStartStopR3Fixture(t)
	const startDeps = "lifecycle store, adapter registry, schemas, and trusted start-profile resolver are required"
	const ttl = "readiness assertion TTL must be in the range 1ns..15m"
	startCases := []struct {
		name string
		make func() (*ServerStartWorkflow, error)
		want string
	}{
		{"no store", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(nil, f.adapters, f.schemas, f.profiles, time.Minute)
		}, startDeps},
		{"no adapters", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(f.servers, nil, f.schemas, f.profiles, time.Minute)
		}, startDeps},
		{"no schemas", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(f.servers, f.adapters, nil, f.profiles, time.Minute)
		}, startDeps},
		{"no profiles", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(f.servers, f.adapters, f.schemas, nil, time.Minute)
		}, startDeps},
		{"zero TTL", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(f.servers, f.adapters, f.schemas, f.profiles, 0)
		}, ttl},
		{"TTL over 15m", func() (*ServerStartWorkflow, error) {
			return NewServerStartWorkflow(f.servers, f.adapters, f.schemas, f.profiles, 15*time.Minute+1)
		}, ttl},
	}
	for _, test := range startCases {
		if workflow, err := test.make(); workflow != nil || startStopR3ErrText(err) != test.want {
			t.Fatalf("%s: %v, %v; want %q", test.name, workflow, err, test.want)
		}
	}
	if workflow, err := NewServerStartWorkflow(f.servers, f.adapters, f.schemas, f.profiles, 15*time.Minute); err != nil || workflow == nil {
		t.Fatalf("15m TTL = %v, %v", workflow, err)
	}
	const stopDeps = "lifecycle store, adapter registry, schemas, and trusted stop-profile resolver are required"
	if workflow, err := NewServerStopWorkflow(f.servers, f.adapters, f.schemas, nil); workflow != nil || startStopR3ErrText(err) != stopDeps {
		t.Fatalf("stop without profiles = %v, %v", workflow, err)
	}
}

// startStopR3Ready completes a planned start with passing readiness evidence
// observed now.
func startStopR3Ready(t *testing.T, f *startStopR3Fixture) store.OperationRecord {
	t.Helper()
	operation := startStopR3PlannedStart(t, f)
	observed := time.Now().UTC().Format(time.RFC3339Nano)
	results := []any{}
	for _, check := range startStopR3Checks() {
		results = append(results, map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion, "result": "pass", "observed_at": observed})
	}
	output := startStopR3MustJSON(t, map[string]any{
		"server_id": startTestServerID, "environment_id": startTestEnvID, "instance_ref": json.RawMessage(startTestInstance),
		"game_release_id": startTestRelease, "observed_at": observed, "check_results": results,
	})
	startStopR3SetStep(&operation, startReadinessStep, startStopR3Succeed(string(output)))
	f.server().LifecycleState = protocolstate.ServerValidating
	return operation
}

// TestStartStopR3StartValidateCompletion pins every server.start completion
// postcondition.
func TestStartStopR3StartValidateCompletion(t *testing.T) {
	const started = "execution unit start postcondition was not confirmed"
	const running = "execution unit is not confirmed running at the pinned reference"
	cases := []struct {
		name   string
		change func(*startStopR3Fixture, *store.OperationRecord)
		want   string
	}{
		{name: "complete"},
		{name: "wrong operation type", change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.stop" }),
			want: "server.start completion validation requires an operation and context"},
		{name: "start not succeeded", change: startStopR3Step(startRuntimeStep, startStopR3Status(protocolstate.StepFailed)), want: started},
		{name: "start not completed", change: startStopR3Step(startRuntimeStep, startStopR3Output(startStopR3UnitResult(startTestUnit, "accepted", "running"))), want: started},
		{name: "verification missing", change: startStopR3RemoveStep(startVerifyRuntimeStep), want: running},
		{name: "verification not running", change: startStopR3Step(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "starting"))), want: running},
		{name: "verification of another unit", change: startStopR3Step(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startStopR3OtherUnit, "running"))), want: running},
		{name: "server missing", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "missing" }), want: "operation not found"},
		{name: "readiness pending", change: startStopR3Step(startReadinessStep, startStopR3Status(protocolstate.StepPending)),
			want: "readiness evidence must come from a completed game.readiness.check step"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3Ready(t, f)
			if test.change != nil {
				test.change(f, &operation)
			}
			if got := startStopR3ErrText(f.start.ValidateCompletion(context.Background(), operation)); got != test.want {
				t.Fatalf("ValidateCompletion = %q; want %q", got, test.want)
			}
		})
	}
	f := newStartStopR3Fixture(t)
	operation := startStopR3Ready(t, f)
	startStopR3CheckCompletionContext(t, f.start.ValidateCompletion, operation, "server.start completion validation requires an operation and context")
}

func startStopR3CheckCompletionContext(t *testing.T, validate func(context.Context, store.OperationRecord) error, operation store.OperationRecord, want string) {
	t.Helper()
	var missing context.Context
	if got := startStopR3ErrText(validate(missing, operation)); got != want {
		t.Fatalf("nil context = %q", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validate(cancelled, operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
}

// TestStartStopR3StartSuccessAndReadyProjection pins the success result and
// every reason a terminal result is refused as ready evidence.
func TestStartStopR3StartSuccessAndReadyProjection(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Ready(t, f)
	result, err := f.start.BuildSuccessResult(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(decoded["operation_data"], &data); err != nil {
		t.Fatal(err)
	}
	want := `{"affected_resources":[` + startTestInstance + `],"audit_event_ids":[],"completed_steps":[{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"start-runtime","step_type":"execution.unit.start"},{"step_id":"verify-runtime","step_type":"execution.unit.inspect"},{"step_id":"check-readiness","step_type":"game.readiness.check"}],"failed_steps":[],"operation_data":{"environment_id":"staging","instance_ref":` + startTestInstance + `,"lifecycle_state":"ready","readiness_assertion":` + string(data["readiness_assertion"]) + `,"server_id":"start-test"},"operation_type":"server.start","outcome_summary":"Server runtime started and all required game readiness checks passed.","partial":false,"unresolved_conditions":[],"warnings":[]}`
	if string(result) != want {
		t.Fatalf("success result = %s\nwant %s", result, want)
	}
	operation.Status = protocolstate.OperationSucceeded
	operation.Result = result
	next, changed, err := f.start.ProjectLifecycle(operation, f.servers.record)
	startStopR3CheckProjection(t, next, changed, err, startStopR3Projection{state: protocolstate.ServerReady, desired: "running", changed: true})

	const refused = "terminal server.start result lacks its exact readiness assertion"
	edits := map[string]func(map[string]any, map[string]any){
		"wrong operation type":   func(r, _ map[string]any) { r["operation_type"] = "server.stop" },
		"other server":           func(_, d map[string]any) { d["server_id"] = "other" },
		"other environment":      func(_, d map[string]any) { d["environment_id"] = "other" },
		"not ready":              func(_, d map[string]any) { d["lifecycle_state"] = "validating" },
		"assertion not object":   func(_, d map[string]any) { d["readiness_assertion"] = "x" },
		"assertion fails schema": func(_, d map[string]any) { d["readiness_assertion"] = map[string]any{"x": 1} },
		"other instance":         func(_, d map[string]any) { d["instance_ref"] = json.RawMessage(startStopR3OtherInstance) },
	}
	for name, edit := range edits {
		changedResult := startStopR3EditResult(t, result, edit)
		candidate := operation
		candidate.Result = changedResult
		next, changed, err := f.start.ProjectLifecycle(candidate, f.servers.record)
		if startStopR3ErrText(err) != refused || changed || next.ServerID != "" {
			t.Fatalf("%s: projection = %#v, %v, %v", name, next, changed, err)
		}
	}
	candidate := operation
	candidate.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance)
	if _, _, err := f.start.ProjectLifecycle(candidate, f.servers.record); startStopR3ErrText(err) != refused {
		t.Fatalf("target instance mismatch = %v", err)
	}
	candidate.Target.InstanceRef = json.RawMessage(startTestInstance)
	if _, changed, err := f.start.ProjectLifecycle(candidate, f.servers.record); err != nil || !changed {
		t.Fatalf("matching target instance = %v, %v", changed, err)
	}
	if _, err := f.start.BuildSuccessResult(context.Background(), startStopR3Operation("server.stop", f.version(), `{}`)); err == nil {
		t.Fatal("success result built for another operation")
	}
}

func startStopR3EditResult(t *testing.T, raw json.RawMessage, edit func(result, data map[string]any)) json.RawMessage {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	data, ok := result["operation_data"].(map[string]any)
	if !ok {
		t.Fatalf("result has no operation data: %s", raw)
	}
	edit(result, data)
	return startStopR3MustJSON(t, result)
}

// startStopR3Stopped completes a planned stop with exact stopped evidence.
func startStopR3Stopped(t *testing.T, f *startStopR3Fixture, parameters string) store.OperationRecord {
	t.Helper()
	operation := startStopR3PlannedStop(t, f)
	operation.RequestSnapshot.Parameters = json.RawMessage(parameters)
	startStopR3SetStep(&operation, stopRuntimeStep, startStopR3Succeed(startStopR3UnitResult(startTestUnit, "completed", "stopped")))
	f.server().LifecycleState = protocolstate.ServerStopping
	return operation
}

// TestStartStopR3StopValidateCompletion pins every server.stop completion
// postcondition.
func TestStartStopR3StopValidateCompletion(t *testing.T) {
	const stopped = "exact execution unit is not confirmed stopped"
	runtime := func(output string) func(*startStopR3Fixture, *store.OperationRecord) {
		return startStopR3Step(stopRuntimeStep, startStopR3Output(output))
	}
	cases := []struct {
		name   string
		change func(*startStopR3Fixture, *store.OperationRecord)
		want   string
	}{
		{name: "complete"},
		{name: "wrong operation type", change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.start" }),
			want: "server.stop completion validation requires an operation and context"},
		{name: "save not confirmed", change: startStopR3Step(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "rejected"))),
			want: "game-native save was not confirmed"},
		{name: "stop missing", change: startStopR3RemoveStep(stopRuntimeStep), want: stopped},
		{name: "stop not succeeded", change: startStopR3Step(stopRuntimeStep, startStopR3Status(protocolstate.StepRunning)), want: stopped},
		{name: "stop undecodable", change: runtime(`[]`), want: stopped},
		{name: "stop accepted", change: runtime(startStopR3UnitResult(startTestUnit, "accepted", "stopped")), want: stopped},
		{name: "stop still stopping", change: runtime(startStopR3UnitResult(startTestUnit, "completed", "stopping")), want: stopped},
		{name: "stop of another unit", change: runtime(startStopR3UnitResult(startStopR3OtherUnit, "completed", "stopped")), want: stopped},
		{name: "stop without observation time", change: runtime(strings.Replace(startStopR3UnitResult(startTestUnit, "completed", "stopped"), startStopR3ObservedAt, "later", 1)),
			want: "execution stop output has an invalid observation time"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3Stopped(t, f, `{}`)
			if test.change != nil {
				test.change(f, &operation)
			}
			if got := startStopR3ErrText(f.stop.ValidateCompletion(context.Background(), operation)); got != test.want {
				t.Fatalf("ValidateCompletion = %q; want %q", got, test.want)
			}
		})
	}
	f := newStartStopR3Fixture(t)
	operation := startStopR3Stopped(t, f, `{}`)
	startStopR3CheckCompletionContext(t, f.stop.ValidateCompletion, operation, "server.stop completion validation requires an operation and context")
}

// TestStartStopR3StopSuccessResult pins the exact success result for each
// proxy-drain outcome.
func TestStartStopR3StopSuccessResult(t *testing.T) {
	cases := []struct {
		name, parameters, network, drain string
	}{
		{"drain by default without network", `{}`, "", "not_configured"},
		{"drain requested without network", `{"drain":true}`, "", "not_configured"},
		{"drain not requested", `{"drain":false}`, "proxy", "not_requested"},
		{"drain requested with network", `{"drain":true}`, "proxy", "unsupported"},
		{"undecodable parameters keep the default drain", `{"force":"yes"}`, "proxy", "unsupported"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3Stopped(t, f, test.parameters)
			f.server().NetworkProfile = test.network
			result, err := f.stop.BuildSuccessResult(context.Background(), operation)
			want := fmt.Sprintf(`{"affected_resources":[%[1]s],"audit_event_ids":[],"completed_steps":[{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"inspect-runtime","step_type":"execution.unit.inspect"},{"step_id":"save-game-data","step_type":"game.save.request"},{"step_id":"stop-runtime","step_type":"execution.unit.graceful_stop"}],"failed_steps":[],"operation_data":{"environment_id":"staging","execution":{"observed_at":%[3]q,"outcome":"stopped","unit_ref":%[2]s},"game_save":{"observed_at":%[3]q,"outcome":"confirmed"},"instance_ref":%[1]s,"lifecycle_state":"stopped","proxy_drain":{"outcome":%[4]q},"server_id":"start-test"},"operation_type":"server.stop","outcome_summary":"Game data was saved and the exact runtime unit was confirmed stopped.","partial":false,"unresolved_conditions":[],"warnings":[]}`,
				startTestInstance, startTestUnit, startStopR3ObservedAt, test.drain)
			startStopR3ExpectResult(t, result, err, want)
		})
	}
	f := newStartStopR3Fixture(t)
	operation := startStopR3Stopped(t, f, `{}`)
	f.server().ServerID = "renamed"
	if _, err := f.stop.BuildSuccessResult(context.Background(), operation); startStopR3ErrText(err) != "operation not found" {
		t.Fatalf("missing server = %v", err)
	}
	startStopR3SetStep(&operation, stopRuntimeStep, startStopR3Status(protocolstate.StepFailed))
	if _, err := f.stop.BuildSuccessResult(context.Background(), operation); startStopR3ErrText(err) != "exact execution unit is not confirmed stopped" {
		t.Fatalf("incomplete stop = %v", err)
	}
}

// TestStartStopR3StopProjectLifecycle pins every server.stop lifecycle
// projection branch and every reason a terminal result is refused.
func TestStartStopR3StopProjectLifecycle(t *testing.T) {
	f := newStartStopR3Fixture(t)
	completed := startStopR3Stopped(t, f, `{}`)
	result, err := f.stop.BuildSuccessResult(context.Background(), completed)
	if err != nil {
		t.Fatal(err)
	}
	const refused = "terminal server.stop result lacks exact save and termination evidence"
	cases := []struct {
		name   string
		state  protocolstate.ServerState
		change func(*store.OperationRecord)
		want   startStopR3Projection
	}{
		{name: "wrong operation type", state: protocolstate.ServerReady, change: func(o *store.OperationRecord) { o.OperationType = "server.start" },
			want: startStopR3Projection{err: "server.stop lifecycle projection target mismatch"}},
		{name: "other server", state: protocolstate.ServerReady, change: func(o *store.OperationRecord) { o.Target.ServerID = "other" },
			want: startStopR3Projection{err: "server.stop lifecycle projection target mismatch"}},
		{name: "ready and failed", state: protocolstate.ServerReady, change: func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed },
			want: startStopR3Projection{state: protocolstate.ServerDegraded, desired: "stopped", changed: true}},
		{name: "ready with pending save", state: protocolstate.ServerReady, change: func(o *store.OperationRecord) {
			startStopR3SetStep(o, stopSaveGameStep, startStopR3Status(protocolstate.StepPending))
		}, want: startStopR3Projection{state: protocolstate.ServerReady, desired: "stopped"}},
		{name: "ready with dispatched save", state: protocolstate.ServerReady,
			want: startStopR3Projection{state: protocolstate.ServerStopping, desired: "stopped", changed: true}},
		{name: "stopping and running", state: protocolstate.ServerStopping, want: startStopR3Projection{state: protocolstate.ServerStopping, desired: "stopped"}},
		{name: "stopping and failed", state: protocolstate.ServerStopping, change: func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed },
			want: startStopR3Projection{state: protocolstate.ServerFailed, desired: "stopped", changed: true}},
		{name: "stopping and succeeded", state: protocolstate.ServerStopping, change: func(o *store.OperationRecord) {
			o.Status, o.Result = protocolstate.OperationSucceeded, result
		}, want: startStopR3Projection{state: protocolstate.ServerStopped, desired: "stopped", changed: true}},
		{name: "succeeded with pinned target", state: protocolstate.ServerStopping, change: func(o *store.OperationRecord) {
			o.Status, o.Result, o.Target.InstanceRef = protocolstate.OperationSucceeded, result, json.RawMessage(startTestInstance)
		}, want: startStopR3Projection{state: protocolstate.ServerStopped, desired: "stopped", changed: true}},
		{name: "succeeded with other target", state: protocolstate.ServerStopping, change: func(o *store.OperationRecord) {
			o.Status, o.Result, o.Target.InstanceRef = protocolstate.OperationSucceeded, result, json.RawMessage(startStopR3OtherInstance)
		}, want: startStopR3Projection{err: refused}},
		{name: "stopped is unchanged", state: protocolstate.ServerStopped, change: func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed },
			want: startStopR3Projection{state: protocolstate.ServerStopped, desired: "stopped"}},
	}
	edits := map[string]func(map[string]any, map[string]any){
		"result of another operation": func(r, _ map[string]any) { r["operation_type"] = "server.start" },
		"result for another server":   func(_, d map[string]any) { d["server_id"] = "other" },
		"result in another env":       func(_, d map[string]any) { d["environment_id"] = "other" },
		"result not stopped":          func(_, d map[string]any) { d["lifecycle_state"] = "stopping" },
		"result for another instance": func(_, d map[string]any) { d["instance_ref"] = json.RawMessage(startStopR3OtherInstance) },
		"result fails schema":         func(r, _ map[string]any) { r["extra"] = true },
	}
	for name, edit := range edits {
		edited := startStopR3EditResult(t, result, edit)
		cases = append(cases, struct {
			name   string
			state  protocolstate.ServerState
			change func(*store.OperationRecord)
			want   startStopR3Projection
		}{name: name, state: protocolstate.ServerStopping, change: func(o *store.OperationRecord) {
			o.Status, o.Result = protocolstate.OperationSucceeded, edited
		}, want: startStopR3Projection{err: refused}})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStop(t, f)
			f.server().LifecycleState = test.state
			f.server().DesiredState = "stopped"
			if test.change != nil {
				test.change(&operation)
			}
			next, changed, err := f.stop.ProjectLifecycle(operation, f.servers.record)
			startStopR3CheckProjection(t, next, changed, err, test.want)
		})
	}
}
