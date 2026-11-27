package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin the server.start and server.stop branches that the round-3
// characterization suite leaves uncovered, plus the order of rules when more
// than one fails at once. They pass on the code before the round-4 refactor.

// startStopR4LenientSchemas accepts every document whose URI ends with
// accept, so a test can reach checks that run after schema validation.
type startStopR4LenientSchemas struct {
	schemaRuntime
	accept string
}

func (s startStopR4LenientSchemas) ValidateJSON(uri string, raw []byte) error {
	if strings.HasSuffix(uri, s.accept) {
		return nil
	}
	return s.schemaRuntime.ValidateJSON(uri, raw)
}

// startStopR4FlakyAdapters answers Descriptor from the fixture until the
// failAt-th lookup of adapterID, which it replaces with replace.
type startStopR4FlakyAdapters struct {
	*startStopR3Adapters
	adapterID string
	failAt    int
	lookups   int
	replace   func(adapter.Descriptor) (adapter.Descriptor, error)
}

func (a *startStopR4FlakyAdapters) Descriptor(adapterID string) (adapter.Descriptor, error) {
	descriptor, err := a.startStopR3Adapters.Descriptor(adapterID)
	if adapterID != a.adapterID {
		return descriptor, err
	}
	a.lookups++
	if a.lookups == a.failAt {
		return a.replace(descriptor)
	}
	return descriptor, err
}

// startStopR4FlakyServers answers GetLogicalServer from the fixture until the
// failAt-th load, which fails.
type startStopR4FlakyServers struct {
	*startStopR3Servers
	failAt int
	loads  int
}

func (s *startStopR4FlakyServers) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	s.loads++
	if s.loads == s.failAt {
		return store.StoredLogicalServer{}, errors.New("store unavailable")
	}
	return s.startStopR3Servers.GetLogicalServer(environmentID, serverID)
}

func startStopR4StartWorkflow(t *testing.T, f *startStopR3Fixture, servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime) *ServerStartWorkflow {
	t.Helper()
	workflow, err := NewServerStartWorkflow(servers, adapters, schemas, f.profiles, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return workflow
}

func startStopR4StopWorkflow(t *testing.T, f *startStopR3Fixture, schemas schemaRuntime) *ServerStopWorkflow {
	t.Helper()
	workflow, err := NewServerStopWorkflow(f.servers, f.adapters, schemas, f.profiles)
	if err != nil {
		t.Fatal(err)
	}
	return workflow
}

// TestStartStopR4StartPlanRechecksGameDescriptor pins the game adapter
// descriptor lookup that runs after every capability was resolved.
func TestStartStopR4StartPlanRechecksGameDescriptor(t *testing.T) {
	cases := []struct {
		name    string
		replace func(adapter.Descriptor) (adapter.Descriptor, error)
		want    string
	}{
		{name: "lookup fails", replace: func(adapter.Descriptor) (adapter.Descriptor, error) {
			return adapter.Descriptor{}, errors.New("registry closed")
		}, want: `resolve game adapter "game.paper": registry closed`},
		{name: "wrong category", replace: func(d adapter.Descriptor) (adapter.Descriptor, error) {
			d.Category = adapter.CategoryHosting
			return d, nil
		}, want: "configured game adapter has the wrong category"},
		{name: "readiness capability gone", replace: func(d adapter.Descriptor) (adapter.Descriptor, error) {
			d.Capabilities = nil
			return d, nil
		}, want: "game adapter does not declare game.readiness.check"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			flaky := &startStopR4FlakyAdapters{startStopR3Adapters: f.adapters, adapterID: "game.paper", failAt: 2, replace: test.replace}
			workflow := startStopR4StartWorkflow(t, f, f.servers, flaky, f.schemas)
			steps, err := workflow.BuildPlan(context.Background(), startStopR3Operation("server.start", f.version(), `{}`))
			if got := startStopR3ErrText(err); got != test.want || steps != nil {
				t.Fatalf("BuildPlan = %d steps, %q; want %q", len(steps), got, test.want)
			}
		})
	}
}

// TestStartStopR4StartPlanRuleOrder pins which rule wins when several fail.
func TestStartStopR4StartPlanRuleOrder(t *testing.T) {
	const stateRequired = "server.start requires a stopped logical server with a managed instance"
	cases := []startStopR3PlanCase{
		{name: "operation shape before server load", change: startStopR3Op(func(o *store.OperationRecord) {
			o.ProtocolVersion = "0.0.1"
			o.Target.ServerID = "missing"
		}), want: "server.start operation and loaded protocol version are required"},
		{name: "state before target instance", change: func(f *startStopR3Fixture, o *store.OperationRecord) {
			f.server().LifecycleState = protocolstate.ServerReady
			o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance)
		}, want: stateRequired},
		{name: "target instance before expected instance", params: `{"expected_instance_ref":` + startStopR3OtherInstance + `}`,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }),
			want:   store.ErrLogicalServerTargetMismatch.Error()},
		{name: "expected instance before profile", params: `{"expected_instance_ref":` + startStopR3OtherInstance + `}`,
			change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.profiles.startErr = errors.New("no profile") },
			want:   "expected_instance_ref does not match the current managed instance"},
		{name: "profile selection before unit binding", change: startStopR3StartProfile(func(p *ServerStartProfile) {
			p.GameReleaseID = ""
			p.UnitRef = json.RawMessage(`null`)
		}), want: "trusted start profile must select a release and bounded readiness check set"},
		{name: "unit binding before checks", change: startStopR3StartProfile(func(p *ServerStartProfile) {
			p.UnitRef = json.RawMessage(`null`)
			p.RequiredChecks[0].CheckID = ""
		}), want: "trusted start profile unit reference does not bind the configured execution adapter and environment"},
		{name: "profile before capabilities", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.profiles.start.GameReleaseID = ""
			delete(f.adapters.descriptors, "hosting.fake")
		}, want: "trusted start profile must select a release and bounded readiness check set"},
		{name: "first capability in plan order", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "game.paper", "game.readiness.check")
			startStopR3DropCapability(f, "execution.fake", "execution.unit.inspect")
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.inspect`},
		{name: "capabilities before readiness declaration", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.start")
			startStopR3EditCapability(f, "game.paper", "game.readiness.check", func(c *adapter.Capability) { c.Constraints = nil })
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.start`},
		{name: "readiness declaration before input schemas", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "game.paper", "game.readiness.check", func(c *adapter.Capability) { c.Constraints = nil })
			f.schemas.reject = ":hosting:instance-inspect-input"
		}, want: "trusted readiness profile does not match an exact game-adapter release, environment, set, and check-version declaration"},
		{name: "first input schema in plan order", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = "-input"
		}, want: "planned inspect-instance input does not match its declared capability schema: rejected by test"},
	}
	startStopR3RunPlanRefusals(t, startStopR3StartTarget, cases)
}

// TestStartStopR4StopPlanRuleOrder pins which rule wins when several fail.
func TestStartStopR4StopPlanRuleOrder(t *testing.T) {
	cases := []startStopR3PlanCase{
		{name: "operation shape before parameters", params: `{"force":"yes"}`,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Target.EnvironmentID = "" }),
			want:   "server.stop operation and loaded protocol version are required"},
		{name: "force before approval", params: `{"force":true,"approval_id":"a","graceful_timeout_ms":0}`,
			want: "force stop is unavailable until exact-target approval validation is wired"},
		{name: "state before target instance", change: func(f *startStopR3Fixture, o *store.OperationRecord) {
			f.server().LifecycleState = protocolstate.ServerStopped
			o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance)
		}, want: "server.stop requires a ready logical server with a managed instance"},
		{name: "drain before profile", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.server().NetworkProfile = "proxy"
			f.profiles.stopErr = errors.New("no profile")
		}, want: "configured proxy drain requires a trusted proxy profile and adapter integration"},
		{name: "release before unit binding", change: startStopR3StopProfile(func(p *ServerStopProfile) {
			p.GameReleaseID = ""
			p.UnitRef = json.RawMessage(`null`)
		}), want: "trusted stop profile must resolve a bounded game release"},
		{name: "profile before capabilities", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.profiles.stop.UnitRef = json.RawMessage(`null`)
			delete(f.adapters.descriptors, "hosting.fake")
		}, want: "trusted stop profile unit reference does not bind the configured execution adapter and environment"},
		{name: "first capability in plan order", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.graceful_stop")
			startStopR3DropCapability(f, "game.paper", "game.save.request")
		}, want: `adapter "game.paper" does not advertise required capability game.save.request`},
		{name: "capabilities before input schemas", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.graceful_stop")
			f.schemas.reject = ":hosting:instance-inspect-input"
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.graceful_stop`},
		{name: "first input schema in plan order", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = "-input"
		}, want: "planned inspect-instance input does not match its declared capability schema: rejected by test"},
	}
	startStopR3RunPlanRefusals(t, startStopR3StopTarget, cases)
	f := newStartStopR3Fixture(t)
	if _, err := (*ServerStopWorkflow)(nil).BuildPlan(context.Background(), startStopR3Operation("server.stop", f.version(), `{}`)); startStopR3ErrText(err) != "initialized stop workflow and context are required" {
		t.Fatalf("nil workflow = %v", err)
	}
}

// TestStartStopR4PreconditionRuleOrder pins which precondition message wins
// when several rules fail for the same step.
func TestStartStopR4PreconditionRuleOrder(t *testing.T) {
	stopped, starting := protocolstate.ServerStopped, protocolstate.ServerStarting
	inactive := startStopR3Step(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "stopped")))
	otherVerify := startStopR3Step(startVerifyRuntimeStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`))
	both := func(first, second func(*startStopR3Fixture, *store.OperationRecord)) func(*startStopR3Fixture, *store.OperationRecord) {
		return func(f *startStopR3Fixture, o *store.OperationRecord) {
			first(f, o)
			second(f, o)
		}
	}
	startCases := []startStopR3PreconditionCase{
		{name: "target mismatch before inspect binding", step: startInspectInstanceStep, state: stopped,
			change: both(startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }),
				startStopR3Step(startInspectInstanceStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`))),
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "runtime state before inactive instance", step: startRuntimeStep, state: starting, change: inactive,
			want: "runtime start requires a stopped server or confirmed start retry and a successful provider inspection"},
		{name: "runtime inactive instance before unit binding", step: startRuntimeStep, state: stopped, change: both(inactive, otherVerify),
			want: "runtime start requires the exact provider instance to be active"},
		{name: "verify state before unit binding", step: startVerifyRuntimeStep, state: stopped, change: otherVerify,
			want: "runtime inspection requires a completed start command"},
		{name: "readiness not running before unit binding", step: startReadinessStep, state: starting,
			change: both(startStopR3Step(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "stopped"))), otherVerify),
			want:   "game readiness requires the execution unit to be confirmed running"},
		{name: "readiness unit binding before instance binding", step: startReadinessStep, state: starting,
			change: both(otherVerify, startStopR3Step(startReadinessStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`))),
			want:   "readiness request must be bound to the verified unit and current instance"},
	}
	startStopR3RunPreconditions(t, startStopR3StartChecks, startCases)

	ready, stopping := protocolstate.ServerReady, protocolstate.ServerStopping
	stopInactive := startStopR3Step(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "stopped")))
	stopCases := []startStopR3PreconditionCase{
		{name: "target mismatch before plan binding", step: stopRuntimeStep, state: stopping,
			change: both(startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }),
				startStopR3Step(stopInspectInstanceStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`))),
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "unit inspect state before inactive instance", step: stopInspectUnitStep, state: stopping, change: stopInactive,
			want: "runtime inspection requires the ready server and successful provider inspection"},
		{name: "save inactive instance before unit state", step: stopSaveGameStep, state: ready,
			change: both(stopInactive, startStopR3Step(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "stopped")))),
			want:   "game save requires the exact provider instance to remain active"},
		{name: "stop save before unit binding", step: stopRuntimeStep, state: stopping,
			change: both(startStopR3Step(stopSaveGameStep, startStopR3Output(`[]`)),
				startStopR3Step(stopInspectUnitStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`))),
			want: "graceful stop requires a confirmed game save and stopping lifecycle state"},
	}
	startStopR3RunPreconditions(t, startStopR3StopChecks, stopCases)
}

// TestStartStopR4ReconcileRejectsUnparseableObservationTime pins the
// observation-time check that runs after the output schema accepted the reply.
func TestStartStopR4ReconcileRejectsUnparseableObservationTime(t *testing.T) {
	output := strings.Replace(startStopR3UnitOutput(startTestUnit, "running"), startStopR3ObservedAt, "not-a-time", 1)
	lenient := func(f *startStopR3Fixture) schemaRuntime {
		return startStopR4LenientSchemas{schemaRuntime: f.schemas, accept: ":execution:systemd-unit-inspect-output"}
	}
	t.Run("start", func(t *testing.T) {
		f := newStartStopR3Fixture(t)
		operation := startStopR3PlannedStart(t, f)
		startStopR3SetStep(&operation, startRuntimeStep, startStopR3Ambiguous)
		workflow := startStopR4StartWorkflow(t, f, f.servers, f.adapters, lenient(f))
		test := startStopR3ReconcileCase{step: startRuntimeStep, reply: startStopR3Reply(output), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1}
		startStopR3RunReconcile(t, f, operation, test, workflow.Reconcile)
	})
	t.Run("stop", func(t *testing.T) {
		f := newStartStopR3Fixture(t)
		operation := startStopR3PlannedStop(t, f)
		startStopR3SetStep(&operation, stopRuntimeStep, startStopR3Ambiguous)
		workflow := startStopR4StopWorkflow(t, f, lenient(f))
		test := startStopR3ReconcileCase{step: stopRuntimeStep, reply: startStopR3Reply(output), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1}
		startStopR3RunReconcile(t, f, operation, test, workflow.Reconcile)
	})
}

// TestStartStopR4StartReconcileWithUndecodableUnitInput pins that an
// ambiguous start whose own input cannot be decoded is never confirmed.
func TestStartStopR4StartReconcileWithUndecodableUnitInput(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3PlannedStart(t, f)
	startStopR3SetStep(&operation, startRuntimeStep, startStopR3Ambiguous)
	test := startStopR3ReconcileCase{step: startRuntimeStep, stepChange: startStopR3Input(`[]`),
		reply: startStopR3Reply(startStopR3UnitOutput(startTestUnit, "running")), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1}
	startStopR3RunReconcile(t, f, operation, test, f.start.Reconcile)
}

// TestStartStopR4StartCompletionWithUndecodableUnitInput pins the start
// postcondition when the planned start input cannot be decoded.
func TestStartStopR4StartCompletionWithUndecodableUnitInput(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Ready(t, f)
	startStopR3SetStep(&operation, startRuntimeStep, startStopR3Input(`[]`))
	if got := startStopR3ErrText(f.start.ValidateCompletion(context.Background(), operation)); got != "execution unit start postcondition was not confirmed" {
		t.Fatalf("ValidateCompletion = %q", got)
	}
}

// TestStartStopR4StartSuccessResultReloadsServer pins the second server load
// in BuildSuccessResult, after completion validation already passed.
func TestStartStopR4StartSuccessResultReloadsServer(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		f := newStartStopR3Fixture(t)
		operation := startStopR3Ready(t, f)
		flaky := &startStopR4FlakyServers{startStopR3Servers: f.servers, failAt: failAt}
		workflow := startStopR4StartWorkflow(t, f, flaky, f.adapters, f.schemas)
		result, err := workflow.BuildSuccessResult(context.Background(), operation)
		if startStopR3ErrText(err) != "store unavailable" || result != nil {
			t.Fatalf("load %d failing: result %s, err %v", failAt, result, err)
		}
	}
}

// TestStartStopR4StopUndecodableSaveOutput pins every caller of the save
// evidence when the save output is not a JSON object.
func TestStartStopR4StopUndecodableSaveOutput(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Stopped(t, f, `{}`)
	startStopR3SetStep(&operation, stopSaveGameStep, startStopR3Output(`[]`))
	err := f.stop.CheckPreconditions(context.Background(), operation, operationStep(operation, stopRuntimeStep))
	if got := startStopR3ErrText(err); got != "graceful stop requires a confirmed game save and stopping lifecycle state" {
		t.Fatalf("CheckPreconditions = %q", got)
	}
	resolution, err := f.stop.HandlePreconditionFailure(context.Background(), operation, operationStep(operation, stopRuntimeStep), errors.New("unmet"))
	if err != nil || resolution.State != ResolutionNeedsOperator || resolution.Error != nil {
		t.Fatalf("HandlePreconditionFailure = %#v, %v", resolution, err)
	}
	if got := startStopR3ErrText(f.stop.ValidateCompletion(context.Background(), operation)); got != "game-native save was not confirmed" {
		t.Fatalf("ValidateCompletion = %q", got)
	}
	result, err := f.stop.BuildFailureResult(context.Background(), operation, store.OperationStep{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil || !decoded.Partial {
		t.Fatalf("failure result = %s, %v; want partial after a stop attempt", result, err)
	}
}

// TestStartStopR4StopSuccessResultDrainOutcome pins the proxy-drain outcome
// reported for each drain request and network profile.
func TestStartStopR4StopSuccessResultDrainOutcome(t *testing.T) {
	cases := []struct {
		name, parameters, network, want string
	}{
		{name: "default drain without network", parameters: `{}`, want: "not_configured"},
		{name: "drain disabled", parameters: `{"drain":false}`, network: "proxy", want: "not_requested"},
		{name: "drain requested with network", parameters: `{"drain":true}`, network: "proxy", want: "unsupported"},
		// A mistyped drain still allocates the pointer, so it reads as false.
		{name: "undecodable parameters", parameters: `{"drain":"yes"}`, want: "not_requested"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3Stopped(t, f, test.parameters)
			f.server().NetworkProfile = test.network
			result, err := f.stop.BuildSuccessResult(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Data struct {
					Drain struct {
						Outcome string `json:"outcome"`
					} `json:"proxy_drain"`
				} `json:"operation_data"`
			}
			if err := json.Unmarshal(result, &decoded); err != nil || decoded.Data.Drain.Outcome != test.want {
				t.Fatalf("proxy drain = %q, %v; want %q", decoded.Data.Drain.Outcome, err, test.want)
			}
		})
	}
}
