package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// stopStepCheck rechecks one planned server.stop step against the stored
// server and the evidence of the steps before it.
type stopStepCheck func(store.OperationRecord, store.LogicalServer) error

var stopStepChecks = map[string]stopStepCheck{
	stopInspectInstanceStep: checkStopInspectInstance,
	stopInspectUnitStep:     checkStopInspectUnit,
	stopSaveGameStep:        checkStopSaveGame,
	stopRuntimeStep:         checkStopRuntime,
}

func (w *ServerStopWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.stop" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.stop operation and context are required")
	}
	server, err := loadPreconditionServer(ctx, w.servers, operation)
	if err != nil {
		return err
	}
	if !stepBindsRef(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref", server.InstanceRef) {
		return errors.New("server.stop plan is not bound to the current managed instance")
	}
	check, supported := stopStepChecks[step.StepID]
	if !supported {
		return fmt.Errorf("unsupported server.stop step %q", step.StepID)
	}
	return check(operation, server)
}

func checkStopInspectInstance(_ store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady {
		return errors.New("server must remain ready until the exact provider instance is inspected")
	}
	return nil
}

func checkStopInspectUnit(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectInstanceStep) {
		return errors.New("runtime inspection requires the ready server and successful provider inspection")
	}
	if !instanceObservedActive(operation, stopInspectInstanceStep, server.InstanceRef) {
		return errors.New("runtime inspection requires the exact provider instance to be active")
	}
	return nil
}

func checkStopSaveGame(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectUnitStep) {
		return errors.New("game save requires a ready server and successful runtime inspection")
	}
	if !instanceObservedActive(operation, stopInspectInstanceStep, server.InstanceRef) {
		return errors.New("game save requires the exact provider instance to remain active")
	}
	if !stopUnitObservedRunning(operation) {
		return errors.New("game save requires the exact execution unit to be running")
	}
	return nil
}

func checkStopRuntime(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStopping || !stepSucceeded(operation, stopSaveGameStep) || !gameSaveConfirmed(operation) {
		return errors.New("graceful stop requires a confirmed game save and stopping lifecycle state")
	}
	if !sameStopStepReference(operation, stopInspectUnitStep, stopRuntimeStep, "unit_ref") {
		return errors.New("graceful stop must target the exact inspected execution unit")
	}
	return nil
}

func (w *ServerStopWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	message, proven := stopPreconditionFailure(operation, step.StepID)
	if !proven {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return preconditionUnmet(operation.OperationID, message), nil
}

// stopPreconditionFailure reports whether stored, target-bound evidence
// proves the step's precondition unmet, and the message to record.
func stopPreconditionFailure(operation store.OperationRecord, stepID string) (string, bool) {
	switch stepID {
	case stopInspectUnitStep, stopSaveGameStep:
		return stopInactiveTargetFailure(operation, stepID)
	case stopRuntimeStep:
		if gameSaveRejected(operation) {
			return "The game adapter rejected its shutdown save; the execution unit was not stopped.", true
		}
	}
	return "", false
}

// stopInactiveTargetFailure proves that the provider instance, or for the
// game save the execution unit, cannot serve the next stop step.
func stopInactiveTargetFailure(operation store.OperationRecord, stepID string) (string, bool) {
	observation, observed := plannedInstanceObservation(operation, stopInspectInstanceStep)
	if !observed {
		return "", false
	}
	if observation.State != "active" {
		if instanceDefinitivelyInactive(observation.State) {
			return "The exact provider instance is not active; no game-save or runtime-stop call was issued.", true
		}
		return "", false
	}
	if stepID != stopSaveGameStep {
		// Kept verbatim: an active instance before runtime inspection has
		// always been recorded as a terminal failure with an empty message.
		return "", true
	}
	return stopUnitNotRunningFailure(operation)
}

func stopUnitNotRunningFailure(operation store.OperationRecord) (string, bool) {
	runtime, err := stopUnitInspectOutput(operation, stopInspectUnitStep)
	if err != nil || !equalJSON(runtime.UnitRef, stopUnitRef(operation)) {
		return "", false
	}
	switch runtime.State {
	case "absent", "stopped", "failed":
		return "The exact execution unit is not running; no game-save or stop call was issued.", true
	}
	return "", false
}
