package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var (
	ErrStreamExpired  = errors.New("stream transfer expired")
	ErrStreamTooLarge = errors.New("stream transfer exceeded its byte limit")
)

// FileServerBackupTransferBroker stages backup bytes in a private directory.
// Transfer IDs remain transient; operation-scoped sidecars record only stream
// measurements needed to verify durable adapter outputs and recover safely.
type FileServerBackupTransferBroker struct {
	root             string
	maxStagingBytes  int64
	transferLifetime time.Duration
	artifactLifetime time.Duration
	mu               sync.Mutex
}

// NewFileServerBackupTransferBroker creates a private staging area for
// operation-scoped backup streams. The configured budget bounds aggregate
// staged bytes across operations; individual transfer references remain limited
// by their trusted profile.
func NewFileServerBackupTransferBroker(root string, maxStagingBytes int64, transferLifetime, artifactLifetime time.Duration) (*FileServerBackupTransferBroker, error) {
	if !validBrokerConfig(root, maxStagingBytes, transferLifetime, artifactLifetime) {
		return nil, errors.New("private staging path, bounded operation bytes, transfer lifetime (1ns..5m), and longer artifact lifetime are required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateStagingDirectory(abs); err != nil {
		return nil, err
	}
	return &FileServerBackupTransferBroker{
		root: abs, maxStagingBytes: maxStagingBytes,
		transferLifetime: transferLifetime, artifactLifetime: artifactLifetime,
	}, nil
}

// maxSafeStagingBytes is the largest integer a JSON number carries exactly.
const maxSafeStagingBytes = 9007199254740991

// maxTransferLifetime bounds how long one issued stream reference lives.
const maxTransferLifetime = 5 * time.Minute

// validBrokerConfig reports whether the staging path is named, the byte
// budget is 1..maxSafeStagingBytes, and the lifetimes are valid.
func validBrokerConfig(root string, maxStagingBytes int64, transferLifetime, artifactLifetime time.Duration) bool {
	return root != "" && maxStagingBytes >= 1 && maxStagingBytes <= maxSafeStagingBytes &&
		validStreamLifetimes(transferLifetime, artifactLifetime)
}

// validStreamLifetimes reports whether the transfer lifetime is
// 1ns..maxTransferLifetime and staged artifacts live at least as long.
func validStreamLifetimes(transferLifetime, artifactLifetime time.Duration) bool {
	return transferLifetime > 0 && transferLifetime <= maxTransferLifetime && artifactLifetime >= transferLifetime
}

// ensurePrivateStagingDirectory creates path if needed and checks that it is
// a real directory that only its owner can access.
func ensurePrivateStagingDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create private stream staging path: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("stream staging path must be a real directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("stream staging directory must not be accessible by group or other users")
	}
	return nil
}

// IssueStepStreamTransfers creates one-use references and direct in-process
// streams for the backup capture, storage, and game-validation steps.
func (b *FileServerBackupTransferBroker) IssueStepStreamTransfers(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation, direction string,
) ([]adapter.StreamTransfer, error) {
	if b == nil || !activeBackupStreamStep(ctx, operation, step, expectations) {
		return nil, errors.New("active server.backup stream step and context are required")
	}
	issue, err := b.planStreamIssue(ctx, operation, step, expectations, direction)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.admitStreamIssueLocked(issue); err != nil {
		return nil, err
	}
	return b.openStreamIssueLocked(issue)
}

// VerifyStepStreams independently checks the measured stream sidecars and, for
// capture/readback streams, re-reads the staged bytes to verify length and hash.
func (b *FileServerBackupTransferBroker) VerifyStepStreams(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation,
) error {
	if b == nil || !completedBackupStreamStep(ctx, operation, step, expectations) {
		return errors.New("completed server.backup stream step and context are required")
	}
	direction, sink, err := backupStreamRoute(step)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.sweepExpiredLocked(time.Now().UTC()); err != nil {
		return err
	}
	if err := b.validateOperationInput(operation, step, expectations); err != nil {
		return err
	}
	return b.verifyCompletedStreams(stepStreams{operation: operation, step: step, direction: direction, storesBytes: sink}, expectations)
}

// verifyInvocationStream validates completion evidence while the workflow
// step is still running. The later completion validator repeats these checks
// against the durable, succeeded step record.
func (b *FileServerBackupTransferBroker) verifyInvocationStream(
	operation store.OperationRecord, step store.OperationStep,
	expectation ServerBackupStreamExpectation, direction string, storesBytes bool,
) error {
	if b == nil || !runningInvocationStream(operation, step, expectation) {
		return errors.New("running server.backup stream and trusted expectation are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validateOperationInput(operation, step, []ServerBackupStreamExpectation{expectation}); err != nil {
		return err
	}
	dataPath, metaPath, err := b.streamPaths(operation.OperationID, step.StepID, step.Attempt, expectation.DataSetID)
	if err != nil {
		return err
	}
	record, err := readStagedStreamRecord(metaPath)
	streams := stepStreams{operation: operation, step: step, direction: direction, storesBytes: storesBytes}
	if err != nil || !stagedRecordMatches(record, streams, expectation) ||
		!invocationRecordComplete(record, expectation.MaximumBytes) {
		return errors.New("backup stream was not fully consumed or did not match its exact integrity expectation")
	}
	if storesBytes && !stagedBytesMatch(dataPath, expectation.MaximumBytes, record.ActualSize, record.ActualSHA256) {
		return errors.New("staged backup bytes failed independent size or checksum verification")
	}
	return nil
}

// CleanupOperation removes staged bytes for a terminal operation. It is safe to
// call repeatedly and never touches the backup objects stored by the adapter.
func (b *FileServerBackupTransferBroker) CleanupOperation(operationID string) error {
	if b == nil || operationID == "" {
		return errors.New("broker and operation ID are required for staging cleanup")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return os.RemoveAll(b.operationDirectory(operationID))
}

// SweepExpired removes operation staging directories that have exceeded the
// configured artifact lifetime.
func (b *FileServerBackupTransferBroker) SweepExpired(now time.Time) (int, error) {
	if b == nil || now.IsZero() {
		return 0, errors.New("broker and sweep clock are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sweepExpiredLocked(now.UTC())
}

func backupStreamRoute(step store.OperationStep) (string, bool, error) {
	switch step.CapabilityName {
	case "game.backup.capture":
		return "adapter_to_controller", true, nil
	case "storage.object.put":
		return "controller_to_adapter", false, nil
	case "storage.object.get":
		return "adapter_to_controller", true, nil
	case "game.backup.validate":
		return "controller_to_adapter", false, nil
	default:
		return "", false, errors.New("capability has no bounded backup stream route")
	}
}

func backupStreamSourceStepID(capability, dataSetID string) string {
	switch capability {
	case "storage.object.put":
		return backupDataStepID("capture", dataSetID)
	case "game.backup.validate":
		return backupDataStepID("readback", dataSetID)
	default:
		return ""
	}
}

func (b *FileServerBackupTransferBroker) verifySourceArtifact(
	operation store.OperationRecord, sourceStepID string, expectation ServerBackupStreamExpectation,
) error {
	if sourceStepID == "" {
		return errors.New("stream source step is not identified")
	}
	sourceStep := operationStep(operation, sourceStepID)
	if !completedSourceStep(sourceStep) {
		return errors.New("stream source step has no completed attempt")
	}
	dataPath, metaPath, err := b.streamPaths(operation.OperationID, sourceStepID, sourceStep.Attempt, expectation.DataSetID)
	if err != nil {
		return err
	}
	record, err := readStagedStreamRecord(metaPath)
	if err != nil || !sourceRecordMatches(record, operation, sourceStep, expectation) {
		return errors.New("verified staged source bytes are unavailable or do not match the transfer expectation")
	}
	if !stagedBytesMatch(dataPath, expectation.MaximumBytes, expectation.SizeBytes, expectation.SHA256) {
		return errors.New("staged source bytes failed independent size or checksum verification")
	}
	return nil
}

func completedSourceStep(step store.OperationStep) bool {
	return step.StepID != "" && step.Status == protocolstate.StepSucceeded && step.Attempt >= 1
}

// sourceRecordMatches reports whether a source step's record describes
// completed staged bytes of exactly the expected data set, size and checksum.
// sourceStep was looked up by its ID, so its StepID is the source step ID.
func sourceRecordMatches(
	record stagedStreamRecord, operation store.OperationRecord, sourceStep store.OperationStep,
	expectation ServerBackupStreamExpectation,
) bool {
	return recordOfStepAttempt(record, operation, sourceStep) &&
		recordInScope(record, operation, expectation.DataSetID, "adapter_to_controller") &&
		sourceRecordMeasured(record, expectation)
}

// sourceRecordMeasured reports whether a record stored complete bytes that
// measured exactly the expectation and fit both its own and the expected
// maximum.
func sourceRecordMeasured(record stagedStreamRecord, expectation ServerBackupStreamExpectation) bool {
	return record.StoresBytes && recordMeasuresExpectation(record, expectation) &&
		record.MaximumBytes >= expectation.SizeBytes && record.ActualSize <= expectation.MaximumBytes
}

func (b *FileServerBackupTransferBroker) streamPaths(operationID, stepID string, attempt int, dataSetID string) (string, string, error) {
	if operationID == "" || stepID == "" || attempt < 1 || dataSetID == "" {
		return "", "", errors.New("operation, step, attempt, and data-set IDs are required for a stream path")
	}
	dataPath := filepath.Join(
		b.operationDirectory(operationID), safeStreamKey(stepID),
		fmt.Sprintf("attempt-%d", attempt), safeStreamKey(dataSetID)+".bin",
	)
	return dataPath, dataPath + ".json", nil
}

func (b *FileServerBackupTransferBroker) operationDirectory(operationID string) string {
	return filepath.Join(b.root, safeStreamKey(operationID))
}

func safeStreamKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func randomTransferID() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// isSafeStreamKey reports whether value has the form safeStreamKey produces:
// a hex SHA-256 digest (sha256.Size*2 = 64 hex characters).
func isSafeStreamKey(value string) bool {
	return isSHA256(value)
}
