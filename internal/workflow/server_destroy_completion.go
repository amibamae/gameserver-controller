package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerDestroyWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.destroy" {
		return errors.New("server.destroy completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return err
	}
	if err := validateDestroyCompletionEvidence(operation, parameters.ExpectedInstanceRef); err != nil {
		return err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	if err := validateServerDestroyTarget(operation, parameters, server.Record); err != nil {
		return err
	}
	if _, err := w.verifiedBackup(ctx, server.Record, parameters); err != nil {
		return err
	}
	return nil
}

func validateDestroyCompletionEvidence(operation store.OperationRecord, instanceRef json.RawMessage) error {
	providerBefore, err := destroyInstanceStepOutput(operation, destroyInspectInstanceStep, protocolstate.StepSucceeded)
	if err != nil || !equalJSON(providerBefore.InstanceRef, instanceRef) || !activeOrStopped(providerBefore.State) {
		return errors.New("server.destroy lacks exact pre-destruction provider evidence")
	}
	if !destroyRuntimeStoppedOrAbsent(operation) {
		return errors.New("server.destroy lacks exact stopped-runtime evidence")
	}
	return validateDestroyProviderAbsence(operation, instanceRef)
}

func validateDestroyProviderAbsence(operation store.OperationRecord, instanceRef json.RawMessage) error {
	destroyed, err := destroyProviderOutput(operation)
	if err != nil || destroyed.Outcome != "completed" || destroyed.State != "destroyed" || !equalJSON(destroyed.InstanceRef, instanceRef) {
		return errors.New("server.destroy lacks an exact completed provider destruction")
	}
	absence, err := destroyInstanceStepOutput(operation, destroyVerifyInstanceStep, protocolstate.StepSucceeded)
	if err != nil || absence.State != "destroyed" || !equalJSON(absence.InstanceRef, instanceRef) {
		return errors.New("server.destroy lacks exact provider-absence verification")
	}
	if err := observationAfter(destroyed.ObservedAt, absence.ObservedAt); err != nil {
		return fmt.Errorf("provider absence observation predates destroy completion: %w", err)
	}
	return nil
}

func (w *ServerDestroyWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	parameters, _ := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"outcome_summary": "The selected backup was verified and the exact provider instance is confirmed absent.",
		"operation_type":  "server.destroy", "affected_resources": []any{parameters.ExpectedInstanceRef},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{},
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false,
		"operation_data": map[string]any{
			"instance_ref": parameters.ExpectedInstanceRef, "verified_backup_id": parameters.VerifiedBackupID,
			"data_generation_id": server.Record.DataGenerationID, "provider_absence_confirmed": true,
			"final_lifecycle_state": string(protocolstate.ServerDestroyed),
		},
	}
	return json.Marshal(result)
}

func (w *ServerDestroyWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := operationStep(operation, destroyProviderStep).Attempt > 0
	affected := []any{}
	if partial && len(operation.Target.InstanceRef) > 0 {
		affected = append(affected, operation.Target.InstanceRef)
	}
	return json.Marshal(map[string]any{
		"outcome_summary": "Server destruction did not confirm provider absence; known effects remain attached to this operation.",
		"operation_type":  "server.destroy", "affected_resources": affected,
		"completed_steps": completedStepSummaries(operation), "failed_steps": failed,
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": partial,
	})
}
