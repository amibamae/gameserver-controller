package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the exact Resolution that Reconcile returns
// for every branch of server.destroy provider reconciliation, including the
// optional exact wait and the read-only provider inspection fallback.

const destroyReconcileOperationRef = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"operation","external_id":"destroy-accepted-1"}`

// destroyRuntimeOverride wraps the adapter registry so a test can change one
// descriptor lookup or answer one invocation without the registry's checks.
type destroyRuntimeOverride struct {
	adapterRuntime
	descriptorCalls int
	descriptor      func(call int, descriptor adapter.Descriptor, err error) (adapter.Descriptor, error)
	invoke          func(adapter.InvokeRequest) (adapter.InvokeResponse, bool)
}

func (r *destroyRuntimeOverride) Descriptor(adapterID string) (adapter.Descriptor, error) {
	r.descriptorCalls++
	descriptor, err := r.adapterRuntime.Descriptor(adapterID)
	if r.descriptor != nil {
		return r.descriptor(r.descriptorCalls, descriptor, err)
	}
	return descriptor, err
}

func (r *destroyRuntimeOverride) Invoke(ctx context.Context, adapterID string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if r.invoke != nil {
		if response, handled := r.invoke(request); handled {
			return response, nil
		}
	}
	return r.adapterRuntime.Invoke(ctx, adapterID, request)
}

// destroySchemaOverride accepts any document for one schema URI.
type destroySchemaOverride struct {
	schemaRuntime
	lenientURI string
}

func (s destroySchemaOverride) ValidateJSON(uri string, document []byte) error {
	if uri == s.lenientURI {
		return nil
	}
	return s.schemaRuntime.ValidateJSON(uri, document)
}

func destroyWithoutCapability(name string) func(int, adapter.Descriptor, error) (adapter.Descriptor, error) {
	return func(_ int, descriptor adapter.Descriptor, err error) (adapter.Descriptor, error) {
		kept := descriptor.Capabilities[:0:0]
		for _, capability := range descriptor.Capabilities {
			if capability.Name != name {
				kept = append(kept, capability)
			}
		}
		descriptor.Capabilities = kept
		return descriptor, err
	}
}

// destroyReconcileState is one destroy step left running after dispatch, with
// the exact provider and runtime inspections recorded as succeeded.
type destroyReconcileState struct {
	fixture   serverDestroyFixture
	workflow  *ServerDestroyWorkflow
	operation store.OperationRecord
	step      store.OperationStep
	started   time.Time
	runtime   *destroyRuntimeOverride
}

func destroyReconcileFixture(t *testing.T) *destroyReconcileState {
	t.Helper()
	fixture := newServerDestroyFixture(t)
	operation := destroyOperationAfter(fixture, 2)
	started := time.Now().UTC()
	step := operation.Steps[2]
	step.Status = protocolstate.StepRunning
	step.Attempt = 1
	step.StartedAt = &started
	operation.Steps[2] = step
	return &destroyReconcileState{fixture: fixture, workflow: fixture.workflow, operation: operation, step: step, started: started}
}

// destroyOverrideRuntime rebuilds the workflow over a wrapped registry.
func (s *destroyReconcileState) destroyOverrideRuntime(t *testing.T) *destroyRuntimeOverride {
	t.Helper()
	s.runtime = &destroyRuntimeOverride{adapterRuntime: s.fixture.base.registry}
	workflow := *s.fixture.workflow
	workflow.adapters = s.runtime
	s.workflow = &workflow
	return s.runtime
}

func (s *destroyReconcileState) destroyScriptInspect(t *testing.T, replies ...fakeadapter.Reply) {
	t.Helper()
	if err := s.fixture.base.hosting.SetScript("hosting.instance.inspect", replies...); err != nil {
		t.Fatal(err)
	}
}

func (s *destroyReconcileState) destroyScriptWait(t *testing.T, replies ...fakeadapter.Reply) {
	t.Helper()
	if err := s.fixture.base.hosting.SetScript("hosting.instance.wait", replies...); err != nil {
		t.Fatal(err)
	}
}

func destroyInspectReply(instance, state string, observedAt, replyAt time.Time) fakeadapter.Reply {
	return fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: instanceInspectionOutput(json.RawMessage(instance), state, observedAt), ObservedAt: replyAt}
}

func destroyWaitReply(instance, operationRef, status, instanceState, observedAt string, replyAt time.Time) fakeadapter.Reply {
	fields := []string{
		`"instance_ref":` + instance,
		`"provider_operation_ref":` + operationRef,
		fmt.Sprintf(`"operation_status":%q`, status),
	}
	if instanceState != "" {
		fields = append(fields, fmt.Sprintf(`"instance_state":%q`, instanceState))
	}
	fields = append(fields, fmt.Sprintf(`"observed_at":%q`, observedAt))
	return fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage("{" + strings.Join(fields, ",") + "}"), ObservedAt: replyAt}
}

// destroyCompletedOutput is the exact normalized destroy output Reconcile
// records once provider absence is observed.
func destroyCompletedOutput(instance, observedAt string) string {
	return `{"instance_ref":` + instance + `,"outcome":"completed","state":"destroyed","observed_at":"` + observedAt + `"}`
}

func destroyHostingCapabilities(calls []adapter.InvokeRequest) []string {
	names := make([]string, len(calls))
	for i, call := range calls {
		names[i] = call.Params.Capability
	}
	return names
}

func destroyWantResolution(t *testing.T, got Resolution, err error, want Resolution) {
	t.Helper()
	if err != nil {
		t.Fatalf("Reconcile error = %v, want nil", err)
	}
	if got.State != want.State || !bytes.Equal(got.Output, want.Output) ||
		!bytes.Equal(got.ExternalOperationRef, want.ExternalOperationRef) || !bytes.Equal(got.Error, want.Error) {
		t.Fatalf("Reconcile = {State:%s Output:%s Ref:%s Error:%s}, want {State:%s Output:%s Ref:%s Error:%s}",
			got.State, got.Output, got.ExternalOperationRef, got.Error, want.State, want.Output, want.ExternalOperationRef, want.Error)
	}
	if (got.Output == nil) != (want.Output == nil) || (got.ExternalOperationRef == nil) != (want.ExternalOperationRef == nil) {
		t.Fatalf("Reconcile nil-ness = output %v ref %v, want output %v ref %v",
			got.Output == nil, got.ExternalOperationRef == nil, want.Output == nil, want.ExternalOperationRef == nil)
	}
}

func TestServerDestroyReconcileCharacterization(t *testing.T) {
	ref := json.RawMessage(destroyReconcileOperationRef)
	needs := Resolution{State: ResolutionNeedsOperator}
	needsRef := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: ref}
	pendingRef := Resolution{State: ResolutionPending, ExternalOperationRef: ref}
	// later is a fixed offset after the step started; reply times differ from
	// output times so the test pins which one each path records.
	later := func(s *destroyReconcileState, seconds int) time.Time {
		return s.started.Add(time.Duration(seconds) * time.Second)
	}
	waitAt := func(s *destroyReconcileState) string { return later(s, 2).Format(time.RFC3339Nano) }
	type wantFunc func(*destroyReconcileState) Resolution
	fixed := func(r Resolution) wantFunc { return func(*destroyReconcileState) Resolution { return r } }
	completedAt := func(withRef bool, at func(*destroyReconcileState) string) wantFunc {
		return func(s *destroyReconcileState) Resolution {
			resolution := Resolution{State: ResolutionCompleted, Output: json.RawMessage(destroyCompletedOutput(startTestInstance, at(s)))}
			if withRef {
				resolution.ExternalOperationRef = ref
			}
			return resolution
		}
	}
	inspectReplyAt := func(s *destroyReconcileState) string { return later(s, 4).Format(time.RFC3339Nano) }
	inspect := func(state string) func(*testing.T, *destroyReconcileState) {
		return func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, destroyInspectReply(startTestInstance, state, later(s, 3), later(s, 4)))
		}
	}
	wait := func(status, instanceState string) func(*testing.T, *destroyReconcileState) {
		return func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptWait(t, destroyWaitReply(startTestInstance, destroyReconcileOperationRef, status, instanceState, waitAt(s), later(s, 5)))
		}
	}
	both := func(steps ...func(*testing.T, *destroyReconcileState)) func(*testing.T, *destroyReconcileState) {
		return func(t *testing.T, s *destroyReconcileState) {
			for _, step := range steps {
				step(t, s)
			}
		}
	}
	tests := []struct {
		name string
		// withRef records the provider operation reference on the step.
		withRef bool
		ctx     func() context.Context
		setup   func(*testing.T, *destroyReconcileState)
		nilW    bool
		want    wantFunc
		calls   []string
	}{
		{name: "missing context", ctx: func() context.Context { return nil }, want: fixed(needs)},
		{name: "nil workflow", nilW: true, want: fixed(needs)},
		{name: "another operation type", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.OperationType = "server.stop" }, want: fixed(needs)},
		{name: "another step", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.step.StepID = destroyVerifyInstanceStep }, want: fixed(needs)},
		{name: "ended context", withRef: true, ctx: func() context.Context {
			ended, cancel := context.WithCancel(context.Background())
			cancel()
			return ended
		}, want: fixed(needs)},
		{name: "invalid parameters", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) {
			s.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_profile":"wasabi-test"}`)
		}, want: fixed(needs)},
		{name: "another protocol version", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.ProtocolVersion = "0.0.1" }, want: fixed(needs)},
		{name: "unknown server", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.Target.ServerID = "server-missing" }, want: fixed(needsRef)},
		{name: "unknown server without reference", setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.Target.ServerID = "server-missing" }, want: fixed(needs)},
		{name: "target instance changed", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) {
			s.operation.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
		}, want: fixed(needsRef)},
		{name: "destroy step bound to another instance", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			setStepInput(t, &s.step, "instance_ref", json.RawMessage(otherDestroyInstance))
		}, want: fixed(needsRef)},
		{name: "destroy capability version changed", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.step.CapabilityVersion = "2" }, want: fixed(needsRef)},
		{name: "destroy step on another adapter", withRef: true, setup: func(_ *testing.T, s *destroyReconcileState) { s.step.AdapterID = "execution.fake" }, want: fixed(needsRef)},
		{name: "destroy capability withdrawn", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.destroy")
		}, want: fixed(needsRef)},

		// Without a provider operation reference only the exact inspection decides.
		{name: "no reference, provider destroyed", setup: inspect("destroyed"), want: completedAt(false, inspectReplyAt), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, provider destroying", setup: inspect("destroying"), want: fixed(Resolution{State: ResolutionPending}), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, provider active", setup: inspect("active"), want: fixed(needs), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, provider stopped", setup: inspect("stopped"), want: fixed(needs), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, provider unknown", setup: inspect("unknown"), want: fixed(needs), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, inspection fails", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, fakeadapter.Reply{Err: errors.New("provider unavailable")})
		}, want: fixed(needs), calls: []string{"hosting.instance.inspect"}},
		{name: "no reference, inspection of another instance", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, destroyInspectReply(otherDestroyInstance, "destroyed", later(s, 3), later(s, 4)))
		}, want: fixed(needs), calls: []string{"hosting.instance.inspect"}},

		// With a provider operation reference the exact wait is tried first.
		{name: "wait descriptor lookup fails", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = func(call int, d adapter.Descriptor, err error) (adapter.Descriptor, error) {
				if call == 2 {
					return adapter.Descriptor{}, errors.New("registry unavailable")
				}
				return d, err
			}
		}, want: fixed(needsRef), calls: []string{}},
		{name: "wait descriptor has another category", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = func(call int, d adapter.Descriptor, err error) (adapter.Descriptor, error) {
				if call == 2 {
					d.Category = adapter.CategoryExecution
				}
				return d, err
			}
		}, want: fixed(needsRef), calls: []string{}},
		{name: "wait not advertised falls back to inspection", withRef: true, setup: both(func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.wait")
		}, inspect("destroyed")), want: completedAt(true, inspectReplyAt), calls: []string{"hosting.instance.inspect"}},
		{name: "wait not advertised, provider destroying", withRef: true, setup: both(func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.wait")
		}, inspect("destroying")), want: fixed(pendingRef), calls: []string{"hosting.instance.inspect"}},
		{name: "wait not advertised, provider active", withRef: true, setup: both(func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.wait")
		}, inspect("active")), want: fixed(needsRef), calls: []string{"hosting.instance.inspect"}},
		{name: "wait not advertised, inspection fails", withRef: true, setup: both(func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.wait")
		}, func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, fakeadapter.Reply{Err: errors.New("provider unavailable")})
		}), want: fixed(needsRef), calls: []string{"hosting.instance.inspect"}},
		{name: "wait call fails", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptWait(t, fakeadapter.Reply{Err: errors.New("provider unavailable")})
		}, want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait observed another instance", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptWait(t, destroyWaitReply(otherDestroyInstance, destroyReconcileOperationRef, "succeeded", "destroyed", waitAt(s), later(s, 5)))
		}, want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait observed another provider operation", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			other := strings.Replace(destroyReconcileOperationRef, "destroy-accepted-1", "destroy-accepted-2", 1)
			s.destroyScriptWait(t, destroyWaitReply(startTestInstance, other, "succeeded", "destroyed", waitAt(s), later(s, 5)))
		}, want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait observation is stale", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptWait(t, destroyWaitReply(startTestInstance, destroyReconcileOperationRef, "succeeded", "destroyed",
				s.started.Add(-time.Hour).Format(time.RFC3339Nano), later(s, 5)))
		}, want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait pending", withRef: true, setup: wait("pending", ""), want: fixed(pendingRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait pending while destroying", withRef: true, setup: wait("pending", "destroying"), want: fixed(pendingRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait succeeded, destroyed", withRef: true, setup: wait("succeeded", "destroyed"), want: completedAt(true, waitAt), calls: []string{"hosting.instance.wait"}},
		{name: "wait succeeded keeps the observed timestamp verbatim", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			observed := later(s, 2).Truncate(time.Second).Format("2006-01-02T15:04:05+00:00")
			s.destroyScriptWait(t, destroyWaitReply(startTestInstance, destroyReconcileOperationRef, "succeeded", "destroyed", observed, later(s, 5)))
		}, want: completedAt(true, func(s *destroyReconcileState) string {
			return later(s, 2).Truncate(time.Second).Format("2006-01-02T15:04:05+00:00")
		}), calls: []string{"hosting.instance.wait"}},
		{name: "wait succeeded, destroying", withRef: true, setup: wait("succeeded", "destroying"), want: fixed(pendingRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait succeeded, active", withRef: true, setup: wait("succeeded", "active"), want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait succeeded, unknown", withRef: true, setup: wait("succeeded", "unknown"), want: fixed(needsRef), calls: []string{"hosting.instance.wait"}},
		{name: "wait failed, provider destroyed", withRef: true, setup: both(wait("failed", ""), inspect("destroyed")), want: completedAt(true, inspectReplyAt),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, provider destroying", withRef: true, setup: both(wait("failed", ""), inspect("destroying")), want: fixed(pendingRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, provider active", withRef: true, setup: both(wait("failed", ""), inspect("active")), want: fixed(Resolution{State: ResolutionSafeToRetry}),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, provider stopped", withRef: true, setup: both(wait("failed", "stopped"), inspect("stopped")), want: fixed(Resolution{State: ResolutionSafeToRetry}),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, provider creating", withRef: true, setup: both(wait("failed", ""), inspect("creating")), want: fixed(needsRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, provider unknown", withRef: true, setup: both(wait("failed", ""), inspect("unknown")), want: fixed(needsRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait failed, inspection fails", withRef: true, setup: both(wait("failed", ""), func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, fakeadapter.Reply{Err: errors.New("provider unavailable")})
		}), want: fixed(needsRef), calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait unknown, provider destroyed", withRef: true, setup: both(wait("unknown", ""), inspect("destroyed")), want: completedAt(true, inspectReplyAt),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait unknown, provider destroying", withRef: true, setup: both(wait("unknown", ""), inspect("destroying")), want: fixed(pendingRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait unknown, provider active", withRef: true, setup: both(wait("unknown", ""), inspect("active")), want: fixed(needsRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait unknown, provider stopped", withRef: true, setup: both(wait("unknown", ""), inspect("stopped")), want: fixed(needsRef),
			calls: []string{"hosting.instance.wait", "hosting.instance.inspect"}},
		{name: "wait status outside the contract", withRef: true, setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).invoke = func(request adapter.InvokeRequest) (adapter.InvokeResponse, bool) {
				if request.Params.Capability != "hosting.instance.wait" {
					return adapter.InvokeResponse{}, false
				}
				output := destroyWaitReply(startTestInstance, destroyReconcileOperationRef, "cancelled", "destroyed", waitAt(s), later(s, 5)).Output
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: output}}, true
			}
			s.workflow.schemas = destroySchemaOverride{schemaRuntime: s.fixture.base.schemas,
				lenientURI: "urn:gameserver-controller:" + s.fixture.base.schemas.ProtocolVersion() + ":hosting:instance-wait-output"}
		}, want: fixed(needsRef), calls: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := destroyReconcileFixture(t)
			if test.withRef {
				state.step.ExternalOperationRef = ref
				state.operation.Steps[2].ExternalOperationRef = ref
			}
			if test.setup != nil {
				test.setup(t, state)
			}
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			workflow := state.workflow
			if test.nilW {
				workflow = nil
			}
			before := len(state.fixture.base.hosting.Calls())
			got, err := workflow.Reconcile(ctx, state.operation, state.step)
			destroyWantResolution(t, got, err, test.want(state))
			if test.calls != nil {
				calls := destroyHostingCapabilities(state.fixture.base.hosting.Calls()[before:])
				if strings.Join(calls, ",") != strings.Join(test.calls, ",") {
					t.Fatalf("provider calls = %v, want %v", calls, test.calls)
				}
			}
		})
	}
}

func TestServerDestroyReconcileWaitRequest(t *testing.T) {
	state := destroyReconcileFixture(t)
	ref := json.RawMessage(destroyReconcileOperationRef)
	state.step.ExternalOperationRef = ref
	state.operation.CorrelationID = "destroy-correlation"
	state.destroyScriptWait(t, destroyWaitReply(startTestInstance, destroyReconcileOperationRef, "failed", "", state.started.Add(time.Second).Format(time.RFC3339Nano), state.started))
	state.destroyScriptInspect(t, destroyInspectReply(startTestInstance, "active", state.started.Add(time.Second), state.started))
	before := len(state.fixture.base.hosting.Calls())
	if _, err := state.workflow.Reconcile(context.Background(), state.operation, state.step); err != nil {
		t.Fatal(err)
	}
	calls := state.fixture.base.hosting.Calls()[before:]
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want wait and inspect", len(calls))
	}
	wantInput := `{"expected_state":"destroyed","instance_ref":` + startTestInstance + `,"provider_operation_ref":` + destroyReconcileOperationRef + `,"wait_timeout_seconds":15}`
	if string(calls[0].Params.Input) != wantInput {
		t.Fatalf("wait input = %s, want %s", calls[0].Params.Input, wantInput)
	}
	if string(calls[1].Params.Input) != string(state.operation.Steps[0].Input) {
		t.Fatalf("inspect input = %s, want the planned inspection input %s", calls[1].Params.Input, state.operation.Steps[0].Input)
	}
	for i, label := range []string{"destroy-wait", "destroy-inspect"} {
		wantID := "destroy-destroy-operation-destroy-instance-1-" + label
		rc := calls[i].Params.RequestContext
		if calls[i].ID != wantID || rc.RequestID != wantID || rc.CorrelationID != "destroy-correlation" ||
			rc.OperationID != "destroy-operation" || rc.StepID != destroyProviderStep {
			t.Fatalf("call %d id/context = %s/%#v, want %s", i, calls[i].ID, rc, wantID)
		}
	}
}

func TestServerDestroyInspectProviderForReconciliationCharacterization(t *testing.T) {
	const (
		noStep  = "destroy reconciliation has no exact-instance inspection step"
		changed = "destroy reconciliation inspection capability changed"
		stale   = "destroy reconciliation inspection is stale or not bound to the exact instance"
	)
	tests := []struct {
		name     string
		instance string
		setup    func(*testing.T, *destroyReconcileState)
		want     string
	}{
		{name: "no inspection step", setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.Steps[0].StepID = "inspect-other" }, want: noStep},
		{name: "inspection step uses another capability", setup: func(_ *testing.T, s *destroyReconcileState) {
			s.operation.Steps[0].CapabilityName = "hosting.instance.wait"
		}, want: noStep},
		{name: "inspection step on another adapter", setup: func(_ *testing.T, s *destroyReconcileState) { s.step.AdapterID = "hosting.other" }, want: noStep},
		{name: "inspection step without a version", setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.Steps[0].CapabilityVersion = "" }, want: noStep},
		{name: "inspection step bound to another instance", instance: otherDestroyInstance, want: noStep},
		{name: "inspection capability version changed", setup: func(_ *testing.T, s *destroyReconcileState) { s.operation.Steps[0].CapabilityVersion = "2" }, want: changed},
		{name: "inspection capability withdrawn", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).descriptor = destroyWithoutCapability("hosting.instance.inspect")
		}, want: changed},
		{name: "inspection call fails", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, fakeadapter.Reply{Err: errors.New("provider unavailable")})
		}, want: stale},
		{name: "inspection reply is not completed", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyOverrideRuntime(t).invoke = func(request adapter.InvokeRequest) (adapter.InvokeResponse, bool) {
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeAccepted}}, true
			}
		}, want: stale},
		{name: "inspection of another instance", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, destroyInspectReply(otherDestroyInstance, "destroyed", s.started.Add(time.Second), s.started))
		}, want: stale},
		{name: "stale inspection", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, destroyInspectReply(startTestInstance, "destroyed", s.started.Add(-time.Hour), s.started))
		}, want: stale},
		{name: "inspection from the future", setup: func(t *testing.T, s *destroyReconcileState) {
			s.destroyScriptInspect(t, destroyInspectReply(startTestInstance, "destroyed", time.Now().UTC().Add(time.Hour), s.started))
		}, want: stale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := destroyReconcileFixture(t)
			if test.setup != nil {
				test.setup(t, state)
			}
			instance := startTestInstance
			if test.instance != "" {
				instance = test.instance
			}
			observed, at, err := state.workflow.inspectProviderForReconciliation(context.Background(), state.operation, state.step, json.RawMessage(instance))
			if err == nil || err.Error() != test.want {
				t.Fatalf("inspect error = %v, want %q", err, test.want)
			}
			if observed.State != "" || len(observed.InstanceRef) != 0 || observed.ObservedAt != "" || !at.IsZero() {
				t.Fatalf("failed inspection returned %#v at %s, want zero values", observed, at)
			}
		})
	}

	t.Run("exact fresh inspection", func(t *testing.T) {
		state := destroyReconcileFixture(t)
		observedAt := state.started.Add(time.Second)
		replyAt := state.started.Add(2 * time.Second)
		state.destroyScriptInspect(t, destroyInspectReply(startTestInstance, "destroying", observedAt, replyAt))
		observed, at, err := state.workflow.inspectProviderForReconciliation(context.Background(), state.operation, state.step, json.RawMessage(startTestInstance))
		if err != nil {
			t.Fatal(err)
		}
		if observed.State != "destroying" || string(observed.InstanceRef) != startTestInstance ||
			observed.ObservedAt != observedAt.Format(time.RFC3339Nano) || !at.Equal(replyAt) {
			t.Fatalf("inspection = %#v at %s, want destroying at reply time %s", observed, at, replyAt)
		}
	})
}
