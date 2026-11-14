package workflow

import (
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	server := current.Record
	if operation.OperationType != "server.backup" || server.ServerID != operation.Target.ServerID ||
		server.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, false, errors.New("server.backup lifecycle projection target mismatch")
	}
	switch server.LifecycleState {
	case protocolstate.ServerReady, protocolstate.ServerDegraded:
		return projectBackupStopClaim(operationStep(operation, backupStopRuntimeStep), server)
	case protocolstate.ServerStopped:
		return projectBackupPrepareClaim(operationStep(operation, backupPrepareStep), server)
	case protocolstate.ServerStopping:
		return projectBackupStopping(operation, server)
	case protocolstate.ServerBackingUp:
		return w.projectBackupCapture(operation, server)
	case protocolstate.ServerStarting:
		return projectBackupStarting(operation, server)
	case protocolstate.ServerValidating:
		return w.projectBackupValidating(operation, server)
	default:
		return server, false, nil
	}
}

func projectBackupStopClaim(stop store.OperationStep, server store.LogicalServer) (store.LogicalServer, bool, error) {
	if stop.StepID == "" || stop.Status == protocolstate.StepPending {
		return server, false, nil
	}
	server.LifecycleState = protocolstate.ServerStopping
	server.DesiredState = "stopped"
	return server, true, nil
}

func projectBackupPrepareClaim(prepare store.OperationStep, server store.LogicalServer) (store.LogicalServer, bool, error) {
	if prepare.Status == protocolstate.StepPending {
		return server, false, nil
	}
	server.LifecycleState = protocolstate.ServerBackingUp
	server.DesiredState = "stopped"
	return server, true, nil
}

func projectBackupStopping(operation store.OperationRecord, next store.LogicalServer) (store.LogicalServer, bool, error) {
	stop := operationStep(operation, backupStopRuntimeStep)
	if stop.Status == protocolstate.StepSucceeded {
		if _, err := backupRuntimeStopOutput(operation); err != nil {
			return store.LogicalServer{}, false, errors.New("backup stop step lacks exact stopped-unit evidence")
		}
		next.LifecycleState = protocolstate.ServerBackingUp
		next.DesiredState = "stopped"
		return next, true, nil
	}
	if backupOperationUnsuccessful(operation.Status) {
		next.LifecycleState = protocolstate.ServerFailed
		next.DesiredState = "stopped"
		return next, true, nil
	}
	return next, false, nil
}

func (w *ServerBackupWorkflow) projectBackupCapture(operation store.OperationRecord, next store.LogicalServer) (store.LogicalServer, bool, error) {
	start := operationStep(operation, backupStartRuntimeStep)
	if start.StepID != "" && start.Status != protocolstate.StepPending {
		next.LifecycleState = protocolstate.ServerStarting
		next.DesiredState = "running"
		next.DataGenerationID = backupNextGenerationID(operation.OperationID, next.DataGenerationID)
		next.DataGenerationStatus = "unknown"
		return next, true, nil
	}
	if operation.Status == protocolstate.OperationSucceeded {
		if backupResumeAfterBackup(operation) {
			return store.LogicalServer{}, false, errors.New("resumed backup cannot succeed before lifecycle enters starting")
		}
		if !hasValidBackupResult(operation.Result, operation, next, w.schemas, protocolstate.ServerStopped) {
			return store.LogicalServer{}, false, errors.New("terminal server.backup result lacks exact stopped-state manifest evidence")
		}
		next.LifecycleState = protocolstate.ServerStopped
		next.DesiredState = "stopped"
		return next, true, nil
	}
	if backupOperationUnsuccessful(operation.Status) {
		if start.Attempt > 0 || backupFinalStopUncertain(operation) {
			next.LifecycleState = protocolstate.ServerFailed
			next.DataGenerationStatus = "unknown"
		} else {
			next.LifecycleState = protocolstate.ServerStopped
		}
		next.DesiredState = "stopped"
		return next, true, nil
	}
	return next, false, nil
}

func projectBackupStarting(operation store.OperationRecord, next store.LogicalServer) (store.LogicalServer, bool, error) {
	readiness := operationStep(operation, backupReadinessStep)
	if readiness.Status != protocolstate.StepPending {
		next.LifecycleState = protocolstate.ServerValidating
		next.DesiredState = "running"
		return next, true, nil
	}
	if backupOperationUnsuccessful(operation.Status) {
		next.LifecycleState = protocolstate.ServerFailed
		next.DataGenerationStatus = "unknown"
		return next, true, nil
	}
	return next, false, nil
}

func (w *ServerBackupWorkflow) projectBackupValidating(operation store.OperationRecord, next store.LogicalServer) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationSucceeded {
		if !hasValidBackupResult(operation.Result, operation, next, w.schemas, protocolstate.ServerReady) {
			return store.LogicalServer{}, false, errors.New("terminal server.backup result lacks exact ready-state manifest and readiness evidence")
		}
		next.LifecycleState = protocolstate.ServerReady
		next.DesiredState = "running"
		next.DataGenerationStatus = "known"
		return next, true, nil
	}
	if backupOperationUnsuccessful(operation.Status) {
		next.LifecycleState = protocolstate.ServerDegraded
		next.DataGenerationStatus = "unknown"
		return next, true, nil
	}
	return next, false, nil
}

func backupOperationUnsuccessful(status protocolstate.OperationStatus) bool {
	return status == protocolstate.OperationFailed || status == protocolstate.OperationCancelled
}

func backupFinalStopUncertain(operation store.OperationRecord) bool {
	return operationStep(operation, backupVerifyStoppedStep).Attempt > 0 && !backupFinalRuntimeStopped(operation)
}
