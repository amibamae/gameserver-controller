package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests cover the branches of the shared runtime step builders that the
// server.start and server.stop plans cannot reach.

func startStopR4Row(input any) plannedRuntimeStep {
	return plannedRuntimeStep{id: "inspect-instance", adapterID: "hosting.fake", category: adapter.CategoryHosting,
		capability: "hosting.instance.inspect", schemaStem: "hosting:instance-inspect", input: input}
}

func TestStartStopR4EncodePlannedSteps(t *testing.T) {
	f := newStartStopR3Fixture(t)
	rows := []plannedRuntimeStep{startStopR4Row(map[string]any{"instance_ref": f.server().InstanceRef})}
	capabilities, err := resolvePlannedCapabilities(f.adapters, f.version(), rows)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := encodePlannedSteps(f.schemas, rows, capabilities)
	if err != nil || len(steps) != 1 {
		t.Fatalf("encodePlannedSteps = %v, %v", steps, err)
	}
	want := store.OperationStep{
		StepID: "inspect-instance", StepType: "hosting.instance.inspect", Status: protocolstate.StepPending,
		AdapterID: "hosting.fake", CapabilityName: "hosting.instance.inspect", CapabilityVersion: "1",
		Input: []byte(`{"instance_ref":` + startTestInstance + `}`),
	}
	if string(startStopR3MustJSON(t, steps[0])) != string(startStopR3MustJSON(t, want)) {
		t.Fatalf("step = %+v; want %+v", steps[0], want)
	}

	rows[0].input = map[string]any{"instance_ref": func() {}}
	if steps, err := encodePlannedSteps(f.schemas, rows, capabilities); steps != nil || startStopR3ErrText(err) != "encode inspect-instance input: json: unsupported type: func()" {
		t.Fatalf("unencodable input = %v, %v", steps, err)
	}
}

func TestStartStopR4CheckPlanRequest(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3Operation("server.start", f.version(), `{}`)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name    string
		ctx     context.Context
		schemas schemaRuntime
		change  func(*store.OperationRecord)
		want    string
	}{
		{name: "accepted", ctx: context.Background(), schemas: f.schemas},
		{name: "nil context", schemas: f.schemas, want: startPlanRule.initError},
		{name: "missing dependencies", ctx: context.Background(), want: startPlanRule.initError},
		{name: "cancelled", ctx: cancelled, schemas: f.schemas, want: context.Canceled.Error()},
		{name: "other operation", ctx: context.Background(), schemas: f.schemas,
			change: func(o *store.OperationRecord) { o.OperationType = "server.stop" }, want: startPlanRule.operationError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := operation
			if test.change != nil {
				test.change(&candidate)
			}
			if got := startStopR3ErrText(checkPlanRequest(test.ctx, test.schemas, candidate, startPlanRule)); got != test.want {
				t.Fatalf("checkPlanRequest = %q; want %q", got, test.want)
			}
		})
	}
}

func TestStartStopR4ResolveTrustedProfile(t *testing.T) {
	server := store.LogicalServer{ServerID: "s"}
	resolveErr := func(context.Context, store.LogicalServer) (string, error) { return "", errors.New("down") }
	resolveOK := func(context.Context, store.LogicalServer) (string, error) { return "profile", nil }
	valid := func(store.LogicalServer, string) error { return nil }
	invalid := func(store.LogicalServer, string) error { return errors.New("invalid profile") }
	if profile, err := resolveTrustedProfile(context.Background(), server, resolveErr, valid, "start"); profile != "" || startStopR3ErrText(err) != "resolve trusted server start profile: down" {
		t.Fatalf("resolver error = %q, %v", profile, err)
	}
	if profile, err := resolveTrustedProfile(context.Background(), server, resolveOK, invalid, "stop"); profile != "" || startStopR3ErrText(err) != "invalid profile" {
		t.Fatalf("invalid profile = %q, %v", profile, err)
	}
	if profile, err := resolveTrustedProfile(context.Background(), server, resolveOK, valid, "stop"); profile != "profile" || err != nil {
		t.Fatalf("valid profile = %q, %v", profile, err)
	}
}
