package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// provisionedServerRepository reports the stored logical server with a
// created instance and, when set, another lifecycle state.
type provisionedServerRepository struct {
	store.LifecycleOperationStore
	instanceRef json.RawMessage
	state       protocolstate.ServerState
}

func (r provisionedServerRepository) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	stored, err := r.LifecycleOperationStore.GetLogicalServer(environmentID, serverID)
	if err != nil {
		return stored, err
	}
	stored.Record.InstanceRef = r.instanceRef
	if r.state != "" {
		stored.Record.LifecycleState = r.state
	}
	return stored, nil
}

const provisionTestInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`

// provisionPreconditionFixture plans the provision, then has the workflow
// see a created instance and every step bound to it.
func provisionPreconditionFixture(t *testing.T, instance string) (*ServerProvisionWorkflow, store.OperationRecord) {
	t.Helper()
	f := newProvisionPlanFixture(t)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	f.workflow.servers = provisionedServerRepository{LifecycleOperationStore: f.workflow.servers, instanceRef: json.RawMessage(instance)}
	bound := json.RawMessage(`{"instance_ref":` + provisionTestInstance + `,"target":{"instance_ref":` + provisionTestInstance + `}}`)
	for i := range steps {
		steps[i].ResolvedInput = bound
	}
	operation := f.operation
	operation.Steps = steps
	return f.workflow, operation
}

func provisionStepIndex(operation store.OperationRecord, stepID string) int {
	for i, step := range operation.Steps {
		if step.StepID == stepID {
			return i
		}
	}
	return -1
}

// TestServerProvisionPreconditionsPerStep pins, for every planned step, the
// error while its prerequisites are pending, the result once only its direct
// prerequisite has succeeded with an empty output, and the error when the
// step is bound to another instance.
func TestServerProvisionPreconditionsPerStep(t *testing.T) {
	notBound := func(step string) string {
		return `provision step "` + step + `" is not bound to the exact created instance`
	}
	cases := []struct {
		step, prerequisite, pending, withPrerequisite, foreign string
	}{
		{provisionWaitStep, provisionCreateStep, "instance wait requires a completed provider create", "", notBound(provisionWaitStep)},
		{provisionBootstrapStep, provisionWaitStep, "bootstrap requires the exact instance to be confirmed active", "bootstrap requires the exact instance to be confirmed active", notBound(provisionBootstrapStep)},
		{provisionBootstrapInspect, provisionBootstrapStep, "bootstrap inspection requires a completed delivery request", "", notBound(provisionBootstrapInspect)},
		{provisionInstallStep, provisionBootstrapInspect, "game installation requires successful inspection of the pinned bootstrap", "game installation requires successful inspection of the pinned bootstrap", notBound(provisionInstallStep)},
		{provisionCreateUnitStep, provisionInstallStep, "execution unit creation requires verified installation of the pinned game release", "execution unit creation requires verified installation of the pinned game release", notBound(provisionCreateUnitStep)},
		{provisionStartUnitStep, provisionCreateUnitStep, "execution unit start requires a verified stopped or unchanged unit", "execution unit start requires a verified stopped or unchanged unit", "execution unit start requires a verified stopped or unchanged unit"},
		{provisionInspectUnitStep, provisionStartUnitStep, "unit inspection requires a completed start", "unit inspection requires a completed start", "unit inspection requires a completed start"},
		{provisionReadinessStep, provisionInspectUnitStep, "game readiness requires the exact execution unit to be running", "game readiness requires the exact execution unit to be running", notBound(provisionReadinessStep)},
	}
	workflow, operation := provisionPreconditionFixture(t, provisionTestInstance)
	foreignInput := json.RawMessage(`{"instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-2"}}`)
	for _, test := range cases {
		t.Run(test.step, func(t *testing.T) {
			step := operation.Steps[provisionStepIndex(operation, test.step)]
			if got := provisionErrText(workflow.CheckPreconditions(context.Background(), operation, step)); got != test.pending {
				t.Fatalf("pending: %q, want %q", got, test.pending)
			}
			withPrerequisite := operation
			withPrerequisite.Steps = append([]store.OperationStep(nil), operation.Steps...)
			i := provisionStepIndex(withPrerequisite, test.prerequisite)
			withPrerequisite.Steps[i].Status, withPrerequisite.Steps[i].Output = protocolstate.StepSucceeded, json.RawMessage(`{}`)
			if got := provisionErrText(workflow.CheckPreconditions(context.Background(), withPrerequisite, step)); got != test.withPrerequisite {
				t.Fatalf("with prerequisite: %q, want %q", got, test.withPrerequisite)
			}
			foreign := step
			foreign.ResolvedInput = foreignInput
			if got := provisionErrText(workflow.CheckPreconditions(context.Background(), operation, foreign)); got != test.foreign {
				t.Fatalf("foreign: %q, want %q", got, test.foreign)
			}
		})
	}
	t.Run("unit steps bound through the created unit", func(t *testing.T) {
		unbound := operation
		unbound.Steps = append([]store.OperationStep(nil), operation.Steps...)
		unbound.Steps[provisionStepIndex(unbound, provisionCreateUnitStep)].ResolvedInput = foreignInput
		for _, id := range []string{provisionStartUnitStep, provisionInspectUnitStep} {
			want := `provision step "` + id + `" is not bound to the exact created unit and instance`
			if got := provisionErrText(workflow.CheckPreconditions(context.Background(), unbound, unbound.Steps[provisionStepIndex(unbound, id)])); got != want {
				t.Fatalf("%s: %q, want %q", id, got, want)
			}
		}
	})
}

// TestServerProvisionPreconditionsGuards pins the checks that run before the
// per-step rules, the create and compatibility rules, and steps outside
// this plan.
func TestServerProvisionPreconditionsGuards(t *testing.T) {
	workflow, operation := provisionPreconditionFixture(t, provisionTestInstance)
	wait := operation.Steps[provisionStepIndex(operation, provisionWaitStep)]
	create := operation.Steps[provisionStepIndex(operation, provisionCreateStep)]
	renamed := func(id string) store.OperationStep {
		step := wait
		step.StepID = id
		return step
	}
	unprovisioned, plain := provisionPreconditionFixture(t, "")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	cases := []struct {
		name     string
		workflow *ServerProvisionWorkflow
		ctx      context.Context
		mutate   func(*store.OperationRecord)
		step     store.OperationStep
		want     string
	}{
		{name: "nil context", workflow: workflow, ctx: missing, step: wait, want: "server.provision operation and context are required"},
		{name: "wrong type", workflow: workflow, mutate: func(o *store.OperationRecord) { o.OperationType = "server.start" }, step: wait, want: "server.provision operation and context are required"},
		{name: "wrong protocol", workflow: workflow, mutate: func(o *store.OperationRecord) { o.ProtocolVersion = "0.0.1" }, step: wait, want: "server.provision operation and context are required"},
		{name: "cancelled", workflow: workflow, ctx: cancelled, step: wait, want: context.Canceled.Error()},
		{name: "unknown server", workflow: workflow, mutate: func(o *store.OperationRecord) { o.Target.ServerID = "other" }, step: wait, want: store.ErrLogicalServerNotFound.Error()},
		{name: "undecodable request", workflow: workflow, mutate: func(o *store.OperationRecord) { o.RequestSnapshot.Parameters = json.RawMessage(`[`) }, step: wait, want: "persisted provision request cannot be decoded"},
		{name: "compatibility without restore", workflow: workflow, step: renamed(provisionCompatibilityStep), want: "backup compatibility step is not part of this provision request"},
		{name: "create with an instance", workflow: workflow, step: create, want: "provider create requires an unprovisioned logical server"},
		{name: "create unprovisioned", workflow: unprovisioned, step: create, want: ""},
		{name: "create retried", workflow: unprovisioned, step: func() store.OperationStep { s := create; s.Attempt = 1; return s }(), want: "provider create requires an unprovisioned logical server"},
		{name: "no created instance", workflow: unprovisioned, step: wait, want: "provision step requires a verified created instance"},
		{name: "restore", workflow: workflow, step: renamed(provisionRestoreStep), want: "backup restore requires verified installation and a compatible exact backup"},
		{name: "restore inspection", workflow: workflow, step: renamed(provisionRestoreInspectStep), want: "restore inspection requires the recorded restore call"},
		{name: "unknown step", workflow: workflow, step: renamed("mystery"), want: `unsupported server.provision step "mystery"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			current := operation
			if test.workflow == unprovisioned {
				current = plain
			}
			if test.mutate != nil {
				test.mutate(&current)
			}
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			if got := provisionErrText(test.workflow.CheckPreconditions(ctx, current, test.step)); got != test.want {
				t.Fatalf("CheckPreconditions = %q, want %q", got, test.want)
			}
		})
	}
}

func provisionErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
