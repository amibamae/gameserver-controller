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
		return expectedValidationBackupStreams(input)
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

func expectedValidationBackupStreams(input json.RawMessage) ([]ServerBackupStreamExpectation, string, error) {
	var decoded map[string]any
	if err := json.Unmarshal(input, &decoded); err != nil {
		return nil, "", err
	}
	manifest := asStringMap(decoded["manifest"])
	expectations := make([]ServerBackupStreamExpectation, 0, len(asAnySlice(manifest["objects"])))
	for _, rawObject := range asAnySlice(manifest["objects"]) {
		object := asStringMap(rawObject)
		size, err := integerFromAny(object["size_bytes"])
		if err != nil {
			return nil, "", errors.New("validation manifest has an invalid object size")
		}
		dataSetID := stringValueMap(object, "data_set_id")
		checksum := stringValueMap(object, "checksum")
		if dataSetID == "" {
			return nil, "", errors.New("validation manifest has an object without a data set ID")
		}
		expectations = append(expectations, ServerBackupStreamExpectation{DataSetID: dataSetID, SizeBytes: size, MaximumBytes: maxInt64(size, 1), SHA256: checksum})
	}
	return expectations, "controller_to_adapter", nil
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
