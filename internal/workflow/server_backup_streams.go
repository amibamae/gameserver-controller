package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) StreamTransfersForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep, deadline time.Time) ([]adapter.StreamTransfer, error) {
	if ctx == nil || w == nil || w.transfers == nil {
		return nil, errors.New("initialized backup workflow and stream context are required")
	}
	expectations, direction, err := w.expectedStreamTransfers(ctx, operation, step)
	if err != nil {
		return nil, err
	}
	if len(expectations) == 0 {
		return nil, nil
	}
	stepContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	transfers, err := w.transfers.IssueStepStreamTransfers(stepContext, operation, step, expectations, direction)
	if err != nil {
		return nil, err
	}
	if len(transfers) != len(expectations) {
		return nil, errors.New("stream broker did not issue exactly one transfer for every planned stream")
	}
	if deadline.IsZero() || !deadline.After(time.Now()) {
		return nil, errors.New("stream transfer issuance has no live invocation deadline")
	}
	bounds := backupStreamBounds{direction: direction, deadline: deadline, now: time.Now().UTC()}
	if err := w.validateIssuedBackupStreams(transfers, expectations, bounds); err != nil {
		return nil, err
	}
	return transfers, nil
}

type backupStreamBounds struct {
	direction string
	deadline  time.Time
	now       time.Time
}

func (w *ServerBackupWorkflow) validateIssuedBackupStreams(transfers []adapter.StreamTransfer, expectations []ServerBackupStreamExpectation, bounds backupStreamBounds) error {
	seen := make(map[string]struct{}, len(transfers))
	for i := range transfers {
		transfer := &transfers[i]
		encoded, err := json.Marshal(transfer.Reference)
		if err != nil || w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":storage:storage-transfer-reference", encoded) != nil {
			return errors.New("stream broker issued a handle that does not match the registered schema")
		}
		if !bounds.accepts(transfer.Reference, expectations[i].MaximumBytes) {
			return errors.New("stream broker issued a handle with an unsafe direction, bound, or expiry")
		}
		if _, duplicate := seen[transfer.Reference.TransferID]; duplicate {
			return errors.New("stream broker reused a transfer ID within one invocation")
		}
		seen[transfer.Reference.TransferID] = struct{}{}
		if !backupTransferDirectionBound(transfer, bounds.direction) {
			return errors.New("stream broker did not bind its handle to exactly one correct-direction stream")
		}
	}
	return nil
}

func (bounds backupStreamBounds) accepts(reference capturestream.Reference, maximumBytes int64) bool {
	if reference.Direction != bounds.direction || reference.MaximumBytes != maximumBytes || reference.ExpiresAt.IsZero() {
		return false
	}
	return reference.ExpiresAt.After(bounds.now) && !reference.ExpiresAt.After(bounds.deadline) &&
		!reference.ExpiresAt.After(bounds.now.Add(5*time.Minute))
}

func backupTransferDirectionBound(transfer *adapter.StreamTransfer, direction string) bool {
	if transfer.Verify == nil {
		return false
	}
	switch direction {
	case "controller_to_adapter":
		return transfer.Source != nil && transfer.Sink == nil
	case "adapter_to_controller":
		return transfer.Sink != nil && transfer.Source == nil
	default:
		return true
	}
}

func (w *ServerBackupWorkflow) expectedStreamTransfers(ctx context.Context, operation store.OperationRecord, step store.OperationStep) ([]ServerBackupStreamExpectation, string, error) {
	input := effectiveStepInput(step)
	switch step.CapabilityName {
	case "game.backup.capture":
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, "capture")
		if !ok {
			return nil, "", errors.New("capture stream is outside the trusted backup plan")
		}
		return []ServerBackupStreamExpectation{{DataSetID: dataSet.DataSetID, SizeBytes: -1, MaximumBytes: dataSet.MaximumBytes}}, "adapter_to_controller", nil
	case "storage.object.put", "storage.object.get":
		return w.expectedStorageBackupStream(ctx, operation, step, input)
	case "game.backup.validate":
		return expectedValidationBackupStreams(step.Input, input)
	default:
		return nil, "", nil
	}
}

func (w *ServerBackupWorkflow) expectedStorageBackupStream(ctx context.Context, operation store.OperationRecord, step store.OperationStep, input json.RawMessage) ([]ServerBackupStreamExpectation, string, error) {
	action := "upload"
	if step.CapabilityName == "storage.object.get" {
		action = "readback"
	}
	dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, action)
	if !ok {
		return nil, "", errors.New("storage stream is outside the trusted backup plan")
	}
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		return nil, "", err
	}
	size, err := integerFromAny(decoded["expected_size_bytes"])
	if err != nil {
		return nil, "", errors.New("storage stream input has no exact expected size")
	}
	checksum := asStringMap(decoded["expected_checksum"])
	direction := "controller_to_adapter"
	if step.CapabilityName == "storage.object.get" {
		direction = "adapter_to_controller"
	}
	return []ServerBackupStreamExpectation{{DataSetID: dataSet.DataSetID, SizeBytes: size, MaximumBytes: maxInt64(size, 1), SHA256: stringValueMap(checksum, "digest")}}, direction, nil
}

// expectedValidationBackupStreams derives the data-set IDs from the accepted
// immutable plan, not from the external schema-valid manifest. Dataset hints
// must never be emitted into the game adapter's backup-manifest payload.
func expectedValidationBackupStreams(planned, input json.RawMessage) ([]ServerBackupStreamExpectation, string, error) {
	var decoded, accepted map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(planned, &accepted); err != nil {
		return nil, "", errors.New("accepted validation plan is invalid")
	}
	manifest := asStringMap(decoded["manifest"])
	plannedManifest := asStringMap(accepted["manifest"])
	objects := asAnySlice(manifest["objects"])
	plannedObjects := asAnySlice(plannedManifest["objects"])
	if len(objects) != len(plannedObjects) {
		return nil, "", errors.New("validation manifest object count differs from accepted plan")
	}
	expectations := make([]ServerBackupStreamExpectation, 0, len(objects))
	for i, raw := range objects {
		expectation, err := validationObjectExpectation(raw, plannedObjects[i])
		if err != nil {
			return nil, "", err
		}
		expectations = append(expectations, expectation)
	}
	return expectations, "controller_to_adapter", nil
}

func validationObjectExpectation(raw, planned any) (ServerBackupStreamExpectation, error) {
	object := asStringMap(raw)
	size, err := integerFromAny(object["size_bytes"])
	if err != nil {
		return ServerBackupStreamExpectation{}, errors.New("validation manifest has an invalid object size")
	}
	dataSetID := stringValueMap(asStringMap(planned), "data_set_id")
	if dataSetID == "" {
		return ServerBackupStreamExpectation{}, errors.New("accepted validation plan has an object without a data set ID")
	}
	if _, hint := object["data_set_id"]; hint {
		return ServerBackupStreamExpectation{}, errors.New("validation manifest contains a planner-only data set ID")
	}
	if len(asStringMap(object["object_ref"])) == 0 {
		return ServerBackupStreamExpectation{}, errors.New("validation manifest has no exact object reference")
	}
	checksum := stringValueMap(object, "checksum")
	return ServerBackupStreamExpectation{
		DataSetID: dataSetID, SizeBytes: size, MaximumBytes: maxInt64(size, 1), SHA256: checksum,
	}, nil
}

func (w *ServerBackupWorkflow) verifyStreams(ctx context.Context, operation store.OperationRecord, stepID string, expectations []ServerBackupStreamExpectation) error {
	step := operationStep(operation, stepID)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return errors.New("completed stream step is missing")
	}
	if err := w.transfers.VerifyStepStreams(ctx, operation, step, expectations); err != nil {
		return fmt.Errorf("controller-side stream verification failed for %s: %w", stepID, err)
	}
	return nil
}
