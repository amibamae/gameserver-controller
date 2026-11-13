package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests call ServerStopWorkflow.BuildPlan directly against a ready
// server and pin the reason each request or profile is rejected.

type stopProfileFunc func(context.Context, store.LogicalServer) (ServerStopProfile, error)

func (f stopProfileFunc) ResolveServerStop(ctx context.Context, server store.LogicalServer) (ServerStopProfile, error) {
	return f(ctx, server)
}

const otherStopInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"other-vm"}`

func TestServerStopPlanRules(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	version := fixture.base.schemas.ProtocolVersion()
	validProfile := ServerStopProfile{GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit)}
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	tests := []struct {
		name       string
		ctx        context.Context
		parameters string
		change     func(*store.OperationRecord, *ServerStopProfile, *error)
		want       string
	}{
		{"missing context", missing, `{}`, nil, "initialized stop workflow and context are required"},
		{"ended context", ended, `{}`, nil, context.Canceled.Error()},
		{"another operation type", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) { o.OperationType = "server.start" },
			"server.stop operation and loaded protocol version are required"},
		{"another protocol version", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) { o.ProtocolVersion = "0.0.1" },
			"server.stop operation and loaded protocol version are required"},
		{"no target server", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) { o.Target.ServerID = "" },
			"server.stop operation and loaded protocol version are required"},
		{"no target environment", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) { o.Target.EnvironmentID = "" },
			"server.stop operation and loaded protocol version are required"},
		{"snapshot of another operation", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) {
			o.RequestSnapshot.OperationType = "server.start"
		}, "server.stop operation and loaded protocol version are required"},
		{"parameters are not an object", context.Background(), `[]`, nil, "server.stop operation and loaded protocol version are required"},
		{"undecodable parameters", context.Background(), `{"force":"yes"}`, nil, "decode server.stop parameters"},
		{"drain timeout", context.Background(), `{"drain_timeout_ms":1000}`, nil, "drain_timeout_ms is unavailable until proxy drain is integrated"},
		{"force stop", context.Background(), `{"force":true}`, nil, "force stop is unavailable until exact-target approval validation is wired"},
		{"approval without force", context.Background(), `{"approval_id":"approval-1"}`, nil, "approval_id is only valid for an approved force-stop policy"},
		{"zero graceful timeout", context.Background(), `{"graceful_timeout_ms":0}`, nil, "graceful_timeout_ms must be between 1 and 3600000"},
		{"graceful timeout over an hour", context.Background(), `{"graceful_timeout_ms":3600001}`, nil, "graceful_timeout_ms must be between 1 and 3600000"},
		{"unknown server", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) { o.Target.ServerID = "server-missing" },
			"load logical server for stop plan: "},
		{"target instance changed", context.Background(), `{}`, func(o *store.OperationRecord, _ *ServerStopProfile, _ *error) {
			o.Target.InstanceRef = json.RawMessage(otherStopInstance)
		}, store.ErrLogicalServerTargetMismatch.Error()},
		{"expected another instance", context.Background(), `{"expected_instance_ref":` + otherStopInstance + `}`, nil,
			"expected_instance_ref does not match the current managed instance"},
		{"profile cannot be resolved", context.Background(), `{}`, func(_ *store.OperationRecord, _ *ServerStopProfile, e *error) { *e = errors.New("no profile") },
			"resolve trusted server stop profile: no profile"},
		{"profile without a release", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) { p.GameReleaseID = "" },
			"trusted stop profile must resolve a bounded game release"},
		{"profile with an oversized release", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) {
			p.GameReleaseID = strings.Repeat("r", 501)
		}, "trusted stop profile must resolve a bounded game release"},
		{"unit is not an object", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) { p.UnitRef = json.RawMessage(`"unit"`) },
			"trusted stop profile unit reference does not bind"},
		{"unit of another execution adapter", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) {
			p.UnitRef = json.RawMessage(strings.Replace(startTestUnit, "execution.fake", "execution.other", 1))
		}, "trusted stop profile unit reference does not bind"},
		{"unit in another environment", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) {
			p.UnitRef = json.RawMessage(strings.Replace(startTestUnit, `"staging"`, `"production"`, 1))
		}, "trusted stop profile unit reference does not bind"},
		{"unit of another resource kind", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) {
			p.UnitRef = json.RawMessage(strings.Replace(startTestUnit, "systemd-unit", "container", 1))
		}, "trusted stop profile unit reference does not bind"},
		{"unit without an external ID", context.Background(), `{}`, func(_ *store.OperationRecord, p *ServerStopProfile, _ *error) {
			p.UnitRef = json.RawMessage(strings.Replace(startTestUnit, "paper-start-test", "", 1))
		}, "trusted stop profile unit reference does not bind"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := stopOperationRecord(version, json.RawMessage(test.parameters))
			profile, profileErr := validProfile, error(nil)
			if test.change != nil {
				test.change(&operation, &profile, &profileErr)
			}
			workflow, err := NewServerStopWorkflow(fixture.base.store, fixture.base.registry, fixture.base.schemas,
				stopProfileFunc(func(context.Context, store.LogicalServer) (ServerStopProfile, error) { return profile, profileErr }))
			if err != nil {
				t.Fatal(err)
			}
			steps, err := workflow.BuildPlan(test.ctx, operation)
			if err == nil || steps != nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildPlan = %d steps, %v; want an error containing %q", len(steps), err, test.want)
			}
		})
	}
	if _, err := (*ServerStopWorkflow)(nil).BuildPlan(context.Background(), stopOperationRecord(version, json.RawMessage(`{}`))); err == nil ||
		err.Error() != "initialized stop workflow and context are required" {
		t.Fatalf("nil workflow BuildPlan = %v", err)
	}
}

func TestServerStopPlanBindsTheExactTargets(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	version := fixture.base.schemas.ProtocolVersion()
	tests := []struct {
		name         string
		parameters   string
		withoutRef   bool
		graceSeconds float64
	}{
		{"default grace period", `{}`, false, 120},
		{"grace period rounded up to whole seconds", `{"graceful_timeout_ms":1500}`, false, 2},
		{"one millisecond", `{"graceful_timeout_ms":1}`, false, 1},
		{"an hour", `{"graceful_timeout_ms":3600000}`, false, 3600},
		{"expected instance and explicit drain", `{"drain":true,"expected_instance_ref":` + startTestInstance + `}`, false, 120},
		{"no instance in the target", `{"drain":false}`, true, 120},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := stopOperationRecord(version, json.RawMessage(test.parameters))
			if test.withoutRef {
				operation.Target.InstanceRef = nil
			}
			steps, err := fixture.workflow.BuildPlan(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			want := []struct{ id, capability, adapterID string }{
				{stopInspectInstanceStep, "hosting.instance.inspect", "hosting.fake"},
				{stopInspectUnitStep, "execution.unit.inspect", "execution.fake"},
				{stopSaveGameStep, "game.save.request", "game.paper"},
				{stopRuntimeStep, "execution.unit.graceful_stop", "execution.fake"},
			}
			if len(steps) != len(want) {
				t.Fatalf("planned %d steps, want %d", len(steps), len(want))
			}
			for i, step := range steps {
				if step.StepID != want[i].id || step.StepType != want[i].capability || step.CapabilityName != want[i].capability ||
					step.AdapterID != want[i].adapterID || step.Status != protocolstate.StepPending || step.CapabilityVersion == "" {
					t.Fatalf("step %d = %+v, want %+v", i, step, want[i])
				}
			}
			var inspect, save, stop map[string]any
			for _, decode := range []struct {
				raw    json.RawMessage
				target *map[string]any
			}{{steps[0].Input, &inspect}, {steps[2].Input, &save}, {steps[3].Input, &stop}} {
				if err := json.Unmarshal(decode.raw, decode.target); err != nil {
					t.Fatal(err)
				}
			}
			if !stepBindsRef(steps[0].Input, "instance_ref", json.RawMessage(startTestInstance)) ||
				!stepBindsRef(steps[1].Input, "unit_ref", json.RawMessage(startTestUnit)) ||
				!stepBindsRef(steps[3].Input, "unit_ref", json.RawMessage(startTestUnit)) {
				t.Fatalf("plan is not bound to the exact instance and unit: %s / %s / %s", steps[0].Input, steps[1].Input, steps[3].Input)
			}
			target, _ := save["target"].(map[string]any)
			if save["release_id"] != startTestRelease || save["reason"] != "shutdown" || target["server_id"] != startTestServerID ||
				target["environment_id"] != startTestEnvID || !stepBindsRef(mustMarshal(t, target), "instance_ref", json.RawMessage(startTestInstance)) {
				t.Fatalf("save input = %s", steps[2].Input)
			}
			if stop["grace_period_seconds"] != test.graceSeconds {
				t.Fatalf("grace period = %v, want %v", stop["grace_period_seconds"], test.graceSeconds)
			}
		})
	}
}

func TestServerStopPlanRequiresAReadyServer(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	stopped, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil || stopped.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("seed stop = %s, %v", stopped.Record.Status, err)
	}
	operation := stopOperationRecord(fixture.base.schemas.ProtocolVersion(), json.RawMessage(`{}`))
	operation.OperationID = "stop-operation-2"
	if _, err := fixture.workflow.BuildPlan(context.Background(), operation); err == nil ||
		err.Error() != "server.stop requires a ready logical server with a managed instance" {
		t.Fatalf("BuildPlan for a stopped server = %v", err)
	}
}
