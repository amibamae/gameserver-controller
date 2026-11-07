package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type adapterRuntime interface {
	ProtocolVersion() string
	Descriptor(string) (adapter.Descriptor, error)
	Invoke(context.Context, string, adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

type schemaRuntime interface {
	ProtocolVersion() string
	HasSchema(string) bool
	ValidateJSON(string, []byte) error
}

// Engine advances one persisted step at a time. A step is marked running in
// the store before Invoke is called. Interrupted calls become outcome_unknown
// and require operation-specific reconciliation before retry.
type Engine struct {
	store            store.OperationStore
	adapters         adapterRuntime
	schemas          schemaRuntime
	definitions      *Definitions
	resumeAuthorizer ResumeAuthorizer
	stepTimeout      time.Duration
	maxStepAttempts  int
	activeMu         sync.Mutex
	activeOperations map[string]bool
}

func NewEngine(operationStore store.OperationStore, adapters adapterRuntime, schemas schemaRuntime, definitions *Definitions, resumeAuthorizer ResumeAuthorizer, stepTimeout time.Duration, maxStepAttempts int) (*Engine, error) {
	if operationStore == nil || adapters == nil || schemas == nil || definitions == nil || resumeAuthorizer == nil {
		return nil, errors.New("operation store, adapter registry, schemas, workflow definitions, and resume authorizer are required")
	}
	if schemas.ProtocolVersion() == "" || stepTimeout <= 0 || stepTimeout > 24*time.Hour || maxStepAttempts < 1 || maxStepAttempts > 10 {
		return nil, errors.New("protocol version, step timeout (1ns..24h), and maximum step attempts (1..10) are required")
	}
	return &Engine{
		store: operationStore, adapters: adapters, schemas: schemas, definitions: definitions,
		resumeAuthorizer: resumeAuthorizer, stepTimeout: stepTimeout, maxStepAttempts: maxStepAttempts,
		activeOperations: make(map[string]bool),
	}, nil
}

// Advance advances one durable workflow boundary. An uncertain step is
// reconciled before it can be retried; dispatch after reconciliation is allowed
// only when the definition explicitly reports that retry is safe.
func (e *Engine) Advance(ctx context.Context, operationID string) (store.StoredOperation, error) {
	if ctx == nil || operationID == "" {
		return store.StoredOperation{}, errors.New("context and operation ID are required")
	}
	if !e.begin(operationID) {
		return store.StoredOperation{}, ErrOperationInProgress
	}
	defer e.end(operationID)
	return e.advanceLocked(ctx, operationID)
}

// Run repeatedly advances synchronous steps until the operation completes,
// waits for an external effect, or requires operator intervention.
func (e *Engine) Run(ctx context.Context, operationID string) (store.StoredOperation, error) {
	if ctx == nil || operationID == "" {
		return store.StoredOperation{}, errors.New("context and operation ID are required")
	}
	stored, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	limit := e.maxStepAttempts*len(stored.Record.Steps) + len(stored.Record.Steps) + 4
	if limit < 8 {
		limit = 8
	}
	for i := 0; i < limit; i++ {
		stored, err = e.Advance(ctx, operationID)
		if err != nil {
			return stored, err
		}
		if protocolstate.IsTerminalOperation(stored.Record.Status) ||
			stored.Record.Status == protocolstate.OperationWaiting ||
			stored.Record.Status == protocolstate.OperationNeedsOperator {
			return stored, nil
		}
		if err := ctx.Err(); err != nil {
			return stored, err
		}
	}
	return e.suspend(stored, "core.workflow.transition_limit", "Workflow advancement exceeded its bounded transition count.")
}

// Recover marks a step that was still running at process shutdown as
// outcome_unknown. It makes no adapter calls; callers may later Run each
// returned recoverable operation to reconcile or resume it.
func (e *Engine) Recover(ctx context.Context) ([]store.StoredOperation, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	operations, err := e.store.ListRecoverableOperations()
	if err != nil {
		return nil, err
	}
	result := make([]store.StoredOperation, 0, len(operations))
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.begin(operation.Record.OperationID) {
			continue
		}
		updated, updateErr := e.recoverOne(operation)
		e.end(operation.Record.OperationID)
		if updateErr != nil {
			return nil, updateErr
		}
		result = append(result, updated)
	}
	return result, nil
}

// Resume leaves needs_operator only after the injected authorizer approves
// this exact durable operation. It resumes at reconciliation; it never
// replays a possibly dispatched step directly.
func (e *Engine) Resume(ctx context.Context, authenticatedOperatorID, operationID string) (store.StoredOperation, error) {
	if ctx == nil || authenticatedOperatorID == "" || operationID == "" {
		return store.StoredOperation{}, errors.New("context, authenticated operator ID, and operation ID are required")
	}
	if !e.begin(operationID) {
		return store.StoredOperation{}, ErrOperationInProgress
	}
	defer e.end(operationID)
	current, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	if current.Record.Status != protocolstate.OperationNeedsOperator {
		return current, ErrNotRecoverable
	}
	if err := e.resumeAuthorizer.AuthorizeResume(ctx, authenticatedOperatorID, current.Record); err != nil {
		return current, fmt.Errorf("authorize operation resume: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	next := current.Record
	next.Status = protocolstate.OperationReconciling
	next.Error = nil
	return e.store.UpdateOperation(current.Revision, next)
}

func (e *Engine) advanceLocked(ctx context.Context, operationID string) (store.StoredOperation, error) {
	current, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	if protocolstate.IsTerminalOperation(current.Record.Status) {
		return current, nil
	}
	if current.Record.Status == protocolstate.OperationNeedsOperator {
		return current, ErrNeedsOperator
	}
	definition, ok := e.definitions.get(current.Record.OperationType)
	if !ok {
		return current, fmt.Errorf("%w: %s", ErrNotRecoverable, current.Record.OperationType)
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}

	if current.Record.Status == protocolstate.OperationPending {
		next := current.Record
		next.Status = protocolstate.OperationRunning
		current, err = e.store.UpdateOperation(current.Revision, next)
		if err != nil {
			return store.StoredOperation{}, err
		}
	}
	if current.Record.ProtocolVersion != e.schemas.ProtocolVersion() || current.Record.ProtocolVersion != e.adapters.ProtocolVersion() {
		return e.suspend(current, "core.compatibility.protocol_version_unavailable", "The operation's recorded protocol version is not loaded by this controller.")
	}
	if !stepOrderValid(current.Record) {
		return e.suspend(current, "core.workflow.invalid_step_order", "The persisted workflow steps are not in a safe sequential order.")
	}

	if current.Record.Status == protocolstate.OperationWaiting || current.Record.Status == protocolstate.OperationReconciling {
		stepIndex := activeStepIndex(current.Record)
		if stepIndex < 0 {
			if hasPendingSteps(current.Record) {
				next := current.Record
				next.Status = protocolstate.OperationRunning
				next.Error = nil
				next.CurrentStepID = nil
				current, err = e.store.UpdateOperation(current.Revision, next)
				if err != nil {
					return store.StoredOperation{}, err
				}
			} else if allStepsComplete(current.Record) {
				next := current.Record
				next.Status = protocolstate.OperationRunning
				next.CurrentStepID = nil
				current, err = e.store.UpdateOperation(current.Revision, next)
				if err != nil {
					return store.StoredOperation{}, err
				}
			} else {
				return e.finishSuccess(ctx, current, definition)
			}
		} else {
			step := current.Record.Steps[stepIndex]
			if step.Status == protocolstate.StepPending {
				next := current.Record
				next.Status = protocolstate.OperationRunning
				next.Error = nil
				current, err = e.store.UpdateOperation(current.Revision, next)
				if err != nil {
					return store.StoredOperation{}, err
				}
			} else {
				return e.reconcile(ctx, current, stepIndex, definition)
			}
		}
	}
	if current.Record.Status != protocolstate.OperationRunning {
		return current, ErrNotRecoverable
	}

	stepIndex := activeStepIndex(current.Record)
	if stepIndex >= 0 {
		step := current.Record.Steps[stepIndex]
		switch step.Status {
		case protocolstate.StepRunning:
			unknown := current.Record
			unknown.Status = protocolstate.OperationReconciling
			unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
			unknown.Steps[stepIndex].Error = protocolError("core.adapter.outcome_unknown", "internal", "The previous adapter invocation has no recorded response.", current.Record.OperationID, false, "reconcile", false)
			unknown.Error = json.RawMessage(unknown.Steps[stepIndex].Error)
			current, err = e.store.UpdateOperation(current.Revision, unknown)
			if err != nil {
				return store.StoredOperation{}, err
			}
			return e.reconcile(ctx, current, stepIndex, definition)
		case protocolstate.StepOutcomeUnknown, protocolstate.StepWaiting:
			return e.reconcile(ctx, current, stepIndex, definition)
		case protocolstate.StepSucceeded:
			current, err = e.clearCurrentStep(current)
			if err != nil {
				return store.StoredOperation{}, err
			}
		case protocolstate.StepFailed, protocolstate.StepSkipped, protocolstate.StepCancelled:
			return current, ErrNotRecoverable
		}
	}
	if current.Record.Status != protocolstate.OperationRunning {
		return current, ErrNotRecoverable
	}
	stepIndex = firstPendingStep(current.Record)
	if stepIndex < 0 {
		if !allStepsComplete(current.Record) {
			return e.suspend(current, "core.workflow.incomplete_steps", "The workflow contains unresolved steps without a current dispatch target.")
		}
		return e.finishSuccess(ctx, current, definition)
	}
	if ctx.Err() != nil {
		return current, ctx.Err()
	}

	step := current.Record.Steps[stepIndex]
	capability, err := e.preflight(current.Record, step)
	if err != nil {
		return e.suspend(current, "core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.")
	}
	if err := definition.CheckPreconditions(ctx, current.Record, step); err != nil {
		return e.suspend(current, "core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.")
	}
	if ctx.Err() != nil {
		return current, ctx.Err()
	}
	deadline := e.stepDeadline(ctx, current.Record)
	if !deadline.After(time.Now()) {
		return e.failBeforeDispatch(ctx, current, stepIndex, definition, "core.operation.deadline_exceeded", "The operation deadline expired before adapter dispatch.", "timeout")
	}

	claimed := current.Record
	claimed.Steps[stepIndex].Status = protocolstate.StepRunning
	claimed.Steps[stepIndex].Attempt++
	currentStepID := claimed.Steps[stepIndex].StepID
	claimed.CurrentStepID = &currentStepID
	current, err = e.store.UpdateOperation(current.Revision, claimed)
	if err != nil {
		return store.StoredOperation{}, err
	}
	return e.dispatch(ctx, current, stepIndex, definition, capability)
}

func (e *Engine) dispatch(ctx context.Context, running store.StoredOperation, stepIndex int, definition Definition, capability adapter.Capability) (store.StoredOperation, error) {
	step := running.Record.Steps[stepIndex]
	deadline := e.stepDeadline(ctx, running.Record)
	if !deadline.After(time.Now()) {
		failed := running.Record
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		errValue := protocolError("core.operation.deadline_exceeded", "timeout", "The operation deadline expired before adapter dispatch.", running.Record.OperationID, false, "inspect_operation", true)
		failed.Steps[stepIndex].Error = errValue
		return e.fail(ctx, running, failed, stepIndex, definition, errValue)
	}

	requestID := adapterRequestID(running.Record.OperationID, step.StepID, step.Attempt)
	correlationID := running.Record.CorrelationID
	if correlationID == "" {
		correlationID = running.Record.OperationID
	}
	request := adapter.InvokeRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: running.Record.ProtocolVersion,
			Capability:      step.CapabilityName,
			RequestContext: adapter.RequestContext{
				RequestID: requestID, CorrelationID: correlationID,
				OperationID: running.Record.OperationID, StepID: step.StepID,
				ServerID: running.Record.Target.ServerID, EnvironmentID: running.Record.Target.EnvironmentID,
				Deadline: deadline,
			},
			Input: append(json.RawMessage(nil), step.Input...),
		},
	}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := e.adapters.Invoke(callContext, step.AdapterID, request)
	cancel()
	if invokeErr != nil {
		var ambiguous *adapter.InvocationError
		if errors.As(invokeErr, &ambiguous) && ambiguous.MayHaveSideEffects {
			unknown := running.Record
			unknown.Status = protocolstate.OperationReconciling
			unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
			unknownError := protocolError("core.adapter.outcome_unknown", "internal", "The adapter call may have taken effect but returned no definitive result.", running.Record.OperationID, false, "reconcile", false)
			unknown.Steps[stepIndex].Error = unknownError
			unknown.Error = json.RawMessage(unknownError)
			return e.store.UpdateOperation(running.Revision, unknown)
		}
		failure := protocolError("core.adapter.invocation_failed", "provider_failure", "The adapter call failed before a side effect was confirmed.", running.Record.OperationID, false, "inspect_operation", true)
		failed := running.Record
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		failed.Steps[stepIndex].Error = failure
		return e.fail(ctx, running, failed, stepIndex, definition, failure)
	}
	encodedResponse, encodeErr := json.Marshal(response)
	responseSchemaID := "urn:gameserver-controller:" + e.schemas.ProtocolVersion() + ":rpc:adapter-invoke-result"
	if encodeErr != nil || response.JSONRPC != "2.0" || response.ID != requestID || response.Result.ObservedAt.IsZero() ||
		e.schemas.ValidateJSON(responseSchemaID, encodedResponse) != nil ||
		e.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		unknown := running.Record
		unknown.Status = protocolstate.OperationReconciling
		unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
		unknown.Steps[stepIndex].Error = protocolError("core.adapter.invalid_response", "internal", "The adapter returned a malformed response after dispatch.", running.Record.OperationID, false, "reconcile", false)
		unknown.Error = json.RawMessage(unknown.Steps[stepIndex].Error)
		return e.store.UpdateOperation(running.Revision, unknown)
	}
	if response.Result.Outcome != adapter.OutcomeCompleted && response.Result.Outcome != adapter.OutcomeAccepted && response.Result.Outcome != adapter.OutcomeUnknown {
		unknown := running.Record
		unknown.Status = protocolstate.OperationReconciling
		unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
		unknown.Steps[stepIndex].Error = protocolError("core.adapter.invalid_outcome", "internal", "The adapter returned an unsupported outcome after dispatch.", running.Record.OperationID, false, "reconcile", false)
		unknown.Error = json.RawMessage(unknown.Steps[stepIndex].Error)
		return e.store.UpdateOperation(running.Revision, unknown)
	}

	updated := running.Record
	updated.Error = nil
	updated.CurrentStepID = nil
	updated.Steps[stepIndex].Error = nil
	if len(response.Result.ExternalOperationRef) > 0 {
		if len(updated.Steps[stepIndex].ExternalOperationRef) > 0 && !equalJSON(updated.Steps[stepIndex].ExternalOperationRef, response.Result.ExternalOperationRef) {
			unknown := running.Record
			unknown.Status = protocolstate.OperationReconciling
			unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
			unknown.Steps[stepIndex].Error = protocolError("core.adapter.external_reference_changed", "internal", "The adapter returned a different external operation reference for a recorded step.", running.Record.OperationID, false, "reconcile", false)
			unknown.Error = json.RawMessage(unknown.Steps[stepIndex].Error)
			return e.store.UpdateOperation(running.Revision, unknown)
		}
		updated.Steps[stepIndex].ExternalOperationRef = append(json.RawMessage(nil), response.Result.ExternalOperationRef...)
	}
	switch response.Result.Outcome {
	case adapter.OutcomeCompleted:
		updated.Steps[stepIndex].Status = protocolstate.StepSucceeded
		updated.Steps[stepIndex].Output = append(json.RawMessage(nil), response.Result.Output...)
		return e.store.UpdateOperation(running.Revision, updated)
	case adapter.OutcomeAccepted:
		updated.Status = protocolstate.OperationWaiting
		updated.Steps[stepIndex].Status = protocolstate.StepWaiting
		return e.store.UpdateOperation(running.Revision, updated)
	case adapter.OutcomeUnknown:
		updated.Status = protocolstate.OperationReconciling
		updated.CurrentStepID = stringPointer(step.StepID)
		updated.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
		unknownError := protocolError("core.adapter.outcome_unknown", "internal", "The adapter reported that the external outcome is unknown.", running.Record.OperationID, false, "reconcile", false)
		updated.Steps[stepIndex].Error = unknownError
		updated.Error = json.RawMessage(unknownError)
		return e.store.UpdateOperation(running.Revision, updated)
	default:
		failure := protocolError("core.adapter.invalid_outcome", "internal", "The adapter returned an unsupported outcome value.", running.Record.OperationID, false, "inspect_operation", true)
		failed := running.Record
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		failed.Steps[stepIndex].Error = failure
		return e.fail(ctx, running, failed, stepIndex, definition, failure)
	}
}

func (e *Engine) reconcile(ctx context.Context, current store.StoredOperation, stepIndex int, definition Definition) (store.StoredOperation, error) {
	step := current.Record.Steps[stepIndex]
	if step.Status == protocolstate.StepRunning {
		unknown := current.Record
		unknown.Status = protocolstate.OperationReconciling
		unknown.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
		unknownError := protocolError("core.adapter.outcome_unknown", "internal", "The previous adapter invocation has no recorded response.", current.Record.OperationID, false, "reconcile", false)
		unknown.Steps[stepIndex].Error = unknownError
		unknown.Error = json.RawMessage(unknownError)
		var err error
		current, err = e.store.UpdateOperation(current.Revision, unknown)
		if err != nil {
			return store.StoredOperation{}, err
		}
		step = current.Record.Steps[stepIndex]
	}
	capability, err := e.preflight(current.Record, step)
	if err != nil {
		return e.suspend(current, "core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.")
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	reconcileContext, cancel := context.WithTimeout(ctx, e.stepTimeout)
	resolution, err := definition.Reconcile(reconcileContext, current.Record, step)
	cancel()
	if err != nil {
		return e.suspend(current, "core.reconciliation.unavailable", "The external outcome could not be reconciled automatically.")
	}
	if err := e.validateExternalOperationRef(step, resolution.ExternalOperationRef); err != nil {
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an invalid or conflicting external operation reference.")
	}
	if len(resolution.ExternalOperationRef) > 0 && len(step.ExternalOperationRef) == 0 {
		updated := current.Record
		updated.Steps[stepIndex].ExternalOperationRef = append(json.RawMessage(nil), resolution.ExternalOperationRef...)
		current, err = e.store.UpdateOperation(current.Revision, updated)
		if err != nil {
			return store.StoredOperation{}, err
		}
		step = current.Record.Steps[stepIndex]
	}
	next := current.Record
	next.Error = nil
	switch resolution.State {
	case ResolutionCompleted:
		if !isObject(resolution.Output) {
			return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation did not provide a valid completed output.")
		}
		if err := e.schemas.ValidateJSON(capability.OutputSchemaURI, resolution.Output); err != nil {
			return e.suspend(current, "core.reconciliation.invalid_result", "Reconciled output failed the recorded capability schema.")
		}
		if current.Record.Status != protocolstate.OperationRunning {
			next.Status = protocolstate.OperationRunning
		}
		next.Steps[stepIndex].Status = protocolstate.StepSucceeded
		next.Steps[stepIndex].Output = append(json.RawMessage(nil), resolution.Output...)
		next.Steps[stepIndex].Error = nil
		next.CurrentStepID = nil
		return e.store.UpdateOperation(current.Revision, next)
	case ResolutionPending:
		next.Status = protocolstate.OperationWaiting
		next.Steps[stepIndex].Status = protocolstate.StepWaiting
		next.Steps[stepIndex].Error = nil
		return e.store.UpdateOperation(current.Revision, next)
	case ResolutionSafeToRetry:
		if step.Attempt >= e.maxStepAttempts {
			return e.suspend(current, "core.operation.retry_limit", "The safe retry limit for this step has been reached.")
		}
		if err := definition.CheckPreconditions(ctx, current.Record, step); err != nil {
			return e.suspend(current, "core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.")
		}
		next.Status = protocolstate.OperationRunning
		next.Steps[stepIndex].Status = protocolstate.StepRunning
		next.Steps[stepIndex].Attempt++
		next.Steps[stepIndex].Error = nil
		next.CurrentStepID = stringPointer(step.StepID)
		running, err := e.store.UpdateOperation(current.Revision, next)
		if err != nil {
			return store.StoredOperation{}, err
		}
		return e.dispatch(ctx, running, stepIndex, definition, capability)
	case ResolutionDefinitiveError:
		if !validProtocolError(e.schemas, resolution.Error) {
			return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an invalid error record.")
		}
		failed := current.Record
		failed.Status = protocolstate.OperationFailed
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		failed.Steps[stepIndex].Error = append(json.RawMessage(nil), resolution.Error...)
		failed.Error = append(json.RawMessage(nil), resolution.Error...)
		return e.fail(ctx, current, failed, stepIndex, definition, resolution.Error)
	case ResolutionNeedsOperator:
		return e.suspend(current, "core.reconciliation.operator_required", "Automatic reconciliation cannot safely determine the external outcome.")
	default:
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an unsupported resolution state.")
	}
}

func (e *Engine) finishSuccess(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, error) {
	if !allStepsComplete(current.Record) {
		return e.suspend(current, "core.workflow.incomplete_steps", "The workflow cannot succeed while any step remains unresolved.")
	}
	result, err := definition.BuildSuccessResult(ctx, current.Record)
	if err != nil || !validOperationResult(e.schemas, current.Record, result, true) {
		return e.suspend(current, "core.workflow.result_invalid", "The workflow could not produce a schema-valid success result.")
	}
	next := current.Record
	next.Status = protocolstate.OperationSucceeded
	next.Error = nil
	next.Result = append(json.RawMessage(nil), result...)
	next.CurrentStepID = nil
	return e.store.UpdateOperation(current.Revision, next)
}

func (e *Engine) failBeforeDispatch(ctx context.Context, current store.StoredOperation, stepIndex int, definition Definition, code, message, category string) (store.StoredOperation, error) {
	errValue := protocolError(code, category, message, current.Record.OperationID, false, "inspect_operation", false)
	failed := current.Record
	failed.Status = protocolstate.OperationFailed
	failed.Error = errValue
	failed.CurrentStepID = nil
	return e.fail(ctx, current, failed, stepIndex, definition, errValue)
}

func (e *Engine) fail(ctx context.Context, current store.StoredOperation, failed store.OperationRecord, stepIndex int, definition Definition, failure json.RawMessage) (store.StoredOperation, error) {
	failed.Status = protocolstate.OperationFailed
	if stepIndex >= 0 && stepIndex < len(failed.Steps) && failed.Steps[stepIndex].Status != protocolstate.StepPending {
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		failed.Steps[stepIndex].Error = append(json.RawMessage(nil), failure...)
	}
	failed.Error = append(json.RawMessage(nil), failure...)
	failed.CurrentStepID = nil
	var failedStep store.OperationStep
	if stepIndex >= 0 && stepIndex < len(failed.Steps) {
		failedStep = failed.Steps[stepIndex]
	}
	if partial, err := definition.BuildFailureResult(ctx, failed, failedStep, failure); err == nil && validOperationResult(e.schemas, failed, partial, false) {
		failed.Result = append(json.RawMessage(nil), partial...)
	} else {
		failed.Result = nil
	}
	return e.store.UpdateOperation(current.Revision, failed)
}

func (e *Engine) suspend(current store.StoredOperation, code, message string) (store.StoredOperation, error) {
	if current.Record.Status == protocolstate.OperationNeedsOperator {
		return current, ErrNeedsOperator
	}
	next := current.Record
	if next.Status == protocolstate.OperationPending {
		return current, fmt.Errorf("%w: pending operation cannot be suspended without starting", ErrNotRecoverable)
	}
	next.Status = protocolstate.OperationNeedsOperator
	next.Error = protocolError(code, "safety_blocked", message, next.OperationID, false, "operator_review", true)
	for i := range next.Steps {
		if next.Steps[i].Status == protocolstate.StepRunning {
			next.Steps[i].Status = protocolstate.StepOutcomeUnknown
			next.Steps[i].Error = append(json.RawMessage(nil), next.Error...)
		}
	}
	return e.store.UpdateOperation(current.Revision, next)
}

func (e *Engine) preflight(record store.OperationRecord, step store.OperationStep) (adapter.Capability, error) {
	if record.ProtocolVersion != e.adapters.ProtocolVersion() || record.ProtocolVersion != e.schemas.ProtocolVersion() {
		return adapter.Capability{}, errors.New("operation protocol version differs from adapter registry")
	}
	if step.AdapterID == "" || step.CapabilityName == "" || step.CapabilityVersion == "" || !isObject(step.Input) {
		return adapter.Capability{}, errors.New("step does not contain a complete adapter invocation plan")
	}
	descriptor, err := e.adapters.Descriptor(step.AdapterID)
	if err != nil || descriptor.AdapterID != step.AdapterID {
		return adapter.Capability{}, errors.New("recorded adapter is unavailable")
	}
	if !containsProtocol(descriptor.ProtocolVersions, record.ProtocolVersion) {
		return adapter.Capability{}, errors.New("recorded adapter does not support operation protocol version")
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Name != step.CapabilityName {
			continue
		}
		if capability.Version != step.CapabilityVersion {
			return adapter.Capability{}, errors.New("recorded capability version is unavailable")
		}
		if !e.schemas.HasSchema(capability.InputSchemaURI) || !e.schemas.HasSchema(capability.OutputSchemaURI) {
			return adapter.Capability{}, errors.New("recorded capability schemas are unavailable")
		}
		if err := e.schemas.ValidateJSON(capability.InputSchemaURI, step.Input); err != nil {
			return adapter.Capability{}, fmt.Errorf("step input does not match recorded capability: %w", err)
		}
		return capability, nil
	}
	return adapter.Capability{}, errors.New("recorded capability is unavailable")
}

func (e *Engine) validateExternalOperationRef(step store.OperationStep, value json.RawMessage) error {
	if len(value) == 0 {
		return nil
	}
	schemaID := "urn:gameserver-controller:" + e.schemas.ProtocolVersion() + ":common:external-resource-ref"
	if !isObject(value) || e.schemas.ValidateJSON(schemaID, value) != nil {
		return errors.New("external operation reference does not match protocol schema")
	}
	if len(step.ExternalOperationRef) > 0 && !equalJSON(step.ExternalOperationRef, value) {
		return errors.New("external operation reference conflicts with the recorded reference")
	}
	return nil
}

func (e *Engine) stepDeadline(ctx context.Context, record store.OperationRecord) time.Time {
	deadline := time.Now().Add(e.stepTimeout)
	if record.Deadline != nil && record.Deadline.Before(deadline) {
		deadline = *record.Deadline
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return deadline
}

func (e *Engine) clearCurrentStep(current store.StoredOperation) (store.StoredOperation, error) {
	next := current.Record
	next.CurrentStepID = nil
	return e.store.UpdateOperation(current.Revision, next)
}

func (e *Engine) recoverOne(current store.StoredOperation) (store.StoredOperation, error) {
	if current.Record.Status != protocolstate.OperationRunning && current.Record.Status != protocolstate.OperationWaiting && current.Record.Status != protocolstate.OperationReconciling {
		return current, nil
	}
	index := currentStepIndex(current.Record)
	if index < 0 || current.Record.Steps[index].Status != protocolstate.StepRunning {
		return current, nil
	}
	next := current.Record
	next.Status = protocolstate.OperationReconciling
	next.Steps[index].Status = protocolstate.StepOutcomeUnknown
	unknownError := protocolError("core.adapter.outcome_unknown", "internal", "The controller restarted while this adapter step was running.", current.Record.OperationID, false, "reconcile", false)
	next.Steps[index].Error = unknownError
	next.Error = json.RawMessage(unknownError)
	return e.store.UpdateOperation(current.Revision, next)
}

func (e *Engine) begin(operationID string) bool {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	if e.activeOperations[operationID] {
		return false
	}
	e.activeOperations[operationID] = true
	return true
}

func (e *Engine) end(operationID string) {
	e.activeMu.Lock()
	delete(e.activeOperations, operationID)
	e.activeMu.Unlock()
}

func currentStepIndex(record store.OperationRecord) int {
	if record.CurrentStepID == nil {
		return -1
	}
	for index := range record.Steps {
		if record.Steps[index].StepID == *record.CurrentStepID {
			return index
		}
	}
	return -1
}

func activeStepIndex(record store.OperationRecord) int {
	if record.CurrentStepID != nil {
		for index := range record.Steps {
			if record.Steps[index].StepID == *record.CurrentStepID {
				switch record.Steps[index].Status {
				case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
					return index
				}
			}
		}
	}
	for index := range record.Steps {
		switch record.Steps[index].Status {
		case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
			return index
		}
	}
	return -1
}

func firstPendingStep(record store.OperationRecord) int {
	for index := range record.Steps {
		switch record.Steps[index].Status {
		case protocolstate.StepPending:
			return index
		case protocolstate.StepSucceeded, protocolstate.StepSkipped:
			continue
		default:
			return -1
		}
	}
	return -1
}

func hasPendingSteps(record store.OperationRecord) bool { return firstPendingStep(record) >= 0 }

func allStepsComplete(record store.OperationRecord) bool {
	for _, step := range record.Steps {
		if step.Status != protocolstate.StepSucceeded && step.Status != protocolstate.StepSkipped {
			return false
		}
	}
	return true
}

func stepOrderValid(record store.OperationRecord) bool {
	incompleteSeen := false
	activeCount := 0
	for _, step := range record.Steps {
		switch step.Status {
		case protocolstate.StepSucceeded, protocolstate.StepSkipped:
			if incompleteSeen {
				return false
			}
		case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
			if incompleteSeen {
				return false
			}
			activeCount++
			incompleteSeen = true
		case protocolstate.StepPending, protocolstate.StepFailed, protocolstate.StepCancelled:
			incompleteSeen = true
		default:
			return false
		}
	}
	return activeCount <= 1
}

func containsProtocol(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func equalJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return string(left) == string(right)
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func adapterRequestID(operationID, stepID string, attempt int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", operationID, stepID, attempt)))
	return "step-" + hex.EncodeToString(sum[:16])
}

func protocolError(code, category, message, operationID string, retryable bool, recoveryAction string, operatorRequired bool) json.RawMessage {
	value := map[string]any{
		"code": code, "category": category, "message": message, "retryable": retryable,
		"operation_id": operationID,
		"recovery":     map[string]any{"action": recoveryAction, "operator_required": operatorRequired},
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func validProtocolError(schemas schemaRuntime, value json.RawMessage) bool {
	return isObject(value) && schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":common:protocol-error", value) == nil
}

func validOperationResult(schemas schemaRuntime, record store.OperationRecord, value json.RawMessage, success bool) bool {
	if !isObject(value) {
		return false
	}
	var header struct {
		OperationType string `json:"operation_type"`
	}
	if json.Unmarshal(value, &header) != nil || header.OperationType != record.OperationType {
		return false
	}
	schemaID := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:operation-result"
	if success {
		typedID := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:" + strings.ReplaceAll(record.OperationType, ".", "-") + "-result"
		if schemas.HasSchema(typedID) {
			schemaID = typedID
		}
	}
	return schemas.ValidateJSON(schemaID, value) == nil
}

func isObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func stringPointer(value string) *string { return &value }
