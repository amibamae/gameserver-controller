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

const provisionR3PriorRef = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"provider-operation","external_id":"op-r3"}`

// provisionR3Resolution is a Resolution in a form test tables can compare.
type provisionR3Resolution struct {
	State  ResolutionState
	Output string
	Ref    string
	Error  string
}

func provisionR3Resolve(resolution Resolution, err error) provisionR3Resolution {
	got := provisionR3Resolution{State: resolution.State, Output: string(resolution.Output), Ref: string(resolution.ExternalOperationRef)}
	var protocol struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(resolution.Error, &protocol) == nil {
		got.Error = protocol.Code
	}
	if err != nil {
		got.Error = "ERR:" + err.Error()
	}
	return got
}

// provisionR3Reply scripts one adapter reply.
func provisionR3Reply(t *testing.T, fake *fakeadapter.Scripted, capability string, reply fakeadapter.Reply) {
	t.Helper()
	if reply.ObservedAt.IsZero() {
		reply.ObservedAt = time.Now().UTC()
	}
	if err := fake.SetScript(capability, reply); err != nil {
		t.Fatal(err)
	}
}

func provisionR3Completed1(outcome adapter.Outcome, output json.RawMessage) fakeadapter.Reply {
	return fakeadapter.Reply{Outcome: outcome, Output: output}
}

// TestServerProvisionR3ReconcileCreate pins every reconcileCreate outcome.
func TestServerProvisionR3ReconcileCreate(t *testing.T) {
	needs := provisionR3Resolution{State: ResolutionNeedsOperator}
	needsRef := provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3PriorRef}
	observed := "2026-11-08T12:00:00Z"
	lookup := func(change func(map[string]any)) func(*testing.T, *provisionPlanFixture, string) fakeadapter.Reply {
		return func(t *testing.T, _ *provisionPlanFixture, key string) fakeadapter.Reply {
			value := map[string]any{"resource_key": key, "inventory_complete": true, "observed_at": observed,
				"matches": []any{map[string]any{"instance_ref": json.RawMessage(provisionR3InstanceRef), "state": "active"}}}
			if change != nil {
				change(value)
			}
			return provisionR3Completed1(adapter.OutcomeCompleted, mustMarshal(t, value))
		}
	}
	matchField := func(field string, value any) func(map[string]any) {
		return func(m map[string]any) { m["matches"].([]any)[0].(map[string]any)[field] = value }
	}
	cases := []struct {
		name  string
		step  func(*store.OperationStep)
		reply func(*testing.T, *provisionPlanFixture, string) fakeadapter.Reply
		want  provisionR3Resolution
	}{
		{name: "wrong capability", step: func(s *store.OperationStep) { s.CapabilityName = "hosting.instance.wait" }, want: needs},
		{name: "no adapter", step: func(s *store.OperationStep) { s.AdapterID = "" }, want: needs},
		{name: "input undecodable", step: func(s *store.OperationStep) { s.Input = json.RawMessage(`[`) }, want: needs},
		{name: "foreign resource key", step: func(s *store.OperationStep) { s.Input = json.RawMessage(`{"resource_key":"gsc-other"}`) }, want: needs},
		{name: "adapter cannot look up", step: func(s *store.OperationStep) { s.AdapterID = "execution.fake" }, want: needs},
		{name: "lookup fails", reply: func(*testing.T, *provisionPlanFixture, string) fakeadapter.Reply {
			return fakeadapter.Reply{Err: errors.New("provider offline")}
		}, want: needsRef},
		{name: "lookup not completed", reply: func(t *testing.T, f *provisionPlanFixture, key string) fakeadapter.Reply {
			reply := lookup(nil)(t, f, key)
			reply.Outcome = adapter.OutcomeAccepted
			return reply
		}, want: needsRef},
		{name: "lookup schema invalid", reply: lookup(func(m map[string]any) { delete(m, "matches") }), want: needsRef},
		{name: "lookup other key", reply: lookup(func(m map[string]any) { m["resource_key"] = "gsc-" + provisionR3Hex64 }), want: needsRef},
		{name: "lookup incomplete", reply: lookup(func(m map[string]any) { m["inventory_complete"] = false }), want: needsRef},
		{name: "no match", reply: lookup(func(m map[string]any) { m["matches"] = []any{} }), want: needsRef},
		{name: "match other adapter", reply: lookup(matchField("instance_ref", map[string]any{"adapter_id": "hosting.other", "environment_id": "staging", "resource_kind": "instance", "external_id": "vm-r3"})), want: needsRef},
		{name: "match other environment", reply: lookup(matchField("instance_ref", map[string]any{"adapter_id": "hosting.fake", "environment_id": "production", "resource_kind": "instance", "external_id": "vm-r3"})), want: needsRef},
		{name: "match other kind", reply: lookup(matchField("instance_ref", map[string]any{"adapter_id": "hosting.fake", "environment_id": "staging", "resource_kind": "volume", "external_id": "vm-r3"})), want: needsRef},
		{name: "match destroyed", reply: lookup(matchField("state", "destroyed")), want: needsRef},
		{name: "match observed badly", reply: lookup(func(m map[string]any) { m["observed_at"] = "yesterday" }), want: needsRef},
		{name: "match creating", reply: lookup(matchField("state", "creating")), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3PriorRef,
			Output: `{"instance_ref":` + provisionR3InstanceRef + `,"state":"creating","observed_at":"` + observed + `"}`}},
		{name: "match stopped", reply: lookup(matchField("state", "stopped")), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3PriorRef,
			Output: `{"instance_ref":` + provisionR3InstanceRef + `,"state":"stopped","observed_at":"` + observed + `"}`}},
		{name: "match active", reply: lookup(nil), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3PriorRef,
			Output: `{"instance_ref":` + provisionR3InstanceRef + `,"state":"active","observed_at":"` + observed + `"}`}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newProvisionPlanFixture(t)
			steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
			if err != nil {
				t.Fatal(err)
			}
			step := steps[0]
			step.ExternalOperationRef = json.RawMessage(provisionR3PriorRef)
			if test.step != nil {
				test.step(&step)
			}
			if test.reply != nil {
				provisionR3Reply(t, f.hosting, "hosting.instance.find_by_resource_key", test.reply(t, &f, provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)))
			}
			got := provisionR3Resolve(f.workflow.Reconcile(context.Background(), f.operation, step))
			if got != test.want {
				t.Fatalf("Reconcile = %#v, want %#v", got, test.want)
			}
		})
	}
}

const provisionR3Hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestServerProvisionR3ReconcileBootstrap pins every reconcileBootstrap outcome.
func TestServerProvisionR3ReconcileBootstrap(t *testing.T) {
	needs := provisionR3Resolution{State: ResolutionNeedsOperator}
	needsRef := provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3PriorRef}
	inspection := func(change func(map[string]any)) func(*testing.T, provisionR3Completed) fakeadapter.Reply {
		return provisionR3InspectionOf(provisionBootstrapStep, change)
	}
	running := func(m map[string]any) {
		m["state"] = "running"
		delete(m, "completed_at")
		delete(m, "exit_code")
		delete(m, "validation_evidence")
	}
	cases := []provisionR3InspectCase{
		{name: "wrong capability", step: func(s *store.OperationStep) { s.CapabilityName = "hosting.bootstrap.inspect" }, want: needsRef},
		{name: "no created instance", change: provisionR3Step(provisionCreateStep, func(s *store.OperationStep) { s.Status = protocolstate.StepPending }), want: needs},
		{name: "output undecodable", step: func(s *store.OperationStep) { s.Output = json.RawMessage(`[`) }, want: needsRef},
		{name: "no execution reference", step: func(s *store.OperationStep) { s.Output = nil; s.ExternalOperationRef = nil }, want: needs},
		{name: "prior reference is not an execution", step: func(s *store.OperationStep) { s.Output = nil }, want: needsRef},
		{name: "execution of another adapter", step: func(s *store.OperationStep) { s.AdapterID = "hosting.other" }, want: needsRef},
		{name: "adapter cannot inspect", step: func(s *store.OperationStep) {
			s.AdapterID = "execution.fake"
			s.Output = provisionR3MustEdit(s.Output, "bootstrap_execution_ref", map[string]any{"adapter_id": "execution.fake", "environment_id": "staging", "resource_kind": "bootstrap-execution", "external_id": "b"})
		}, want: needsRef},
		{name: "inspection fails", reply: func(*testing.T, provisionR3Completed) fakeadapter.Reply {
			return fakeadapter.Reply{Err: errors.New("offline")}
		}, want: needsRef},
		{name: "inspection not completed", reply: func(t *testing.T, c provisionR3Completed) fakeadapter.Reply {
			reply := inspection(nil)(t, c)
			reply.Outcome = adapter.OutcomeUnknown
			return reply
		}, want: needsRef},
		{name: "inspection schema invalid", reply: inspection(func(m map[string]any) { delete(m, "revision") }), want: needsRef},
		{name: "inspection other instance", reply: inspection(provisionR3SetIn("instance_ref", "external_id", "vm-other")), want: needsRef},
		{name: "inspection other execution", reply: inspection(provisionR3SetIn("bootstrap_execution_ref", "external_id", "other")), want: needsRef},
		{name: "inspection other revision", reply: inspection(provisionR3Set("revision", "bootstrap-2")), want: needsRef},
		{name: "inspection other digest", reply: inspection(provisionR3Set("digest", "sha256:"+provisionR3Hex64)), want: needsRef},
		{name: "running", reply: inspection(running), want: provisionR3Resolution{State: ResolutionPending, Ref: provisionR3PriorRef}},
		{name: "failed", reply: inspection(func(m map[string]any) { m["state"] = "failed" }),
			want: provisionR3Resolution{State: ResolutionDefinitiveError, Ref: provisionR3PriorRef, Error: "core.bootstrap.failed"}},
		{name: "unknown", reply: inspection(running), step: nil, want: provisionR3Resolution{State: ResolutionPending, Ref: provisionR3PriorRef}},
		{name: "state unknown", reply: inspection(func(m map[string]any) { running(m); m["state"] = "unknown" }), want: needsRef},
		{name: "no delivery output", step: func(s *store.OperationStep) {
			s.Output = nil
			s.ExternalOperationRef = json.RawMessage(provisionR3BootstrapRef)
		}, reply: inspection(nil),
			want: provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3BootstrapRef}},
		{name: "succeeded from prior reference", step: func(s *store.OperationStep) {
			s.Output = provisionR3MustDelete(s.Output, "bootstrap_execution_ref")
			s.ExternalOperationRef = json.RawMessage(provisionR3BootstrapRef)
		}, reply: inspection(nil), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3BootstrapRef, Output: "SAME"}},
		{name: "succeeded", reply: inspection(nil), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3PriorRef, Output: "SAME"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provisionR3RunInspectReconcile(t, provisionR3Inspected{restore: false, stepID: provisionBootstrapStep, capability: "hosting.bootstrap.inspect"}, test)
		})
	}
}

// provisionR3MustEdit sets one field of a JSON object; the fixtures are
// always valid objects, so a failure panics the test.
func provisionR3MustEdit(raw json.RawMessage, field string, value any) json.RawMessage {
	return provisionR3MustChange(raw, func(m map[string]any) { m[field] = value })
}

func provisionR3MustDelete(raw json.RawMessage, field string) json.RawMessage {
	return provisionR3MustChange(raw, func(m map[string]any) { delete(m, field) })
}

func provisionR3MustChange(raw json.RawMessage, change func(map[string]any)) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	change(m)
	encoded, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return encoded
}

// TestServerProvisionR3ReconcileRoutes pins the steps Reconcile does not
// inspect, and the unit start reconciliation.
func TestServerProvisionR3ReconcileRoutes(t *testing.T) {
	c := provisionR3Complete(t, true)
	ref := json.RawMessage(provisionR3PriorRef)
	routed := func(stepID string) store.OperationStep {
		step := operationStep(c.operation, stepID)
		step.ExternalOperationRef = ref
		return step
	}
	var missing context.Context
	cases := []struct {
		name string
		ctx  context.Context
		op   func(store.OperationRecord) store.OperationRecord
		step store.OperationStep
		want provisionR3Resolution
	}{
		{name: "nil context", ctx: missing, step: routed(provisionWaitStep), want: provisionR3Resolution{State: ResolutionNeedsOperator}},
		{name: "wrong type", op: func(o store.OperationRecord) store.OperationRecord { o.OperationType = "server.start"; return o }, step: routed(provisionWaitStep),
			want: provisionR3Resolution{State: ResolutionNeedsOperator}},
		{name: "compatibility", step: routed(provisionCompatibilityStep), want: provisionR3Resolution{State: ResolutionSafeToRetry, Ref: provisionR3PriorRef}},
		{name: "restore inspection", step: routed(provisionRestoreInspectStep), want: provisionR3Resolution{State: ResolutionSafeToRetry, Ref: provisionR3PriorRef}},
		{name: "readiness", step: routed(provisionReadinessStep), want: provisionR3Resolution{State: ResolutionSafeToRetry, Ref: provisionR3PriorRef}},
		{name: "wait", step: routed(provisionWaitStep), want: provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3PriorRef}},
		{name: "install", step: routed(provisionInstallStep), want: provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3PriorRef}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			operation := c.operation
			if test.op != nil {
				operation = test.op(operation)
			}
			if got := provisionR3Resolve(c.f.workflow.Reconcile(ctx, operation, test.step)); got != test.want {
				t.Fatalf("Reconcile = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestServerProvisionR3ReconcileRestore pins every reconcileRestore outcome.
func TestServerProvisionR3ReconcileRestore(t *testing.T) {
	needsRef := provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3PriorRef}
	needsExec := provisionR3Resolution{State: ResolutionNeedsOperator, Ref: provisionR3RestoreRef}
	inspection := func(change func(map[string]any)) func(*testing.T, provisionR3Completed) fakeadapter.Reply {
		return provisionR3InspectionOf(provisionRestoreInspectStep, change)
	}
	cases := []provisionR3InspectCase{
		{name: "wrong capability", step: func(s *store.OperationStep) { s.CapabilityName = "game.install" }, want: needsRef},
		{name: "no adapter", step: func(s *store.OperationStep) { s.AdapterID = "" }, want: needsRef},
		{name: "no created instance", change: provisionR3Step(provisionCreateStep, func(s *store.OperationStep) { s.Status = protocolstate.StepPending }), want: needsRef},
		{name: "input undecodable", step: func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`[`) }, want: needsRef},
		{name: "input without backup", step: func(s *store.OperationStep) {
			s.ResolvedInput = provisionR3MustEdit(s.ResolvedInput, "source_backup", map[string]any{})
		}, want: needsRef},
		{name: "input other instance", step: func(s *store.OperationStep) {
			s.ResolvedInput = provisionR3MustEdit(s.ResolvedInput, "target", map[string]any{"instance_ref": map[string]any{"external_id": "vm-other"}})
		}, want: needsRef},
		{name: "no execution reference", step: func(s *store.OperationStep) { s.Output = nil; s.ExternalOperationRef = nil },
			want: provisionR3Resolution{State: ResolutionNeedsOperator}},
		{name: "output reference not an object", step: func(s *store.OperationStep) {
			s.Output = provisionR3MustEdit(s.Output, "restore_execution_ref", "restore-r3")
		}, want: needsRef},
		{name: "execution of another adapter", step: func(s *store.OperationStep) { s.AdapterID = "game.other" }, want: needsRef},
		{name: "adapter cannot inspect", step: func(s *store.OperationStep) {
			s.AdapterID = "execution.fake"
			s.Output = provisionR3MustEdit(s.Output, "restore_execution_ref", map[string]any{"adapter_id": "execution.fake", "environment_id": "staging", "resource_kind": "restore-execution", "external_id": "r"})
		}, want: needsRef},
		{name: "inspection fails", reply: func(*testing.T, provisionR3Completed) fakeadapter.Reply {
			return fakeadapter.Reply{Err: errors.New("offline")}
		}, want: needsRef},
		{name: "inspection not completed", reply: func(t *testing.T, c provisionR3Completed) fakeadapter.Reply {
			reply := inspection(nil)(t, c)
			reply.Outcome = adapter.OutcomeAccepted
			return reply
		}, want: needsRef},
		{name: "inspection schema invalid", reply: inspection(func(m map[string]any) { delete(m, "backup_id") }), want: needsRef},
		{name: "inspection other backup", reply: inspection(provisionR3Set("backup_id", "other")), want: needsRef},
		{name: "inspection other environment", reply: inspection(provisionR3Set("environment_id", "production")), want: needsRef},
		{name: "inspection other source", reply: inspection(provisionR3Set("source_data_generation_id", "gen-other")), want: needsRef},
		{name: "inspection other target generation", reply: inspection(provisionR3Set("target_data_generation_id", "gen-other")), want: needsRef},
		{name: "inspection other target", reply: inspection(provisionR3SetIn("target", "server_id", "other")), want: needsRef},
		{name: "inspection other execution", reply: inspection(provisionR3SetIn("restore_execution_ref", "external_id", "other")), want: needsRef},
		{name: "restored with another check", reply: inspection(func(m map[string]any) {
			m["validation_checks"].([]any)[0].(map[string]any)["check_id"] = "other.check"
		}), want: needsExec},
		{name: "rejected", reply: inspection(func(m map[string]any) { m["outcome"] = "rejected"; delete(m, "validation_checks") }),
			want: provisionR3Resolution{State: ResolutionDefinitiveError, Ref: provisionR3RestoreRef, Error: "core.restore.rejected"}},
		{name: "outcome unknown", reply: inspection(func(m map[string]any) { m["outcome"] = "outcome_unknown"; delete(m, "validation_checks") }), want: needsExec},
		{name: "restored from prior reference", step: func(s *store.OperationStep) {
			s.Output = nil
			s.ExternalOperationRef = json.RawMessage(provisionR3RestoreRef)
		}, reply: inspection(nil),
			want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3RestoreRef, Output: "SAME"}},
		{name: "restored", reply: inspection(nil), want: provisionR3Resolution{State: ResolutionCompleted, Ref: provisionR3RestoreRef, Output: "SAME"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provisionR3RunInspectReconcile(t, provisionR3Inspected{restore: true, stepID: provisionRestoreStep, capability: "game.restore.inspect"}, test)
		})
	}
}

// provisionR3InspectCase is one reconciliation case for a step that
// reconciles by inspecting its recorded execution.
type provisionR3InspectCase struct {
	name   string
	change func(*testing.T, *store.OperationRecord)
	step   func(*store.OperationStep)
	reply  func(*testing.T, provisionR3Completed) fakeadapter.Reply
	want   provisionR3Resolution
}

// provisionR3Inspected names the reconciled step and its inspect capability.
type provisionR3Inspected struct {
	restore    bool
	stepID     string
	capability string
}

// provisionR3InspectionOf replies with stepID's recorded output, changed.
func provisionR3InspectionOf(stepID string, change func(map[string]any)) func(*testing.T, provisionR3Completed) fakeadapter.Reply {
	return func(t *testing.T, c provisionR3Completed) fakeadapter.Reply {
		output := operationStep(c.operation, stepID).Output
		if change != nil {
			output = provisionR3EditJSON(t, output, change)
		}
		return provisionR3Completed1(adapter.OutcomeCompleted, output)
	}
}

// provisionR3RunInspectReconcile reconciles the completed fixture's step
// after the case's changes. A wanted Output of "SAME" means the reply output.
func provisionR3RunInspectReconcile(t *testing.T, inspected provisionR3Inspected, test provisionR3InspectCase) {
	t.Helper()
	c := provisionR3Complete(t, inspected.restore)
	c.operation = provisionR3Clone(c.operation)
	if test.change != nil {
		test.change(t, &c.operation)
	}
	step := operationStep(c.operation, inspected.stepID)
	step.ExternalOperationRef = json.RawMessage(provisionR3PriorRef)
	if test.step != nil {
		test.step(&step)
	}
	want := test.want
	if test.reply != nil {
		reply := test.reply(t, c)
		fake := c.f.hosting
		if inspected.restore {
			fake = c.f.game
		}
		provisionR3Reply(t, fake, inspected.capability, reply)
		if want.Output == "SAME" {
			want.Output = string(reply.Output)
		}
	}
	if got := provisionR3Resolve(c.f.workflow.Reconcile(context.Background(), c.operation, step)); got != want {
		t.Fatalf("Reconcile = %#v, want %#v", got, want)
	}
}
