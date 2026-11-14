package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// startStopR3Planner plans one operation for a fixture.
type startStopR3Planner func(*startStopR3Fixture) func(context.Context, store.OperationRecord) ([]store.OperationStep, error)

func startStopR3StartPlanner(f *startStopR3Fixture) func(context.Context, store.OperationRecord) ([]store.OperationStep, error) {
	return f.start.BuildPlan
}

func startStopR3StopPlanner(f *startStopR3Fixture) func(context.Context, store.OperationRecord) ([]store.OperationStep, error) {
	return f.stop.BuildPlan
}

// startStopR3PlanTarget names the workflow under test and the lifecycle state
// its plan starts from.
type startStopR3PlanTarget struct {
	operationType string
	state         protocolstate.ServerState
	planner       startStopR3Planner
}

var (
	startStopR3StartTarget = startStopR3PlanTarget{"server.start", protocolstate.ServerStopped, startStopR3StartPlanner}
	startStopR3StopTarget  = startStopR3PlanTarget{"server.stop", protocolstate.ServerReady, startStopR3StopPlanner}
)

func startStopR3Plan(t *testing.T, target startStopR3PlanTarget, test startStopR3PlanCase) ([]store.OperationStep, error) {
	t.Helper()
	f := newStartStopR3Fixture(t)
	f.server().LifecycleState = target.state
	params := test.params
	if params == "" {
		params = `{}`
	}
	operation := startStopR3Operation(target.operationType, f.version(), params)
	if test.change != nil {
		test.change(f, &operation)
	}
	return target.planner(f)(context.Background(), operation)
}

func startStopR3PlanAll(t *testing.T, target startStopR3PlanTarget, cases []startStopR3PlanCase) map[string][]store.OperationStep {
	t.Helper()
	plans := map[string][]store.OperationStep{}
	for _, test := range cases {
		steps, err := startStopR3Plan(t, target, test)
		if err != nil {
			t.Fatalf("%s %s: %v", target.operationType, test.name, err)
		}
		plans[test.name] = steps
	}
	return plans
}

func startStopR3CompareGolden(t *testing.T, value any, name string) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if *updateStartStopR3Golden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("planned steps differ from %s; rerun with -update-start-stop-r3-golden only for an intended change", path)
	}
}

// TestStartStopR3BuildPlanSnapshot pins every planned step (ID, adapter,
// capability, version and exact input bytes) for each accepted starting state.
func TestStartStopR3BuildPlanSnapshot(t *testing.T) {
	startCases := []startStopR3PlanCase{
		{name: "stopped untargeted", params: `{}`},
		{name: "stopped with pinned target instance", params: `{}`, change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
			o.Target.InstanceRef = json.RawMessage(startTestInstance)
		}},
		{name: "stopped with matching expected instance", params: `{"expected_instance_ref":` + startTestInstance + `}`},
		{name: "stopped with network profile", params: `{}`, change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.server().NetworkProfile = "proxy" }},
	}
	stopCases := []startStopR3PlanCase{
		{name: "ready default grace", params: `{}`},
		{name: "ready pinned target instance", params: `{}`, change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
			o.Target.InstanceRef = json.RawMessage(startTestInstance)
		}},
		{name: "ready grace rounded up", params: `{"graceful_timeout_ms":1500}`},
		{name: "ready grace one millisecond", params: `{"graceful_timeout_ms":1}`},
		{name: "ready grace one hour", params: `{"graceful_timeout_ms":3600000}`},
		{name: "ready explicit drain without network", params: `{"drain":true}`},
		{name: "ready network profile without drain", params: `{"drain":false}`, change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.server().NetworkProfile = "proxy" }},
		{name: "ready matching expected instance", params: `{"expected_instance_ref":` + startTestInstance + `}`},
	}
	startStopR3CompareGolden(t, map[string]map[string][]store.OperationStep{
		"server.start": startStopR3PlanAll(t, startStopR3StartTarget, startCases),
		"server.stop":  startStopR3PlanAll(t, startStopR3StopTarget, stopCases),
	}, "server_start_stop_plans.golden.json")
}

type startStopR3PlanCase struct {
	name   string
	params string
	change func(*startStopR3Fixture, *store.OperationRecord)
	want   string
}

func startStopR3RunPlanRefusals(t *testing.T, target startStopR3PlanTarget, cases []startStopR3PlanCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			steps, err := startStopR3Plan(t, target, test)
			if got := startStopR3ErrText(err); got != test.want || steps != nil {
				t.Fatalf("BuildPlan = %d steps, %q; want %q", len(steps), got, test.want)
			}
		})
	}
}

// startStopR3CheckPlanContext pins the nil and cancelled context refusals.
func startStopR3CheckPlanContext(t *testing.T, target startStopR3PlanTarget, initErr string) {
	t.Helper()
	f := newStartStopR3Fixture(t)
	f.server().LifecycleState = target.state
	operation := startStopR3Operation(target.operationType, f.version(), `{}`)
	var missing context.Context
	if _, err := target.planner(f)(missing, operation); startStopR3ErrText(err) != initErr {
		t.Fatalf("nil context = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := target.planner(f)(cancelled, operation); !errors.Is(err, context.Canceled) || err.Error() != context.Canceled.Error() {
		t.Fatalf("cancelled context = %v", err)
	}
}

// TestStartStopR3StartBuildPlanRefusals pins the exact error for every
// server.start planning rule, in the order the rules are checked.
func TestStartStopR3StartBuildPlanRefusals(t *testing.T) {
	const operationRequired = "server.start operation and loaded protocol version are required"
	const stateRequired = "server.start requires a stopped logical server with a managed instance"
	const selection = "trusted start profile must select a release and bounded readiness check set"
	const unitBinding = "trusted start profile unit reference does not bind the configured execution adapter and environment"
	const declaration = "trusted readiness profile does not match an exact game-adapter release, environment, set, and check-version declaration"
	declared := []any{
		map[string]any{"check_id": "paper.startup-complete", "check_version": "1"},
		map[string]any{"check_id": "paper.world-loaded", "check_version": "2"},
	}
	manyChecks := make([]ReadinessRequirement, 65)
	for i := range manyChecks {
		manyChecks[i] = ReadinessRequirement{CheckID: fmt.Sprintf("check-%d", i), CheckVersion: "1"}
	}
	cases := []startStopR3PlanCase{
		{name: "wrong operation type", change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.stop" }), want: operationRequired},
		{name: "wrong protocol", change: startStopR3Op(func(o *store.OperationRecord) { o.ProtocolVersion = "0.0.1" }), want: operationRequired},
		{name: "no target server", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "" }), want: operationRequired},
		{name: "no target environment", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.EnvironmentID = "" }), want: operationRequired},
		{name: "snapshot of another operation", change: startStopR3Op(func(o *store.OperationRecord) { o.RequestSnapshot.OperationType = "server.stop" }), want: operationRequired},
		{name: "parameters are an array", params: `[]`, want: operationRequired},
		{name: "parameters are null", params: `null`, want: operationRequired},
		{name: "unknown server", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "missing" }), want: "load logical server for start plan: operation not found"},
		{name: "ready server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerReady }), want: stateRequired},
		{name: "starting server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerStarting }), want: stateRequired},
		{name: "degraded server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerDegraded }), want: stateRequired},
		{name: "failed server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerFailed }), want: stateRequired},
		{name: "stopped server without instance", change: startStopR3Server(func(s *store.LogicalServer) { s.InstanceRef = nil }), want: stateRequired},
		{name: "target instance mismatch", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }),
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "expected instance mismatch", params: `{"expected_instance_ref":` + startStopR3OtherInstance + `}`,
			want: "expected_instance_ref does not match the current managed instance"},
		{name: "profile not resolved", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.profiles.startErr = errors.New("no profile") },
			want: "resolve trusted server start profile: no profile"},
		{name: "profile without release", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.GameReleaseID = "" }), want: selection},
		{name: "profile with oversized release", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.GameReleaseID = strings.Repeat("r", 201) }), want: selection},
		{name: "profile without check set", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.ReadinessCheckSetID = "" }), want: selection},
		{name: "profile with oversized check set", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.ReadinessCheckSetID = strings.Repeat("s", 201) }), want: selection},
		{name: "profile without checks", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.RequiredChecks = nil }), want: selection},
		{name: "profile with too many checks", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.RequiredChecks = manyChecks }), want: selection},
		{name: "unit is invalid JSON", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = json.RawMessage(`{`) }), want: unitBinding},
		{name: "unit is a string", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = json.RawMessage(`"unit"`) }), want: unitBinding},
		{name: "unit is null", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = json.RawMessage(`null`) }), want: unitBinding},
		{name: "unit of another adapter", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = startStopR3UnitWith("execution.fake", "execution.other") }), want: unitBinding},
		{name: "unit in another environment", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = startStopR3UnitWith(`"staging"`, `"production"`) }), want: unitBinding},
		{name: "unit without resource kind", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = startStopR3UnitWith("systemd-unit", "") }), want: unitBinding},
		{name: "unit without external ID", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.UnitRef = startStopR3UnitWith("paper-start-test", "") }), want: unitBinding},
		{name: "check without ID", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.RequiredChecks[1].CheckID = "" }),
			want: "trusted readiness checks require non-empty IDs and versions"},
		{name: "check without version", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.RequiredChecks[0].CheckVersion = "" }),
			want: "trusted readiness checks require non-empty IDs and versions"},
		{name: "repeated check", change: startStopR3StartProfile(func(p *ServerStartProfile) { p.RequiredChecks[1].CheckID = p.RequiredChecks[0].CheckID }),
			want: `trusted readiness profile repeats check "paper.startup-complete"`},
		{name: "hosting adapter missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { delete(f.adapters.descriptors, "hosting.fake") },
			want: `resolve adapter "hosting.fake" for hosting.instance.inspect: adapter is not registered`},
		{name: "instance names no adapter", change: startStopR3Server(func(s *store.LogicalServer) { s.InstanceRef = json.RawMessage(`{"external_id":"vm"}`) }),
			want: `resolve adapter "" for hosting.instance.inspect: adapter is not registered`},
		{name: "hosting descriptor identity differs", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			descriptor := f.adapters.descriptors["hosting.fake"]
			descriptor.AdapterID = "hosting.other"
			f.adapters.descriptors["hosting.fake"] = descriptor
		}, want: `adapter "hosting.fake" has the wrong identity or category for hosting.instance.inspect`},
		{name: "execution adapter has the wrong category", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.server().ExecutionAdapterID = "hosting.fake"
			f.profiles.start.UnitRef = startStopR3UnitWith("execution.fake", "hosting.fake")
		}, want: `adapter "hosting.fake" has the wrong identity or category for execution.unit.start`},
		{name: "start capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.start")
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.start`},
		{name: "inspect capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.inspect")
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.inspect`},
		{name: "start capability not mutating", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "execution.fake", "execution.unit.start", func(c *adapter.Capability) { c.Mutating = false })
		}, want: `adapter "execution.fake" declares an incompatible execution.unit.start capability`},
		{name: "inspect capability without version", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "execution.fake", "execution.unit.inspect", func(c *adapter.Capability) { c.Version = "" })
		}, want: `adapter "execution.fake" declares an incompatible execution.unit.inspect capability`},
		{name: "inspect capability with another output schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "hosting.fake", "hosting.instance.inspect", func(c *adapter.Capability) { c.OutputSchemaURI += "-other" })
		}, want: `adapter "hosting.fake" declares an incompatible hosting.instance.inspect capability`},
		{name: "readiness capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "game.paper", "game.readiness.check")
		}, want: `adapter "game.paper" does not advertise required capability game.readiness.check`},
		{name: "readiness capability with another input schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "game.paper", "game.readiness.check", func(c *adapter.Capability) { c.InputSchemaURI += "-other" })
		}, want: `adapter "game.paper" declares an incompatible game.readiness.check capability`},
		{name: "game adapter missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { delete(f.adapters.descriptors, "game.paper") },
			want: `resolve adapter "game.paper" for game.readiness.check: adapter is not registered`},
		{name: "readiness constraints cannot be encoded", change: startStopR3Readiness(map[string]any{"readiness_check_sets": func() {}}),
			want: "json: unsupported type: func()"},
		{name: "readiness constraints cannot be decoded", change: startStopR3Readiness(map[string]any{"readiness_check_sets": "sets"}),
			want: "decode game adapter readiness declarations: json: cannot unmarshal string into Go struct field .readiness_check_sets of type []struct { GameReleaseID string \"json:\\\"game_release_id\\\"\"; EnvironmentIDs []string \"json:\\\"environment_ids\\\"\"; ReadinessCheckSetID string \"json:\\\"readiness_check_set_id\\\"\"; RequiredChecks []workflow.ReadinessRequirement \"json:\\\"required_checks\\\"\" }"},
		{name: "no readiness declarations", change: startStopR3Readiness(nil), want: declaration},
		{name: "declaration for another release", change: startStopR3Readiness(startStopR3ReadinessSet("other", []string{startTestEnvID}, startTestSetID, declared)), want: declaration},
		{name: "declaration for another environment", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{"production"}, startTestSetID, declared)), want: declaration},
		{name: "declaration for another set", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{startTestEnvID}, "other", declared)), want: declaration},
		{name: "declaration with other check versions", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{startTestEnvID}, startTestSetID, []any{
			map[string]any{"check_id": "paper.startup-complete", "check_version": "1"}, map[string]any{"check_id": "paper.world-loaded", "check_version": "3"},
		})), want: declaration},
		{name: "declaration with fewer checks", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{startTestEnvID}, startTestSetID, declared[:1])), want: declaration},
		{name: "declaration with repeated checks", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{startTestEnvID}, startTestSetID, []any{declared[0], declared[0]})), want: declaration},
		{name: "declaration with an empty check version", change: startStopR3Readiness(startStopR3ReadinessSet(startTestRelease, []string{startTestEnvID}, startTestSetID, []any{
			declared[0], map[string]any{"check_id": "paper.world-loaded", "check_version": ""},
		})), want: declaration},
		{name: "inspect input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":hosting:instance-inspect-input"
		},
			want: "planned inspect-instance input does not match its declared capability schema: rejected by test"},
		{name: "start input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":execution:systemd-unit-start-input"
		},
			want: "planned start-runtime input does not match its declared capability schema: rejected by test"},
		{name: "verify input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":execution:systemd-unit-inspect-input"
		},
			want: "planned verify-runtime input does not match its declared capability schema: rejected by test"},
		{name: "readiness input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":game:readiness-check-input"
		},
			want: "planned check-readiness input does not match its declared capability schema: rejected by test"},
	}
	startStopR3RunPlanRefusals(t, startStopR3StartTarget, cases)
	startStopR3CheckPlanContext(t, startStopR3StartTarget, "initialized start workflow and context are required")
	f := newStartStopR3Fixture(t)
	operation := startStopR3Operation("server.start", f.version(), `{}`)
	if _, err := (*ServerStartWorkflow)(nil).BuildPlan(context.Background(), operation); startStopR3ErrText(err) != "initialized start workflow and context are required" {
		t.Fatalf("nil workflow = %v", err)
	}
}

// TestStartStopR3StopBuildPlanRefusals pins the exact error for every
// server.stop planning rule, in the order the rules are checked.
func TestStartStopR3StopBuildPlanRefusals(t *testing.T) {
	const operationRequired = "server.stop operation and loaded protocol version are required"
	const stateRequired = "server.stop requires a ready logical server with a managed instance"
	const unitBinding = "trusted stop profile unit reference does not bind the configured execution adapter and environment"
	const grace = "graceful_timeout_ms must be between 1 and 3600000 for the systemd stop adapter"
	const drain = "configured proxy drain requires a trusted proxy profile and adapter integration"
	cases := []startStopR3PlanCase{
		{name: "wrong operation type", change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.start" }), want: operationRequired},
		{name: "wrong protocol", change: startStopR3Op(func(o *store.OperationRecord) { o.ProtocolVersion = "0.0.1" }), want: operationRequired},
		{name: "no target server", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "" }), want: operationRequired},
		{name: "no target environment", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.EnvironmentID = "" }), want: operationRequired},
		{name: "snapshot of another operation", change: startStopR3Op(func(o *store.OperationRecord) { o.RequestSnapshot.OperationType = "server.start" }), want: operationRequired},
		{name: "parameters are an array", params: `[]`, want: operationRequired},
		{name: "undecodable parameters", params: `{"force":"yes"}`, want: "decode server.stop parameters"},
		{name: "drain timeout", params: `{"drain_timeout_ms":0}`, want: "drain_timeout_ms is unavailable until proxy drain is integrated"},
		{name: "drain timeout before force", params: `{"drain_timeout_ms":5,"force":true}`, want: "drain_timeout_ms is unavailable until proxy drain is integrated"},
		{name: "force", params: `{"force":true,"approval_id":"a"}`, want: "force stop is unavailable until exact-target approval validation is wired"},
		{name: "approval without force", params: `{"approval_id":"a","graceful_timeout_ms":0}`, want: "approval_id is only valid for an approved force-stop policy"},
		{name: "zero grace", params: `{"graceful_timeout_ms":0}`, want: grace},
		{name: "negative grace", params: `{"graceful_timeout_ms":-1}`, want: grace},
		{name: "grace over an hour", params: `{"graceful_timeout_ms":3600001}`, want: grace},
		{name: "grace checked before server", params: `{"graceful_timeout_ms":0}`, change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "missing" }), want: grace},
		{name: "unknown server", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "missing" }), want: "load logical server for stop plan: operation not found"},
		{name: "stopped server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerStopped }), want: stateRequired},
		{name: "degraded server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerDegraded }), want: stateRequired},
		{name: "stopping server", change: startStopR3Server(func(s *store.LogicalServer) { s.LifecycleState = protocolstate.ServerStopping }), want: stateRequired},
		{name: "ready server without instance", change: startStopR3Server(func(s *store.LogicalServer) { s.InstanceRef = nil }), want: stateRequired},
		{name: "target instance mismatch", change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }),
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "expected instance mismatch", params: `{"expected_instance_ref":` + startStopR3OtherInstance + `}`,
			want: "expected_instance_ref does not match the current managed instance"},
		{name: "network profile with default drain", change: startStopR3Server(func(s *store.LogicalServer) { s.NetworkProfile = "proxy" }), want: drain},
		{name: "network profile with explicit drain", params: `{"drain":true}`, change: startStopR3Server(func(s *store.LogicalServer) { s.NetworkProfile = "proxy" }), want: drain},
		{name: "network profile checked after expected instance", params: `{"expected_instance_ref":` + startStopR3OtherInstance + `}`,
			change: startStopR3Server(func(s *store.LogicalServer) { s.NetworkProfile = "proxy" }), want: "expected_instance_ref does not match the current managed instance"},
		{name: "profile not resolved", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.profiles.stopErr = errors.New("no profile") },
			want: "resolve trusted server stop profile: no profile"},
		{name: "profile without release", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.GameReleaseID = "" }), want: "trusted stop profile must resolve a bounded game release"},
		{name: "profile with oversized release", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.GameReleaseID = strings.Repeat("r", 501) }), want: "trusted stop profile must resolve a bounded game release"},
		{name: "unit is invalid JSON", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = json.RawMessage(`{`) }), want: unitBinding},
		{name: "unit is null", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = json.RawMessage(`null`) }), want: unitBinding},
		{name: "unit of another adapter", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = startStopR3UnitWith("execution.fake", "execution.other") }), want: unitBinding},
		{name: "unit in another environment", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = startStopR3UnitWith(`"staging"`, `"production"`) }), want: unitBinding},
		{name: "unit of another kind", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = startStopR3UnitWith("systemd-unit", "container") }), want: unitBinding},
		{name: "unit without external ID", change: startStopR3StopProfile(func(p *ServerStopProfile) { p.UnitRef = startStopR3UnitWith("paper-start-test", "") }), want: unitBinding},
		{name: "hosting adapter missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { delete(f.adapters.descriptors, "hosting.fake") },
			want: `resolve adapter "hosting.fake" for hosting.instance.inspect: adapter is not registered`},
		{name: "inspect capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.inspect")
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.inspect`},
		{name: "save capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "game.paper", "game.save.request")
		}, want: `adapter "game.paper" does not advertise required capability game.save.request`},
		{name: "game adapter has the wrong category", change: startStopR3Server(func(s *store.LogicalServer) { s.GameAdapterID = "hosting.fake" }),
			want: `adapter "hosting.fake" has the wrong identity or category for game.save.request`},
		{name: "graceful stop capability not mutating", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "execution.fake", "execution.unit.graceful_stop", func(c *adapter.Capability) { c.Mutating = false })
		}, want: `adapter "execution.fake" declares an incompatible execution.unit.graceful_stop capability`},
		{name: "graceful stop capability missing", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.graceful_stop")
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.graceful_stop`},
		{name: "inspect input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":hosting:instance-inspect-input"
		},
			want: "planned inspect-instance input does not match its declared capability schema: rejected by test"},
		{name: "unit inspect input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":execution:systemd-unit-inspect-input"
		},
			want: "planned inspect-runtime input does not match its declared capability schema: rejected by test"},
		{name: "save input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) { f.schemas.reject = ":game:save-request-input" },
			want: "planned save-game-data input does not match its declared capability schema: rejected by test"},
		{name: "stop input rejected by schema", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			f.schemas.reject = ":execution:systemd-unit-graceful-stop-input"
		},
			want: "planned stop-runtime input does not match its declared capability schema: rejected by test"},
	}
	startStopR3RunPlanRefusals(t, startStopR3StopTarget, cases)
	startStopR3CheckPlanContext(t, startStopR3StopTarget, "initialized stop workflow and context are required")
}
