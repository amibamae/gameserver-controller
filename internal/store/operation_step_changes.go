package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func validateStepChanges(current, next []OperationStep, operationStatus protocolstate.OperationStatus) error {
	if len(next) != len(current) {
		return fmt.Errorf("%w: accepted operation steps cannot be added or removed", ErrImmutableFields)
	}
	for i, previous := range current {
		if err := validateStepChange(next, i, previous, operationStatus); err != nil {
			return err
		}
	}
	return nil
}

// validateStepChange checks step i of next against its previous version:
// recorded fields first, then a newly committed resolved input, then
// recorded outcomes, then the status transition and attempt count.
func validateStepChange(next []OperationStep, i int, previous OperationStep, operationStatus protocolstate.OperationStatus) error {
	candidate := next[i]
	if err := validateRecordedStepFields(previous, candidate); err != nil {
		return err
	}
	if err := validateResolvedInputCommit(next, i, previous, operationStatus); err != nil {
		return err
	}
	if changedOnceRecorded(previous.Output, candidate.Output) {
		return fmt.Errorf("%w: recorded step output cannot change", ErrImmutableFields)
	}
	if changedOnceRecorded(previous.ExternalOperationRef, candidate.ExternalOperationRef) {
		return fmt.Errorf("%w: recorded external operation reference cannot change", ErrImmutableFields)
	}
	return validateStepProgress(previous, candidate, operationStatus)
}

// validateRecordedStepFields rejects a change to a step's identity, recorded
// capability, bindings, input, or recorded resolved input.
func validateRecordedStepFields(previous, candidate OperationStep) error {
	switch {
	case previous.StepID != candidate.StepID || previous.StepType != candidate.StepType:
		return fmt.Errorf("%w: step order and identity cannot change", ErrImmutableFields)
	case !sameRecordedCapability(previous, candidate):
		return fmt.Errorf("%w: recorded adapter capability identity cannot change", ErrImmutableFields)
	case !slices.Equal(previous.InputBindings, candidate.InputBindings):
		return fmt.Errorf("%w: recorded step input bindings cannot change", ErrImmutableFields)
	case !bytes.Equal(previous.Input, candidate.Input):
		return fmt.Errorf("%w: recorded step input cannot change", ErrImmutableFields)
	case changedOnceRecorded(previous.ResolvedInput, candidate.ResolvedInput):
		return fmt.Errorf("%w: resolved step input cannot change", ErrImmutableFields)
	}
	return nil
}

// sameRecordedCapability allows each capability field to be recorded once
// and never changed afterwards.
func sameRecordedCapability(previous, candidate OperationStep) bool {
	return keptOnceRecorded(previous.AdapterID, candidate.AdapterID) &&
		keptOnceRecorded(previous.CapabilityName, candidate.CapabilityName) &&
		keptOnceRecorded(previous.CapabilityVersion, candidate.CapabilityVersion)
}

func keptOnceRecorded(previous, candidate string) bool {
	return previous == "" || previous == candidate
}

func changedOnceRecorded(previous, candidate json.RawMessage) bool {
	return len(previous) > 0 && !bytes.Equal(previous, candidate)
}

// validateResolvedInputCommit checks a resolved input recorded for the first
// time: the bound step must still be pending in a running operation, and the
// input must be what the bindings resolve to from the recorded outputs.
func validateResolvedInputCommit(next []OperationStep, i int, previous OperationStep, operationStatus protocolstate.OperationStatus) error {
	candidate := next[i]
	if len(previous.ResolvedInput) > 0 || len(candidate.ResolvedInput) == 0 {
		return nil
	}
	if !resolvedInputCommitAllowed(previous, candidate, operationStatus) {
		return fmt.Errorf("%w: resolved input must be committed while the bound step is pending", ErrInvalidRecord)
	}
	expected, err := inputbinding.Resolve(inputBindingSteps(next), i)
	if err != nil || !bytes.Equal(expected, candidate.ResolvedInput) {
		return fmt.Errorf("%w: resolved input does not match the immutable bindings and recorded outputs", ErrInvalidRecord)
	}
	return nil
}

func resolvedInputCommitAllowed(previous, candidate OperationStep, operationStatus protocolstate.OperationStatus) bool {
	return boundStepStillPending(previous, candidate) && candidate.Attempt == previous.Attempt && operationStatus == protocolstate.OperationRunning
}

func boundStepStillPending(previous, candidate OperationStep) bool {
	return len(previous.InputBindings) > 0 && previous.Status == protocolstate.StepPending && candidate.Status == protocolstate.StepPending
}

// validateStepProgress checks the step status transition, and that the
// attempt count rises by one exactly when the step enters running, which it
// may do only while the operation is running.
func validateStepProgress(previous, candidate OperationStep, operationStatus protocolstate.OperationStatus) error {
	if previous.Status != candidate.Status {
		if err := protocolstate.ValidateStepTransition(previous.Status, candidate.Status); err != nil {
			return err
		}
	}
	if candidate.Status != protocolstate.StepRunning || previous.Status == protocolstate.StepRunning {
		if candidate.Attempt != previous.Attempt {
			return fmt.Errorf("%w: step attempt can change only when entering running", ErrImmutableFields)
		}
		return nil
	}
	if operationStatus != protocolstate.OperationRunning {
		return fmt.Errorf("%w: a step may start only while the operation is running", ErrInvalidRecord)
	}
	if candidate.Attempt != previous.Attempt+1 {
		return fmt.Errorf("%w: entering running must increment step attempt by one", ErrInvalidRecord)
	}
	return nil
}
