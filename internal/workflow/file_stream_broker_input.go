package workflow

import (
	"errors"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// validateOperationInput checks that a stream step's input is bound to its
// operation and to the trusted stream expectations.
func (b *FileServerBackupTransferBroker) validateOperationInput(
	operation store.OperationRecord, step store.OperationStep, expectations []ServerBackupStreamExpectation,
) error {
	input := decodeObject(effectiveStepInput(step))
	switch step.CapabilityName {
	case "game.backup.capture":
		return validateCaptureStreamInput(input, operation, expectations)
	case "storage.object.put", "storage.object.get":
		return validateStorageStreamInput(input, operation, step.CapabilityName, expectations)
	case "game.backup.validate":
		return validateGameStreamInput(input, operation, expectations)
	default:
		return errors.New("unsupported stream capability")
	}
}

func validateCaptureStreamInput(input map[string]any, operation store.OperationRecord, expectations []ServerBackupStreamExpectation) error {
	if !captureInputBound(input, operation, expectations) {
		return errors.New("capture stream is not bound to the exact operation target and data set")
	}
	return nil
}

func captureInputBound(input map[string]any, operation store.OperationRecord, expectations []ServerBackupStreamExpectation) bool {
	return stringValueMap(input, "backup_id") == operation.OperationID &&
		stringValueMap(input, "data_set_id") == expectations[0].DataSetID &&
		backupTargetMatchesOperation(mustJSON(input["target"]), operation)
}

// validateStorageStreamInput checks the expected size and checksum, then the
// environment of the upload target or readback reference.
func validateStorageStreamInput(
	input map[string]any, operation store.OperationRecord, capability string, expectations []ServerBackupStreamExpectation,
) error {
	if !storageInputBound(input, expectations) {
		return errors.New("storage stream is not bound to the exact expected size and checksum")
	}
	reference, message := "object_ref", "storage readback reference crosses its operation environment"
	if capability == "storage.object.put" {
		reference, message = "target", "storage upload target crosses its operation environment"
	}
	if stringValueMap(asStringMap(input[reference]), "environment_id") != operation.Target.EnvironmentID {
		return errors.New(message)
	}
	return nil
}

func storageInputBound(input map[string]any, expectations []ServerBackupStreamExpectation) bool {
	size, sizeErr := integerFromAny(input["expected_size_bytes"])
	checksum := asStringMap(input["expected_checksum"])
	return sizeErr == nil && size == expectations[0].SizeBytes &&
		stringValueMap(checksum, "algorithm") == "sha256" &&
		stringValueMap(checksum, "digest") == expectations[0].SHA256
}

// validateGameStreamInput checks the operation binding, then that each
// expectation matches the manifest object and stream at the same index.
func validateGameStreamInput(input map[string]any, operation store.OperationRecord, expectations []ServerBackupStreamExpectation) error {
	if stringValueMap(input, "backup_id") != operation.OperationID ||
		stringValueMap(input, "environment_id") != operation.Target.EnvironmentID {
		return errors.New("game validation stream is not bound to its operation and environment")
	}
	objects := asAnySlice(asStringMap(input["manifest"])["objects"])
	streams := asAnySlice(input["object_streams"])
	if len(objects) != len(expectations) || len(streams) != len(expectations) {
		return errors.New("game validation stream count differs from the manifest object count")
	}
	for index, expectation := range expectations {
		if !manifestObjectBound(asStringMap(objects[index]), asStringMap(streams[index]), expectation) {
			return errors.New("game validation stream is not bound to the corresponding exact manifest object")
		}
	}
	return nil
}

func manifestObjectBound(object, stream map[string]any, expectation ServerBackupStreamExpectation) bool {
	size, sizeErr := integerFromAny(object["size_bytes"])
	if sizeErr != nil || size != expectation.SizeBytes {
		return false
	}
	return stringValueMap(object, "data_set_id") == expectation.DataSetID &&
		stringValueMap(object, "checksum") == expectation.SHA256 &&
		equalJSON(mustJSON(stream["object_ref"]), mustJSON(object["object_ref"]))
}
