package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	code := "core.safety.backup_precondition_unmet"
	message := "Backup evidence did not establish the exact safe precondition; no next adapter call was issued."
	switch step.StepID {
	case backupStopRuntimeStep:
		if backupSaveRejected(operation) {
			message = "The game adapter rejected the backup save; the runtime was not stopped."
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
	case backupPrepareStep:
		if operationStep(operation, backupStopRuntimeStep).StepID != "" && backupRuntimeStopOutputSucceeded(operation) {
			break
		}
		return Resolution{State: ResolutionNeedsOperator}, nil
	default:
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "capture"); ok && backupPreparationRejected(operation) {
			message = fmt.Sprintf("The game adapter rejected backup preparation for data set %q.", dataSet.DataSetID)
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "upload"); ok && backupCaptureRejected(operation, dataSet.DataSetID) {
			message = fmt.Sprintf("The game adapter rejected capture of data set %q.", dataSet.DataSetID)
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "readback"); ok {
			put, err := backupPutOutput(operation, dataSet.DataSetID)
			if err == nil && put.UploadState == "not_uploaded" {
				message = fmt.Sprintf("Storage confirmed that data set %q was not uploaded.", dataSet.DataSetID)
				return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "provider_failure", message, operation.OperationID, false, "inspect_operation", true)}, nil
			}
		}
		if step.StepID == backupStartRuntimeStep && backupValidationRejected(operation) {
			message = "Game-specific backup validation did not confirm every required check."
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
	}
	return Resolution{State: ResolutionNeedsOperator}, nil
}

func (w *ServerBackupWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || ctx.Err() != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if _, ok := backupDataSetIDFromStep(operation, step.StepID, "readback"); ok {
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch step.StepID {
	case backupInspectInstanceStep, backupInspectRuntimeStep, backupDescribeDataStep, backupValidateStep, backupVerifyRuntimeStep, backupVerifyStoppedStep, backupReadinessStep:
		// These are read-only. Any stream read is reissued with a fresh handle
		// and a new attempt-scoped staging slot before the safe retry.
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case backupStopRuntimeStep, backupStartRuntimeStep:
		return w.reconcileBackupRuntime(ctx, operation, step), nil
	default:
		// Save, prepare, capture, and upload are mutating. The protocol has no
		// read-only status capability that proves an unknown outcome did not
		// take effect, so the workflow never retries these calls blindly.
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerBackupWorkflow) reconcileBackupRuntime(ctx context.Context, operation store.OperationRecord, step store.OperationStep) Resolution {
	result := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	observation, err := w.inspectRuntimeForReconciliation(ctx, operation, step)
	if err != nil {
		return result
	}
	if step.StepID == backupStopRuntimeStep {
		return reconcileBackupStop(result, observation)
	}
	return reconcileBackupStart(result, observation, operation.OperationID)
}

func reconcileBackupStop(result Resolution, observation stopUnitInspection) Resolution {
	switch observation.State {
	case "stopped":
		result.State = ResolutionCompleted
		// The unit reference came from a decoded JSON response. The other fields are strings.
		result.Output, _ = json.Marshal(executionUnitStopResult{UnitRef: observation.UnitRef, Outcome: "completed", State: "stopped", ObservedAt: observation.ObservedAt})
	case "running":
		result.State = ResolutionSafeToRetry
	case "stopping":
		result.State = ResolutionPending
	}
	return result
}

func reconcileBackupStart(result Resolution, observation stopUnitInspection, operationID string) Resolution {
	switch observation.State {
	case "running":
		result.State = ResolutionCompleted
		// The unit reference came from a decoded JSON response. The other fields are strings.
		result.Output, _ = json.Marshal(executionUnitStartResult{UnitRef: observation.UnitRef, Outcome: "completed", State: "running", ObservedAt: observation.ObservedAt})
	case "stopped", "absent":
		result.State = ResolutionSafeToRetry
	case "starting":
		result.State = ResolutionPending
	case "failed":
		result.State = ResolutionDefinitiveError
		result.Error = protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after backup resume was attempted.", operationID, false, "operator_review", true)
	}
	return result
}

func (w *ServerBackupWorkflow) inspectRuntimeForReconciliation(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (stopUnitInspection, error) {
	inspect := operationStep(operation, backupInspectRuntimeStep)
	if step.StepID == backupStartRuntimeStep {
		inspect = operationStep(operation, backupVerifyRuntimeStep)
	}
	if !backupReconciliationInspectionMatches(inspect, step) {
		return stopUnitInspection{}, errors.New("no exact runtime inspection step is available for reconciliation")
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryExecution, inspect.CapabilityName,
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-output", false)
	if err != nil || capability.Version != inspect.CapabilityVersion {
		return stopUnitInspection{}, errors.New("pinned runtime inspection capability is unavailable")
	}
	deadline := time.Now().UTC().Add(15 * time.Second)
	request := backupReconciliationInspectionRequest(operation, step, inspect, capability.Name, deadline)
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := w.adapters.Invoke(callContext, inspect.AdapterID, request)
	cancel()
	if !backupReconciliationInspectionCompleted(response, request.ID, invokeErr) ||
		w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return stopUnitInspection{}, errors.New("runtime inspection did not complete during reconciliation")
	}
	var observation stopUnitInspection
	if json.Unmarshal(response.Result.Output, &observation) != nil || !equalJSON(observation.UnitRef, stepField(step.Input, "unit_ref")) {
		return stopUnitInspection{}, errors.New("runtime reconciliation inspection is not bound to the exact unit")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return stopUnitInspection{}, err
	}
	return observation, nil
}

func backupReconciliationInspectionMatches(inspect, step store.OperationStep) bool {
	if inspect.StepID == "" || inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID {
		return false
	}
	return inspect.CapabilityVersion != "" && stepBindsRef(inspect.Input, "unit_ref", stepField(step.Input, "unit_ref"))
}

func backupReconciliationInspectionRequest(operation store.OperationRecord, step, inspect store.OperationStep, capability string, deadline time.Time) adapter.InvokeRequest {
	requestID := backupReconcileRequestID(operation.OperationID, step.StepID, step.Attempt)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	return adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: operation.ProtocolVersion, Capability: capability,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
			StepID: step.StepID, ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline},
		Input: append(json.RawMessage(nil), effectiveStepInput(inspect)...),
	}}
}

func backupReconciliationInspectionCompleted(response adapter.InvokeResponse, requestID string, err error) bool {
	return err == nil && response.JSONRPC == "2.0" && response.ID == requestID && response.Result.Outcome == adapter.OutcomeCompleted
}

func backupReconcileRequestID(operationID, stepID string, attempt int) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + stepID + "\x00reconcile-backup\x00" + fmt.Sprint(attempt)))
	return "reconcile-" + hex.EncodeToString(digest[:16])
}
