package workflow

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type backupPreparationObservation struct {
	Target              json.RawMessage `json:"target"`
	ReleaseID           string          `json:"release_id"`
	BackupID            string          `json:"backup_id"`
	DataGenerationID    string          `json:"data_generation_id"`
	DataSetIDs          []string        `json:"data_set_ids"`
	ConsistencyMode     string          `json:"consistency_mode"`
	Outcome             string          `json:"outcome"`
	PreparationID       string          `json:"preparation_id"`
	ConsistencyEvidence string          `json:"consistency_evidence"`
	ObservedAt          string          `json:"observed_at"`
}

type backupCaptureObservation struct {
	Target           json.RawMessage `json:"target"`
	ReleaseID        string          `json:"release_id"`
	BackupID         string          `json:"backup_id"`
	DataGenerationID string          `json:"data_generation_id"`
	PreparationID    string          `json:"preparation_id"`
	DataSetID        string          `json:"data_set_id"`
	CaptureID        string          `json:"capture_id"`
	ConsistencyMode  string          `json:"consistency_mode"`
	Outcome          string          `json:"outcome"`
	StreamCompleted  bool            `json:"stream_completed"`
	SizeBytes        int64           `json:"size_bytes"`
	Checksum         struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"checksum"`
	ObservedAt string `json:"observed_at"`
}

type backupPutObservation struct {
	Target        json.RawMessage `json:"target"`
	UploadState   string          `json:"upload_state"`
	ObjectRef     json.RawMessage `json:"object_ref"`
	BytesReceived int64           `json:"bytes_received"`
	ObservedAt    string          `json:"observed_at"`
}

type backupGetObservation struct {
	ObjectRef        json.RawMessage `json:"object_ref"`
	TransferState    string          `json:"transfer_state"`
	BytesSent        *int64          `json:"bytes_sent"`
	ComputedChecksum struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"computed_checksum"`
	ObservedAt string `json:"observed_at"`
}

type backupValidationObservation struct {
	BackupID         string `json:"backup_id"`
	EnvironmentID    string `json:"environment_id"`
	DataGenerationID string `json:"data_generation_id"`
	ValidationResult string `json:"validation_result"`
	ObservedAt       string `json:"observed_at"`
	CheckResults     []struct {
		CheckID      string `json:"check_id"`
		CheckVersion string `json:"check_version"`
		Result       string `json:"result"`
		ObservedAt   string `json:"observed_at"`
	} `json:"check_results"`
}

func backupPrepareOutput(operation store.OperationRecord) (backupPreparationObservation, error) {
	var output backupPreparationObservation
	step := operationStep(operation, backupPrepareStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("backup preparation output is missing")
	}
	var input backupPreparationObservation
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !preparationMatchesRequest(input, output) {
		return backupPreparationObservation{}, errors.New("backup preparation output does not bind the exact request")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupPreparationObservation{}, err
	}
	return output, nil
}

func preparationMatchesRequest(input, output backupPreparationObservation) bool {
	if output.Outcome != "prepared" || !equalJSON(output.Target, input.Target) ||
		output.ReleaseID != input.ReleaseID || output.BackupID != input.BackupID {
		return false
	}
	if output.DataGenerationID != input.DataGenerationID || output.ConsistencyMode != input.ConsistencyMode ||
		!sameStringSlice(output.DataSetIDs, input.DataSetIDs) {
		return false
	}
	return output.PreparationID != "" && output.ConsistencyEvidence != ""
}

func backupCaptureOutput(operation store.OperationRecord, dataSetID string) (backupCaptureObservation, error) {
	var output backupCaptureObservation
	step := operationStep(operation, backupDataStepID("capture", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("capture output is missing")
	}
	var input backupCaptureObservation
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !captureMatchesRequest(input, output) {
		return backupCaptureObservation{}, errors.New("capture output does not bind the exact prepared data set")
	}
	if _, err := hex.DecodeString(output.Checksum.Digest); err != nil {
		return backupCaptureObservation{}, errors.New("capture output checksum is not hexadecimal SHA-256")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupCaptureObservation{}, err
	}
	return output, nil
}

func captureMatchesRequest(input, output backupCaptureObservation) bool {
	if output.Outcome != "captured" || !output.StreamCompleted || !equalJSON(output.Target, input.Target) {
		return false
	}
	return captureMatchesPreparation(input, output) && captureMatchesDataSet(input, output) &&
		output.SizeBytes >= 0 && captureHasSHA256(output)
}

func captureMatchesPreparation(input, output backupCaptureObservation) bool {
	return output.ReleaseID == input.ReleaseID && output.BackupID == input.BackupID &&
		output.DataGenerationID == input.DataGenerationID && output.PreparationID == input.PreparationID
}

func captureMatchesDataSet(input, output backupCaptureObservation) bool {
	return output.DataSetID == input.DataSetID && output.CaptureID == input.CaptureID &&
		output.ConsistencyMode == input.ConsistencyMode
}

func captureHasSHA256(output backupCaptureObservation) bool {
	return output.Checksum.Algorithm == "sha256" && len(output.Checksum.Digest) == 64
}

func backupPutOutput(operation store.OperationRecord, dataSetID string) (backupPutObservation, error) {
	var output backupPutObservation
	step := operationStep(operation, backupDataStepID("upload", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("object upload output is missing")
	}
	var input struct {
		Target json.RawMessage `json:"target"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !equalJSON(output.Target, input.Target) ||
		!backupObjectRefMatchesTarget(output.ObjectRef, input.Target) || output.BytesReceived < 0 {
		return backupPutObservation{}, errors.New("object upload output does not bind the exact target")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupPutObservation{}, err
	}
	return output, nil
}

func backupGetOutput(operation store.OperationRecord, dataSetID string) (backupGetObservation, error) {
	var output backupGetObservation
	step := operationStep(operation, backupDataStepID("readback", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("object readback output is missing")
	}
	var input struct {
		ObjectRef json.RawMessage `json:"object_ref"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !equalJSON(output.ObjectRef, input.ObjectRef) || output.BytesSent == nil {
		return backupGetObservation{}, errors.New("object readback output does not bind the exact object")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupGetObservation{}, err
	}
	return output, nil
}

func backupObjectRefMatchesTarget(objectRef, target json.RawMessage) bool {
	var ref, destination map[string]any
	if json.Unmarshal(objectRef, &ref) != nil || json.Unmarshal(target, &destination) != nil || len(ref) == 0 || len(destination) == 0 {
		return false
	}
	for _, key := range []string{"adapter_id", "storage_profile_id", "environment_id", "container", "key"} {
		if !reflect.DeepEqual(ref[key], destination[key]) {
			return false
		}
	}
	return true
}
