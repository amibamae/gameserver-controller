package workflow

// Staged stream records: the sidecar each stream leaves beside its staged
// bytes, how it is built and matched to a step, and how it and the bytes are
// stored and re-read.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

type stagedStreamRecord struct {
	OperationID    string    `json:"operation_id"`
	StepID         string    `json:"step_id"`
	Attempt        int       `json:"attempt"`
	AdapterID      string    `json:"adapter_id"`
	Capability     string    `json:"capability"`
	EnvironmentID  string    `json:"environment_id"`
	DataSetID      string    `json:"data_set_id"`
	Direction      string    `json:"direction"`
	MaximumBytes   int64     `json:"maximum_bytes"`
	ExpectedSize   int64     `json:"expected_size_bytes"`
	ExpectedSHA256 string    `json:"expected_sha256"`
	InputSHA256    string    `json:"input_sha256"`
	ExpiresAt      time.Time `json:"expires_at"`
	StoresBytes    bool      `json:"stores_bytes"`
	Complete       bool      `json:"complete"`
	ActualSize     int64     `json:"actual_size_bytes"`
	ActualSHA256   string    `json:"actual_sha256"`
}

func (b *FileServerBackupTransferBroker) newRecord(
	operation store.OperationRecord, step store.OperationStep, expectation ServerBackupStreamExpectation,
	direction string, expiresAt time.Time, storesBytes bool,
) stagedStreamRecord {
	return stagedStreamRecord{
		OperationID: operation.OperationID, StepID: step.StepID, Attempt: step.Attempt,
		AdapterID: step.AdapterID, Capability: step.CapabilityName,
		EnvironmentID: operation.Target.EnvironmentID, DataSetID: expectation.DataSetID,
		Direction: direction, MaximumBytes: expectation.MaximumBytes,
		ExpectedSize: expectation.SizeBytes, ExpectedSHA256: expectation.SHA256,
		InputSHA256: streamInputDigest(effectiveStepInput(step)), ExpiresAt: expiresAt,
		StoresBytes: storesBytes,
	}
}

// stagedRecordMatches reports whether a record was issued for exactly this
// step attempt, adapter, data set, direction, input and expectation.
func stagedRecordMatches(record stagedStreamRecord, streams stepStreams, expectation ServerBackupStreamExpectation) bool {
	return recordOfStepAttempt(record, streams.operation, streams.step) &&
		recordOfAdapterCall(record, streams.step) &&
		recordInScope(record, streams.operation, expectation.DataSetID, streams.direction) &&
		recordIssuedFor(record, streams, expectation)
}

func recordOfStepAttempt(record stagedStreamRecord, operation store.OperationRecord, step store.OperationStep) bool {
	return record.OperationID == operation.OperationID && record.StepID == step.StepID && record.Attempt == step.Attempt
}

func recordOfAdapterCall(record stagedStreamRecord, step store.OperationStep) bool {
	return record.AdapterID == step.AdapterID && record.Capability == step.CapabilityName
}

func recordInScope(record stagedStreamRecord, operation store.OperationRecord, dataSetID, direction string) bool {
	return record.EnvironmentID == operation.Target.EnvironmentID && record.DataSetID == dataSetID && record.Direction == direction
}

// recordIssuedFor reports whether a record carries the issue-time bounds,
// input digest, storage mode, expiry and expectation of this stream.
func recordIssuedFor(record stagedStreamRecord, streams stepStreams, expectation ServerBackupStreamExpectation) bool {
	return record.MaximumBytes == expectation.MaximumBytes &&
		record.InputSHA256 == streamInputDigest(effectiveStepInput(streams.step)) &&
		record.StoresBytes == streams.storesBytes && recordExpectationCompatible(record, expectation)
}

// recordExpectationCompatible reports whether a record has an expiry and
// expected size and checksum that are unknown or equal to the expectation's.
func recordExpectationCompatible(record stagedStreamRecord, expectation ServerBackupStreamExpectation) bool {
	sizeCompatible := record.ExpectedSize < 0 || record.ExpectedSize == expectation.SizeBytes
	checksumCompatible := record.ExpectedSHA256 == "" || record.ExpectedSHA256 == expectation.SHA256
	return !record.ExpiresAt.IsZero() && sizeCompatible && checksumCompatible
}

func streamInputDigest(input json.RawMessage) string {
	canonical := decodeObject(input)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func writeStagedStreamRecord(path string, record stagedStreamRecord) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".stream-record-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func readStagedStreamRecord(path string) (stagedStreamRecord, error) {
	var record stagedStreamRecord
	fileInfo, err := os.Lstat(path)
	if err != nil || !fileInfo.Mode().IsRegular() {
		return record, errors.New("stream record is not a regular file")
	}
	encoded, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(encoded, &record) != nil {
		return stagedStreamRecord{}, errors.New("decode stream completion record")
	}
	return record, nil
}

func hashFileBounded(path string, maximumBytes int64) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximumBytes {
		return 0, "", errors.New("staged stream file is missing or exceeds its limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	hasher := sha256.New()
	size, copyErr := io.Copy(hasher, io.LimitReader(file, maximumBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size > maximumBytes {
		return 0, "", errors.New("read staged stream bytes")
	}
	return size, hex.EncodeToString(hasher.Sum(nil)), nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
