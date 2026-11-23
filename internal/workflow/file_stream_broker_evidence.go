package workflow

import (
	"context"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// stepStreams identifies the streams of one step attempt being verified.
type stepStreams struct {
	operation   store.OperationRecord
	step        store.OperationStep
	direction   string
	storesBytes bool
}

func completedBackupStreamStep(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation,
) bool {
	return liveContext(ctx) && backupStreamOperation(operation) &&
		streamAttempt(step, protocolstate.StepSucceeded) && len(expectations) > 0
}

func runningInvocationStream(operation store.OperationRecord, step store.OperationStep, expectation ServerBackupStreamExpectation) bool {
	return operation.OperationType == "server.backup" && step.Status == protocolstate.StepRunning &&
		step.Attempt >= 1 && expectation.DataSetID != ""
}

// verifyCompletedStreams checks each expectation in order: its bounds, that
// its data set is new, then its completion evidence.
func (b *FileServerBackupTransferBroker) verifyCompletedStreams(streams stepStreams, expectations []ServerBackupStreamExpectation) error {
	seen := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if !verifiableExpectation(expectation) {
			return errors.New("stream verification expectation has an invalid data set or byte count")
		}
		if !markDataSet(seen, expectation.DataSetID) {
			return errors.New("stream verification repeats a data set")
		}
		if err := b.verifyCompletedStream(streams, expectation); err != nil {
			return err
		}
	}
	return nil
}

func verifiableExpectation(expectation ServerBackupStreamExpectation) bool {
	return expectation.DataSetID != "" && expectation.SizeBytes >= 0 && expectation.SizeBytes <= expectation.MaximumBytes
}

func (b *FileServerBackupTransferBroker) verifyCompletedStream(streams stepStreams, expectation ServerBackupStreamExpectation) error {
	operation, step := streams.operation, streams.step
	dataPath, metaPath, err := b.streamPaths(operation.OperationID, step.StepID, step.Attempt, expectation.DataSetID)
	if err != nil {
		return err
	}
	record, err := readStagedStreamRecord(metaPath)
	if err != nil {
		return errors.New("stream completion evidence is missing")
	}
	if !stagedRecordMatches(record, operation, step, expectation, streams.direction, streams.storesBytes) ||
		!recordMeasuresExpectation(record, expectation) {
		return errors.New("stream completion evidence does not match the exact operation, step, data set, size, and checksum")
	}
	if streams.storesBytes && !stagedBytesMatch(dataPath, expectation.MaximumBytes, expectation.SizeBytes, expectation.SHA256) {
		return errors.New("staged stream bytes do not match the adapter output and trusted expectation")
	}
	return nil
}

// recordMeasuresExpectation reports whether a completed record measured
// exactly the trusted size and checksum.
func recordMeasuresExpectation(record stagedStreamRecord, expectation ServerBackupStreamExpectation) bool {
	return record.Complete && record.ActualSize == expectation.SizeBytes && record.ActualSHA256 == expectation.SHA256
}

// invocationRecordComplete reports whether a running stream finished within
// its maximum and met the size and checksum it was issued with.
func invocationRecordComplete(record stagedStreamRecord, maximumBytes int64) bool {
	return record.Complete && record.ActualSize >= 0 && record.ActualSize <= maximumBytes && recordMeetsOwnExpectation(record)
}

func recordMeetsOwnExpectation(record stagedStreamRecord) bool {
	sizeMet := record.ExpectedSize < 0 || record.ActualSize == record.ExpectedSize
	checksumMet := record.ExpectedSHA256 == "" || record.ActualSHA256 == record.ExpectedSHA256
	return sizeMet && checksumMet
}

// stagedBytesMatch re-reads a staged file within maximumBytes and reports
// whether it has exactly the given size and SHA-256 digest.
func stagedBytesMatch(path string, maximumBytes, size int64, digest string) bool {
	actualSize, actualDigest, err := hashFileBounded(path, maximumBytes)
	return err == nil && actualSize == size && actualDigest == digest
}
