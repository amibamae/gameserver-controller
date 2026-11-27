package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// startStepCheck rechecks one planned server.start step against the stored
// server and the evidence of the steps before it.
type startStepCheck func(store.OperationRecord, store.OperationStep, store.LogicalServer) error

var startStepChecks = map[string]startStepCheck{
	startInspectInstanceStep: checkStartInspectInstance,
	startRuntimeStep:         checkStartRuntime,
	startVerifyRuntimeStep:   checkStartVerifyRuntime,
	startReadinessStep:       checkStartReadiness,
}

// CheckPreconditions rechecks server identity and all prior-step evidence just
// before each planned dispatch.
func (w *ServerStartWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.start" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.start operation and context are required")
	}
	server, err := loadPreconditionServer(ctx, w.servers, operation)
	if err != nil {
		return err
	}
	check, supported := startStepChecks[step.StepID]
	if !supported {
		return fmt.Errorf("unsupported server.start step %q", step.StepID)
	}
	return check(operation, step, server)
}

func checkStartInspectInstance(_ store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("provider inspection is not bound to the current managed instance")
	}
	if server.LifecycleState != protocolstate.ServerStopped {
		return errors.New("server must remain stopped until the provider instance is confirmed active")
	}
	return nil
}

func checkStartRuntime(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if !startRuntimeAttemptAllowed(step.Attempt, server.LifecycleState) || !stepSucceeded(operation, startInspectInstanceStep) {
		return errors.New("runtime start requires a stopped server or confirmed start retry and a successful provider inspection")
	}
	if !instanceObservedActive(operation, startInspectInstanceStep, server.InstanceRef) {
		return errors.New("runtime start requires the exact provider instance to be active")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
		return errors.New("runtime start and verification must target the same planned unit")
	}
	return nil
}

// startRuntimeAttemptAllowed allows the first start attempt from a stopped
// server and a retry only after the server was confirmed starting.
func startRuntimeAttemptAllowed(attempt int, state protocolstate.ServerState) bool {
	initialAttempt := attempt == 0 && state == protocolstate.ServerStopped
	confirmedRetry := attempt > 0 && state == protocolstate.ServerStarting
	return initialAttempt || confirmedRetry
}

func checkStartVerifyRuntime(operation store.OperationRecord, _ store.OperationStep, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStarting || !unitStartCompleted(operation) {
		return errors.New("runtime inspection requires a completed start command")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
		return errors.New("runtime verification must target the exact started unit")
	}
	return nil
}

func checkStartReadiness(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStarting || !stepSucceeded(operation, startVerifyRuntimeStep) {
		return errors.New("game readiness requires a successful exact-unit runtime inspection")
	}
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || inspection.State != "running" {
		return errors.New("game readiness requires the execution unit to be confirmed running")
	}
	if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") || !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("readiness request must be bound to the verified unit and current instance")
	}
	return nil
}

// HandlePreconditionFailure terminally fails only when a stored, target-bound
// inspection proves the next side effect cannot proceed. Unknown observations
// remain suspended so the controller never guesses about provider state.
func (w *ServerStartWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	message := startPreconditionFailureMessage(operation, step.StepID)
	if message == "" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return preconditionUnmet(operation.OperationID, message), nil
}

// startPreconditionFailureMessage explains a proven-unmet precondition, or
// returns "" when the stored evidence does not prove one.
func startPreconditionFailureMessage(operation store.OperationRecord, stepID string) string {
	switch stepID {
	case startRuntimeStep:
		if startInstanceProvenInactive(operation) {
			return "The exact provider instance is not active; no runtime start was issued."
		}
	case startReadinessStep:
		if startUnitProvenNotRunning(operation) {
			return "The exact execution unit is not confirmed running; game readiness was not checked."
		}
	}
	return ""
}

func startInstanceProvenInactive(operation store.OperationRecord) bool {
	observation, observed := plannedInstanceObservation(operation, startInspectInstanceStep)
	return observed && instanceDefinitivelyInactive(observation.State)
}

func startUnitProvenNotRunning(operation store.OperationRecord) bool {
	observation, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || !equalJSON(observation.UnitRef, stepField(operationStep(operation, startRuntimeStep).Input, "unit_ref")) {
		return false
	}
	switch observation.State {
	case "absent", "stopped", "starting", "stopping", "failed":
		return true
	}
	return false
}
