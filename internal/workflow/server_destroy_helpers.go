package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// destroyInstanceStepOutput extracts a fresh instance inspection from the exact step output.
func destroyInstanceStepOutput(operation store.OperationRecord, stepID string, requiredStatus protocolstate.StepStatus) (serverDestroyInstanceInspection, error) {
	var output serverDestroyInstanceInspection
	step := operationStep(operation, stepID)
	if step.StepID == "" || step.Status != requiredStatus || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyInstanceInspection{}, errors.New("provider instance inspection output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyInstanceInspection{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyInstanceInspection{}, errors.New("provider observation is stale or outside the tolerated clock window")
	}
	return output, nil
}

// destroyRuntimeStepOutput extracts a fresh runtime inspection from the exact step output.
func destroyRuntimeStepOutput(operation store.OperationRecord) (serverDestroyUnitInspection, error) {
	var output serverDestroyUnitInspection
	step := operationStep(operation, destroyInspectRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyUnitInspection{}, errors.New("runtime inspection output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyUnitInspection{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyUnitInspection{}, errors.New("runtime observation is stale or outside the tolerated clock window")
	}
	return output, nil
}

// destroyProviderOutput extracts a fresh provider destruction from the exact step output.
func destroyProviderOutput(operation store.OperationRecord) (serverDestroyOutput, error) {
	var output serverDestroyOutput
	step := operationStep(operation, destroyProviderStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyOutput{}, errors.New("provider destruction output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyOutput{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyOutput{}, errors.New("provider destroy result is stale or outside the tolerated clock window")
	}
	return output, nil
}

// observationIsFreshForStep reports whether the timestamp is within the provider's tolerated drift.
func observationIsFreshForStep(value string, step store.OperationStep) bool {
	observed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || step.StartedAt == nil {
		return false
	}
	// Permit small provider clock drift, but reject evidence that could predate
	// the inspection or implausibly claim to come from the future.
	const toleratedClockSkew = 30 * time.Second
	if observed.Before(step.StartedAt.Add(-toleratedClockSkew)) || observed.After(time.Now().UTC().Add(5*time.Minute)) {
		return false
	}
	return true
}

// observationAfter compares two timestamps and reports an error if the first precedes the second.
func observationAfter(before, after string) error {
	first, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		return err
	}
	second, err := time.Parse(time.RFC3339Nano, after)
	if err != nil {
		return err
	}
	if second.Before(first) {
		return errors.New("observation timestamp moved backwards")
	}
	return nil
}

// activeOrStopped reports true if the resource is live or shutdown cleanly.
func activeOrStopped(state string) bool {
	return state == "active" || state == "stopped"
}

// stoppedOrFailedLifecycle reports true if the server has reached a terminal non-active state.
func stoppedOrFailedLifecycle(state protocolstate.ServerState) bool {
	return state == protocolstate.ServerStopped || state == protocolstate.ServerFailed
}

// equalJSON performs byte-level comparison of JSON values, suitable for evidence and references.
func equalJSON(left, right []byte) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// operationStep loads the exact step record for a given stepID in an operation.
func operationStep(operation store.OperationRecord, stepID string) store.OperationStep {
	for _, step := range operation.Steps {
		if step.StepID == stepID {
			return step
		}
	}
	return store.OperationStep{}
}

// stepField extracts a field value from a step's Input JSON by key name.
func stepField(step store.OperationStep, key string) any {
	var extracted any
	if err := json.Unmarshal(step.Input, &extracted); err != nil {
		return nil
	}
	switch v := extracted.(type) {
	case map[string]json.RawMessage:
		raw := v[key]
		if len(raw) == 0 {
			return ""
		}
		var decoded any
		json.Unmarshal(raw, &decoded)
		return decoded
	default:
		return nil
	}
}

// stepSucceeded reports whether the given step is known to have succeeded.
func stepSucceeded(operation store.OperationRecord, stepID string) bool {
	step := operationStep(operation, stepID)
	return step.StepID != "" && step.Status == protocolstate.StepSucceeded
}

// stepBindsRef reports whether a step's Input carries an exact instance_ref binding the target.
func stepBindsRef(input json.RawMessage, refKey string, instanceRef json.RawMessage) bool {
	var extracted any
	if err := json.Unmarshal(input, &extracted); err != nil {
		return false
	}
	switch v := extracted.(type) {
	case map[string]json.RawMessage:
		raw := v[refKey]
		if len(raw) == 0 {
			return false
		}
		var decoded json.RawMessage
		json.Unmarshal(raw, &decoded)
		return equalJSON(decoded, instanceRef)
	default:
		return false
	}
}

// serverDestroyRequestID generates a unique request ID for provider reconciliation calls.
func serverDestroyRequestID(operationID, stepID string, attempt int, label string) string {
	return fmt.Sprintf("destroy-%s-%s-%d", operationID, stepID, attempt)
}

// isObject reports whether the raw value contains an object structure.
func isObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch raw[0] {
	case '{', '[':
		return true
	default:
		return false
	}
}

// externalInstanceIdentity parses an instance reference to extract the provider adapter and instance IDs.
func externalInstanceIdentity(raw json.RawMessage) (string, string, error) {
	var ref struct {
		AdapterID    string `json:"adapter_id"`
		ResourceKind string `json:"resource_kind"`
		ExternalID   string `json:"external_id"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", "", errors.New("managed VM capacity requires an exact provider instance reference")
	}
	if !isObject(raw) || !exactInstanceReference(ref) {
		return "", "", errors.New("managed VM capacity requires an exact provider instance reference")
	}
	return ref.AdapterID, ref.ExternalID, nil
}

// exactInstanceReference reports whether a parsed instance reference names an
// instance owned by a specific adapter.
func exactInstanceReference(ref struct {
	AdapterID    string `json:"adapter_id"`
	ResourceKind string `json:"resource_kind"`
	ExternalID   string `json:"external_id"`
}) bool {
	return ref.AdapterID != "" && ref.ResourceKind == "instance" && ref.ExternalID != ""
}

// destroyedCapacityState reports whether a reservation already reflects a
// destroyed or absent provider instance.
func destroyedCapacityState(reservation store.CapacityReservation) bool {
	return reservation.State == store.CapacityAbsent || reservation.State == store.CapacityDestroyed
}

// destroyedReservationBindsInstance reports whether a destroyed or absent
// reservation identifies the exact server instance, allowing it to carry no
// adapter identity.
func destroyedReservationBindsInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	if !destroyedCapacityState(reservation) {
		return false
	}
	return reservation.ProviderInstanceID == instanceID &&
		(reservation.ProviderAdapterID == "" || reservation.ProviderAdapterID == instanceAdapterID)
}

// reservationIdentifiesServerInstance reports whether an active reservation
// carries the adapter and instance identity of the server being destroyed.
// An active reservation may omit the adapter only when it identifies no
// provider at all.
func reservationIdentifiesServerInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	adapterKnown := reservation.ProviderAdapterID != ""
	adapterMatches := !adapterKnown || reservation.ProviderAdapterID == instanceAdapterID
	instanceMatches := reservation.ProviderInstanceID == instanceID
	return adapterMatches && instanceMatches
}

// reservationMismatchError explains why an active reservation is not the one
// for this server.
func reservationMismatchError(reservation store.CapacityReservation, instanceAdapterID, instanceID string) error {
	if reservation.ProviderAdapterID != "" && reservation.ProviderAdapterID != instanceAdapterID {
		return errors.New("managed VM reservation belongs to a different hosting adapter")
	}
	return errors.New("managed VM reservation does not identify the exact server instance")
}

// completedStepSummaries collects summaries of every step that reached a terminal state.
func completedStepSummaries(operation store.OperationRecord) []any {
	summaries := make([]any, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		if step.Status == protocolstate.StepSucceeded || step.Status == protocolstate.StepFailed {
			summaries = append(summaries, map[string]any{
				"step_id":   step.StepID,
				"attempt":   step.Attempt,
				"status":    step.Status,
				"type":      step.StepType,
			})
		}
	}
	return summaries
}
