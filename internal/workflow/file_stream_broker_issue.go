package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// maxStepStreams bounds how many streams one backup step may request.
const maxStepStreams = 128

// streamIssue is one IssueStepStreamTransfers call once its route and expiry
// are known.
type streamIssue struct {
	operation    store.OperationRecord
	step         store.OperationStep
	expectations []ServerBackupStreamExpectation
	direction    string
	sink         bool
	now          time.Time
	expiresAt    time.Time
}

// streamTarget is where one issued stream is staged and how it is recorded.
type streamTarget struct {
	reference capturestream.Reference
	record    stagedStreamRecord
	dataPath  string
	metaPath  string
}

// openedStreams tracks what an issue has opened or created so far, so a
// failed issue can release all of it.
type openedStreams struct {
	transfers []adapter.StreamTransfer
	paths     []string
}

func activeBackupStreamStep(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation,
) bool {
	return liveContext(ctx) && backupStreamOperation(operation) &&
		streamAttempt(step, protocolstate.StepRunning) && issuableStreamCount(len(expectations))
}

func liveContext(ctx context.Context) bool { return ctx != nil && ctx.Err() == nil }

func backupStreamOperation(operation store.OperationRecord) bool {
	return operation.OperationID != "" && operation.OperationType == "server.backup"
}

func streamAttempt(step store.OperationStep, status protocolstate.StepStatus) bool {
	return step.StepID != "" && step.Attempt >= 1 && step.Status == status
}

func issuableStreamCount(count int) bool { return count > 0 && count <= maxStepStreams }

// planStreamIssue checks the invocation deadline and the capability route,
// and fixes the expiry every issued reference shares.
func (b *FileServerBackupTransferBroker) planStreamIssue(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation, direction string,
) (*streamIssue, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("stream invocation deadline is required")
	}
	expectedDirection, sink, err := backupStreamRoute(step)
	if err != nil || direction != expectedDirection {
		return nil, errors.New("stream direction does not match the exact backup capability")
	}
	now := time.Now().UTC()
	expiresAt := b.streamExpiry(now, deadline)
	if !expiresAt.After(now) {
		return nil, errors.New("stream invocation deadline has expired")
	}
	return &streamIssue{
		operation: operation, step: step, expectations: expectations,
		direction: direction, sink: sink, now: now, expiresAt: expiresAt,
	}, nil
}

// streamExpiry is the transfer lifetime from now, cut short by the deadline.
func (b *FileServerBackupTransferBroker) streamExpiry(now, deadline time.Time) time.Time {
	expiresAt := now.Add(b.transferLifetime)
	if deadline.Before(expiresAt) {
		return deadline.UTC()
	}
	return expiresAt
}

// admitStreamIssueLocked runs every check that must pass before any stream
// is opened, in order: sweep, input binding, readback retry cleanup, sink
// count, expectation bounds, then the staging budget or source evidence.
func (b *FileServerBackupTransferBroker) admitStreamIssueLocked(issue *streamIssue) error {
	if _, err := b.sweepExpiredLocked(issue.now); err != nil {
		return err
	}
	if err := b.validateOperationInput(issue.operation, issue.step, issue.expectations); err != nil {
		return err
	}
	if err := b.clearReadbackRetry(issue.operation.OperationID, issue.step); err != nil {
		return err
	}
	if issue.sink && len(issue.expectations) != 1 {
		return errors.New("one sink stream is allowed per backup capture or readback step")
	}
	if err := b.checkIssueExpectations(issue.expectations, issue.sink); err != nil {
		return err
	}
	if issue.sink {
		return b.reserveSinkStagingLocked(issue.expectations)
	}
	return b.verifyIssueSources(issue)
}

// clearReadbackRetry removes earlier attempts of a storage readback step.
func (b *FileServerBackupTransferBroker) clearReadbackRetry(operationID string, step store.OperationStep) error {
	if step.CapabilityName != "storage.object.get" {
		return nil
	}
	return b.clearPriorStepAttempts(operationID, step.StepID, step.Attempt)
}

func (b *FileServerBackupTransferBroker) checkIssueExpectations(expectations []ServerBackupStreamExpectation, sink bool) error {
	seen := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if !b.issuableExpectation(expectation, sink) {
			return errors.New("stream expectation is outside the configured operation and transfer bounds")
		}
		if !markDataSet(seen, expectation.DataSetID) {
			return errors.New("backup stream step repeats a data set")
		}
	}
	return nil
}

// issuableExpectation reports whether an expectation names a data set, fits
// the staging budget, and has a known size unless it is a sink.
func (b *FileServerBackupTransferBroker) issuableExpectation(expectation ServerBackupStreamExpectation, sink bool) bool {
	return expectation.DataSetID != "" && b.withinStagingBudget(expectation.MaximumBytes) && issuableSize(expectation, sink)
}

func (b *FileServerBackupTransferBroker) withinStagingBudget(maximumBytes int64) bool {
	return maximumBytes >= 1 && maximumBytes <= b.maxStagingBytes
}

// issuableSize allows -1 (unknown) only for sinks, and otherwise 0..maximum.
func issuableSize(expectation ServerBackupStreamExpectation, sink bool) bool {
	return expectation.SizeBytes >= -1 && expectation.SizeBytes <= expectation.MaximumBytes &&
		(sink || expectation.SizeBytes != -1)
}

// markDataSet records dataSetID in seen and reports false if it was already there.
func markDataSet(seen map[string]struct{}, dataSetID string) bool {
	if _, duplicate := seen[dataSetID]; duplicate {
		return false
	}
	seen[dataSetID] = struct{}{}
	return true
}

func (b *FileServerBackupTransferBroker) verifyIssueSources(issue *streamIssue) error {
	for _, expectation := range issue.expectations {
		sourceStepID := backupStreamSourceStepID(issue.step.CapabilityName, expectation.DataSetID)
		if err := b.verifySourceArtifact(issue.operation, sourceStepID, expectation); err != nil {
			return err
		}
	}
	return nil
}

// reserveSinkStagingLocked checks that existing staging plus every sink's
// maximum fits the configured budget.
func (b *FileServerBackupTransferBroker) reserveSinkStagingLocked(expectations []ServerBackupStreamExpectation) error {
	usage, err := b.stagingUsageLocked()
	if err != nil {
		return err
	}
	reserved, err := b.sinkReservation(expectations)
	if err != nil {
		return err
	}
	if usage > b.maxStagingBytes-reserved {
		return errors.New("backup stream staging budget is exhausted")
	}
	return nil
}

func (b *FileServerBackupTransferBroker) sinkReservation(expectations []ServerBackupStreamExpectation) (int64, error) {
	reserved := int64(0)
	for _, expectation := range expectations {
		if expectation.MaximumBytes > b.maxStagingBytes-reserved {
			return 0, errors.New("backup stream staging reservation exceeds the configured budget")
		}
		reserved += expectation.MaximumBytes
	}
	return reserved, nil
}

// openStreamIssueLocked opens one stream per expectation, in order. If any
// stream fails, every stream already opened is closed and every staging
// path created for this issue is removed.
func (b *FileServerBackupTransferBroker) openStreamIssueLocked(issue *streamIssue) ([]adapter.StreamTransfer, error) {
	opened := &openedStreams{
		transfers: make([]adapter.StreamTransfer, 0, len(issue.expectations)),
		paths:     make([]string, 0, len(issue.expectations)*2),
	}
	committed := false
	defer func() {
		if !committed {
			opened.abandon()
		}
	}()
	for _, expectation := range issue.expectations {
		transfer, err := b.openIssueStream(issue, expectation, opened)
		if err != nil {
			return nil, err
		}
		opened.transfers = append(opened.transfers, transfer)
	}
	committed = true
	return opened.transfers, nil
}

func (b *FileServerBackupTransferBroker) openIssueStream(
	issue *streamIssue, expectation ServerBackupStreamExpectation, opened *openedStreams,
) (adapter.StreamTransfer, error) {
	transferID, err := randomTransferID()
	if err != nil {
		return adapter.StreamTransfer{}, err
	}
	target := streamTarget{
		reference: capturestream.Reference{
			TransferID: transferID, Direction: issue.direction,
			MaximumBytes: expectation.MaximumBytes, ExpiresAt: issue.expiresAt,
		},
		record: b.newRecord(issue.operation, issue.step, expectation, issue.direction, issue.expiresAt, issue.sink),
	}
	target.dataPath, target.metaPath, err = b.streamPaths(issue.operation.OperationID, issue.step.StepID, issue.step.Attempt, expectation.DataSetID)
	if err != nil {
		return adapter.StreamTransfer{}, err
	}
	if err := prepareStreamAttempt(target); err != nil {
		return adapter.StreamTransfer{}, err
	}
	if issue.sink {
		return b.openSinkStream(issue, expectation, target, opened)
	}
	return b.openSourceStream(issue, expectation, target, opened)
}

// prepareStreamAttempt creates the attempt directory and refuses to reuse an
// attempt that already has transfer metadata.
func prepareStreamAttempt(target streamTarget) error {
	if err := os.MkdirAll(filepath.Dir(target.dataPath), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(target.metaPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("stream attempt already has transfer metadata")
	}
	return nil
}

// openSinkStream creates the bounded staging file the adapter writes into.
func (b *FileServerBackupTransferBroker) openSinkStream(
	issue *streamIssue, expectation ServerBackupStreamExpectation, target streamTarget, opened *openedStreams,
) (adapter.StreamTransfer, error) {
	file, err := os.OpenFile(target.dataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return adapter.StreamTransfer{}, fmt.Errorf("create bounded stream staging file: %w", err)
	}
	opened.paths = append(opened.paths, target.dataPath, target.metaPath)
	if err := recordOpenedStream(file, target, target.dataPath); err != nil {
		return adapter.StreamTransfer{}, err
	}
	writer, err := newStagedStreamWriter(file, target.metaPath, target.record)
	if err != nil {
		closeAbandoned(file)
		return adapter.StreamTransfer{}, err
	}
	return adapter.StreamTransfer{Reference: target.reference, Sink: writer, Verify: b.invocationVerifier(issue, expectation, true)}, nil
}

// openSourceStream opens the verified staged bytes of the step's source.
func (b *FileServerBackupTransferBroker) openSourceStream(
	issue *streamIssue, expectation ServerBackupStreamExpectation, target streamTarget, opened *openedStreams,
) (adapter.StreamTransfer, error) {
	sourceStepID := backupStreamSourceStepID(issue.step.CapabilityName, expectation.DataSetID)
	sourceStep := operationStep(issue.operation, sourceStepID)
	sourcePath, _, pathErr := b.streamPaths(issue.operation.OperationID, sourceStepID, sourceStep.Attempt, expectation.DataSetID)
	if pathErr != nil {
		return adapter.StreamTransfer{}, pathErr
	}
	readerFile, err := os.Open(sourcePath)
	if err != nil {
		return adapter.StreamTransfer{}, fmt.Errorf("open verified backup source stream: %w", err)
	}
	opened.paths = append(opened.paths, target.metaPath)
	if err := recordOpenedStream(readerFile, target); err != nil {
		return adapter.StreamTransfer{}, err
	}
	reader, err := newStagedStreamReader(readerFile, target.metaPath, target.record)
	if err != nil {
		closeAbandoned(readerFile)
		return adapter.StreamTransfer{}, err
	}
	return adapter.StreamTransfer{Reference: target.reference, Source: reader, Verify: b.invocationVerifier(issue, expectation, false)}, nil
}

// recordOpenedStream writes a stream's initial record. If that fails, it
// closes file and removes the discard paths before returning the error.
func recordOpenedStream(file *os.File, target streamTarget, discard ...string) error {
	err := writeStagedStreamRecord(target.metaPath, target.record)
	if err == nil {
		return nil
	}
	closeAbandoned(file)
	for _, path := range discard {
		removeAbandoned(path)
	}
	return err
}

// invocationVerifier binds the running-step verification for one stream.
func (b *FileServerBackupTransferBroker) invocationVerifier(
	issue *streamIssue, expectation ServerBackupStreamExpectation, storesBytes bool,
) func() error {
	operation, step, direction := issue.operation, issue.step, issue.direction
	return func() error { return b.verifyInvocationStream(operation, step, expectation, direction, storesBytes) }
}

// abandon closes every stream opened so far, then removes the staging paths
// the issue created.
func (o *openedStreams) abandon() {
	for _, transfer := range o.transfers {
		closeAbandoned(transfer.Source)
		closeAbandoned(transfer.Sink)
	}
	for _, path := range o.paths {
		removeAbandoned(path)
	}
}

// closeAbandoned closes a stream on a failure path. The caller is already
// returning the error that caused the failure, and that error must stay
// exactly as it is, so a close error here is deliberately dropped.
func closeAbandoned(closer io.Closer) {
	if closer == nil {
		return
	}
	_ = closer.Close()
}

// removeAbandoned removes a staging path on a failure path. As with
// closeAbandoned, the caller's original error is returned unchanged; a path
// that cannot be removed is reclaimed later by the expiry sweep.
func removeAbandoned(path string) {
	_ = os.Remove(path)
}
