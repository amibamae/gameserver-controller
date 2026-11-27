package captureinvoke

import (
	"bytes"
	"encoding/json"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/gowebpki/jcs"
)

// This file holds the durable-record checks shared by capture dispatch and
// sealed-capture recovery.

// captureSchema returns the loaded protocol's backup-capture schema ID for
// "input" or "output".
func (r *Runtime) captureSchema(kind string) string {
	return "urn:gameserver-controller:" + r.schemas.ProtocolVersion() + ":game:backup-capture-" + kind
}

// advertisesCapture reports whether the adapter still advertises the step's
// capability name and version with the loaded protocol's capture schemas.
func (r *Runtime) advertisesCapture(descriptor adapter.Descriptor, step store.OperationStep) bool {
	for _, capability := range descriptor.Capabilities {
		if capability.Name == step.CapabilityName && capability.Version == step.CapabilityVersion &&
			capability.InputSchemaURI == r.captureSchema("input") && capability.OutputSchemaURI == r.captureSchema("output") {
			return true
		}
	}
	return false
}

// holdsServerLock reports whether operationID holds the logical-server lock.
func (r *Runtime) holdsServerLock(operationID, environmentID, serverID string) bool {
	scope := store.LogicalServerLockScope(environmentID, serverID)
	lock, err := r.records.GetResourceLock(scope)
	return err == nil && lock.OperationID == operationID && lock.Scope == scope
}

// backupInState reports whether op is a backup of protocolVersion, with its
// initial lifecycle state recorded, in the given status.
func backupInState(op store.OperationRecord, status protocolstate.OperationStatus, protocolVersion string) bool {
	return op.OperationType == "server.backup" && op.Status == status && op.BackupInitialLifecycleState != "" &&
		op.ProtocolVersion == protocolVersion
}

// atStep reports whether op's current step is stepID.
func atStep(op store.OperationRecord, stepID string) bool {
	return op.CurrentStepID != nil && *op.CurrentStepID == stepID
}

// dispatchedCaptureStep reports whether step is a backup capture in status
// that has been dispatched at least once.
func dispatchedCaptureStep(step *store.OperationStep, status protocolstate.StepStatus) bool {
	return step != nil && step.Status == status && step.Attempt >= 1 && step.CapabilityName == "game.backup.capture"
}

func findStep(op store.OperationRecord, stepID string) *store.OperationStep {
	for i := range op.Steps {
		if op.Steps[i].StepID == stepID {
			return &op.Steps[i]
		}
	}
	return nil
}

// captureTargets reports whether input targets serverID in environmentID and
// exactly instanceRef.
func captureTargets(input capture, serverID, environmentID string, instanceRef json.RawMessage) bool {
	return input.Target.ServerID == serverID && input.Target.EnvironmentID == environmentID &&
		sameJSON(input.Target.InstanceRef, instanceRef)
}

// captureInstanceRef is the hosting instance a capture reads from.
type captureInstanceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	Kind          string `json:"resource_kind"`
	ID            string `json:"external_id"`
}

// captureInstance decodes the capture target's instance reference and requires
// an instance in the given environment.
func captureInstance(ref json.RawMessage, environmentID string) (captureInstanceRef, bool) {
	var instance captureInstanceRef
	if json.Unmarshal(ref, &instance) != nil || instance.EnvironmentID != environmentID || instance.Kind != "instance" {
		return captureInstanceRef{}, false
	}
	return instance, true
}

// captureStepRef names the durable step, adapter and logical server that a
// capture stream is bound to.
type captureStepRef struct {
	operationID   string
	stepID        string
	adapterID     string
	serverID      string
	environmentID string
}

// captureScope is the stream scope that binds one capture to its operation,
// step, adapter, target instance, data generation, data set, and capture ID.
func captureScope(step captureStepRef, instance captureInstanceRef, input capture) capturestream.Scope {
	return capturestream.Scope{OperationID: step.operationID, StepID: step.stepID, AdapterID: step.adapterID, ServerID: step.serverID,
		EnvironmentID: step.environmentID, InstanceAdapterID: instance.AdapterID, InstanceID: instance.ID, DataGenerationID: input.GenerationID,
		DataSetID: input.DataSetID, CaptureID: input.CaptureID}
}

// measuredAs reports whether output reports the receipt's size and sha256
// digest.
func measuredAs(output result, receipt capturestream.Receipt) bool {
	return output.Size == receipt.SizeBytes && output.Checksum.Algorithm == "sha256" && output.Checksum.Digest == receipt.SHA256
}

// sameCapture reports whether a result names exactly the requested capture.
// A capture that cannot be encoded matches nothing.
func sameCapture(requested, reported capture) bool {
	a, err := json.Marshal(requested)
	if err != nil {
		return false
	}
	b, err := json.Marshal(reported)
	if err != nil {
		return false
	}
	return sameJSON(a, b)
}

func sameJSON(a, b []byte) bool {
	left, e1 := jcs.Transform(a)
	right, e2 := jcs.Transform(b)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}
