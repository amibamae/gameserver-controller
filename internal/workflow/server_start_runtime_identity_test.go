package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	startInspectVersionError   = `server.start requires execution.unit.inspect capability version 2, which reports runtime_id; step "verify-runtime" uses version "1"`
	startReadinessVersionError = `server.start requires game.readiness.check capability version 2, which reports runtime_id; step "check-readiness" uses version "1"`
)

// TestServerStartPlanRequiresRuntimeIdentityCapabilities pins that a start
// is refused at planning, before anything is dispatched, unless the
// execution adapter's unit inspection and the game adapter's readiness check
// are declared at the capability version that reports runtime_id.
func TestServerStartPlanRequiresRuntimeIdentityCapabilities(t *testing.T) {
	cases := []struct {
		name, adapterID, capability, want string
	}{
		{name: "unit inspection at version 1", adapterID: "execution.fake", capability: "execution.unit.inspect", want: startInspectVersionError},
		{name: "readiness check at version 1", adapterID: "game.paper", capability: "game.readiness.check", want: startReadinessVersionError},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			startStopR3EditCapability(f, test.adapterID, test.capability, func(capability *adapter.Capability) { capability.Version = "1" })
			steps, err := f.start.BuildPlan(context.Background(), startStopR3Operation("server.start", f.version(), `{}`))
			if startStopR3ErrText(err) != test.want || steps != nil {
				t.Fatalf("BuildPlan = %d steps, %q; want none, %q", len(steps), startStopR3ErrText(err), test.want)
			}
		})
	}
}

// TestServerStartRuntimeDispatchRequiresRuntimeIdentityCapabilities pins that
// a durable plan recorded with a version-1 capability fails definitively
// before the unit start is dispatched.
func TestServerStartRuntimeDispatchRequiresRuntimeIdentityCapabilities(t *testing.T) {
	cases := map[string]string{startVerifyRuntimeStep: startInspectVersionError, startReadinessStep: startReadinessVersionError}
	for stepID, want := range cases {
		t.Run(stepID, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStart(t, f)
			startStopR3SetStep(&operation, stepID, func(step *store.OperationStep) { step.CapabilityVersion = "1" })
			start := operationStep(operation, startRuntimeStep)
			start.Status, start.Attempt, start.Output = protocolstate.StepPending, 0, nil
			if got := startStopR3ErrText(f.start.CheckPreconditions(context.Background(), operation, start)); got != want {
				t.Fatalf("CheckPreconditions = %q; want %q", got, want)
			}
			resolution, err := f.start.HandlePreconditionFailure(context.Background(), operation, start, nil)
			if err != nil || resolution.State != ResolutionDefinitiveError {
				t.Fatalf("HandlePreconditionFailure = %#v, %v; want a definitive error", resolution, err)
			}
			const message = "The selected execution or game capability does not report runtime identity; no runtime start was issued."
			var failure struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(resolution.Error, &failure) != nil || failure.Message != message {
				t.Fatalf("precondition failure = %s; want message %q", resolution.Error, message)
			}
		})
	}
}

// TestServerStartReadinessDispatchRequiresInspectedRuntime pins that game
// readiness is not dispatched when the post-start inspection reported a
// running unit without a runtime_id.
func TestServerStartReadinessDispatchRequiresInspectedRuntime(t *testing.T) {
	f := newStartStopR3Fixture(t)
	operation := startStopR3PlannedStart(t, f)
	startStopR3SetStep(&operation, startVerifyRuntimeStep, startStopR3Succeed(startStopR3RunningUnit(t, "")))
	f.server().LifecycleState = protocolstate.ServerStarting
	readiness := operationStep(operation, startReadinessStep)
	const want = "game readiness requires the runtime inspection to report a runtime_id"
	if got := startStopR3ErrText(f.start.CheckPreconditions(context.Background(), operation, readiness)); got != want {
		t.Fatalf("CheckPreconditions = %q; want %q", got, want)
	}
	startStopR3SetStep(&operation, startVerifyRuntimeStep, startStopR3Succeed(startStopR3RunningUnit(t, startStopR3RuntimeID)))
	if err := f.start.CheckPreconditions(context.Background(), operation, readiness); err != nil {
		t.Fatalf("CheckPreconditions with runtime_id = %v", err)
	}
}
