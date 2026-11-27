package workflow

import (
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// ProjectLifecycle derives start's durable states only from immutable steps.
// The ready transition occurs atomically with the terminal success result.
func (w *ServerStartWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if !projectionTargets(operation, "server.start", current.Record) {
		return store.LogicalServer{}, false, errors.New("server.start lifecycle projection target mismatch")
	}
	next := current.Record
	switch current.Record.LifecycleState {
	case protocolstate.ServerStopped:
		return projectStartFromStopped(next, operation)
	case protocolstate.ServerStarting:
		return projectStartFromStarting(next, operation)
	case protocolstate.ServerValidating:
		return w.projectStartFromValidating(next, operation)
	}
	return current.Record, false, nil
}

// projectStartFromStopped moves a stopped server to starting once the start
// step has left pending.
func projectStartFromStopped(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	if operationStep(operation, startRuntimeStep).Status == protocolstate.StepPending {
		return next, false, nil
	}
	next.LifecycleState = protocolstate.ServerStarting
	next.DesiredState = "running"
	return next, true, nil
}

// projectStartFromStarting fails a starting server when the operation failed
// or the unit was observed failed, and moves it to validating once the
// readiness step has left pending.
func projectStartFromStarting(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	unitFailed, err := startVerificationObservedFailure(operation)
	if err != nil {
		return store.LogicalServer{}, false, err
	}
	if unitFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	if operationStep(operation, startReadinessStep).Status == protocolstate.StepPending {
		return next, false, nil
	}
	next.LifecycleState = protocolstate.ServerValidating
	next.DesiredState = "running"
	return next, true, nil
}

// startVerificationObservedFailure reports whether a succeeded runtime
// inspection observed the unit failed.
func startVerificationObservedFailure(operation store.OperationRecord) (bool, error) {
	verifyStep := operationStep(operation, startVerifyRuntimeStep)
	if verifyStep.Status != protocolstate.StepSucceeded {
		return false, nil
	}
	var observed executionUnitInspection
	if json.Unmarshal(verifyStep.Output, &observed) != nil {
		return false, errors.New("execution unit verification output cannot be decoded")
	}
	return observed.State == "failed", nil
}

// projectStartFromValidating makes a validating server ready only with an
// exact terminal success result, and degrades it when the operation failed.
func (w *ServerStartWorkflow) projectStartFromValidating(next store.LogicalServer, operation store.OperationRecord) (store.LogicalServer, bool, error) {
	return projectTerminal(next, operation.Status, terminalProjection{
		validResult: func() bool {
			return hasValidStartResult(operation.Result, operation.Target, next.InstanceRef, w.schemas)
		},
		invalidResult: "terminal server.start result lacks its exact readiness assertion",
		succeeded:     protocolstate.ServerReady, desired: "running", failed: protocolstate.ServerDegraded,
	})
}

func hasValidStartResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result lifecycleResultDocument
	if json.Unmarshal(raw, &result) != nil || !resultNamesTarget(result, "server.start", protocolstate.ServerReady, target) ||
		!isObject(result.OperationData.Assertion) {
		return false
	}
	if !resultBindsInstance(result.OperationData.InstanceRef, target.InstanceRef, instanceRef) {
		return false
	}
	return schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", result.OperationData.Assertion) == nil
}
