package workflow

import (
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerStopWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if !projectionTargets(operation, "server.stop", current.Record) {
		return store.LogicalServer{}, false, errors.New("server.stop lifecycle projection target mismatch")
	}
	next := current.Record
	saveStep := operationStep(operation, stopSaveGameStep)
	switch current.Record.LifecycleState {
	case protocolstate.ServerReady:
		return w.handleLifecycleReady(next, operation, saveStep)
	case protocolstate.ServerStopping:
		return w.handleLifecycleStopping(next, operation, saveStep)
	}
	return current.Record, false, nil
}

// handleLifecycleReady handles the ready state for server stop.
func (w *ServerStopWorkflow) handleLifecycleReady(next store.LogicalServer, operation store.OperationRecord, saveStep store.OperationStep) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerDegraded
		return next, true, nil
	}
	if saveStep.Status != protocolstate.StepPending {
		next.LifecycleState = protocolstate.ServerStopping
		next.DesiredState = "stopped"
		return next, true, nil
	}
	return next, false, nil
}

// handleLifecycleStopping handles the stopping state for server stop.
func (w *ServerStopWorkflow) handleLifecycleStopping(next store.LogicalServer, operation store.OperationRecord, saveStep store.OperationStep) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationSucceeded {
		if !hasValidStopResult(operation.Result, operation.Target, next.InstanceRef, w.schemas) {
			return store.LogicalServer{}, false, errors.New("terminal server.stop result lacks exact save and termination evidence")
		}
		next.LifecycleState = protocolstate.ServerStopped
		next.DesiredState = "stopped"
		return next, true, nil
	}
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	return next, false, nil
}

func hasValidStopResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result lifecycleResultDocument
	if json.Unmarshal(raw, &result) != nil || !resultNamesTarget(result, "server.stop", protocolstate.ServerStopped, target) {
		return false
	}
	return resultBindsInstance(result.OperationData.InstanceRef, target.InstanceRef, instanceRef) &&
		schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-stop-result", raw) == nil
}
