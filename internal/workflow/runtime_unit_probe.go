package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// unitProbeTimeout bounds the read-only inspection that reconciles an
// ambiguous execution-unit command.
const unitProbeTimeout = 15 * time.Second

// unitProbe is the read-only execution.unit.inspect call that resolves an
// ambiguous unit command. server.start and server.stop both reconcile this way.
type unitProbe struct {
	operation store.OperationRecord
	// step is the ambiguous step being reconciled.
	step store.OperationStep
	// inspect is the planned inspection step whose pinned input is replayed.
	inspect store.OperationStep
	// unitRef is the exact unit the observation must name.
	unitRef   json.RawMessage
	requestID string
}

// pinnedUnitInspect reports whether inspect is a planned execution.unit.inspect
// step on the ambiguous step's adapter with a pinned capability version.
func pinnedUnitInspect(inspect, step store.OperationStep) bool {
	return inspect.StepID != "" && inspect.CapabilityName == "execution.unit.inspect" &&
		inspect.AdapterID == step.AdapterID && inspect.CapabilityVersion != ""
}

// observeUnit runs the probe against the exact pinned capability. It returns
// the observation only when the adapter completed the call, the output
// matches its schema, names the exact unit and has a valid observation time.
// Any other answer returns false so the caller asks for an operator.
func observeUnit(ctx context.Context, adapters adapterRuntime, schemas schemaRuntime, probe unitProbe) (executionUnitInspection, bool) {
	version := schemas.ProtocolVersion()
	capability, err := exactCapability(adapters, probe.inspect.AdapterID, adapter.CategoryExecution, probe.inspect.CapabilityName,
		capabilitySchemaURI(version, "execution:systemd-unit-inspect", "-input"),
		capabilitySchemaURI(version, "execution:systemd-unit-inspect", "-output"), false)
	if err != nil || capability.Version != probe.inspect.CapabilityVersion {
		return executionUnitInspection{}, false
	}
	response, err := invokeUnitProbe(ctx, adapters, capability.Name, probe)
	if err != nil || !completedProbeResponse(response, probe.requestID) {
		return executionUnitInspection{}, false
	}
	if err := schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return executionUnitInspection{}, false
	}
	return decodeUnitObservation(response.Result.Output, probe.unitRef)
}

func invokeUnitProbe(ctx context.Context, adapters adapterRuntime, capabilityName string, probe unitProbe) (adapter.InvokeResponse, error) {
	deadline := time.Now().UTC().Add(unitProbeTimeout)
	request := unitProbeRequest(probe, capabilityName, deadline)
	callContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return adapters.Invoke(callContext, probe.inspect.AdapterID, request)
}

func unitProbeRequest(probe unitProbe, capabilityName string, deadline time.Time) adapter.InvokeRequest {
	operation := probe.operation
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	return adapter.InvokeRequest{
		JSONRPC: "2.0", ID: probe.requestID, Method: "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: operation.ProtocolVersion, Capability: capabilityName,
			RequestContext: adapter.RequestContext{
				RequestID: probe.requestID, CorrelationID: correlation, OperationID: operation.OperationID,
				StepID: probe.step.StepID, ServerID: operation.Target.ServerID,
				EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline,
			},
			Input: append(json.RawMessage(nil), probe.inspect.Input...),
		},
	}
}

func completedProbeResponse(response adapter.InvokeResponse, requestID string) bool {
	return response.JSONRPC == "2.0" && response.ID == requestID && response.Result.Outcome == adapter.OutcomeCompleted
}

func decodeUnitObservation(output, unitRef json.RawMessage) (executionUnitInspection, bool) {
	var observation executionUnitInspection
	if err := json.Unmarshal(output, &observation); err != nil || !equalJSON(observation.UnitRef, unitRef) {
		return executionUnitInspection{}, false
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return executionUnitInspection{}, false
	}
	return observation, true
}

// reconcileRequestID derives a stable request ID for one reconciliation
// attempt; salt keeps workflows from sharing IDs.
func reconcileRequestID(operationID, stepID, salt string, attempt int) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + stepID + "\x00" + salt + "\x00" + fmt.Sprint(attempt)))
	return "reconcile-" + hex.EncodeToString(digest[:16])
}
