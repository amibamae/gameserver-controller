package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func (b *FileServerBackupTransferBroker) clearPriorStepAttempts(operationID, stepID string, currentAttempt int) error {
	stepDirectory := filepath.Join(b.operationDirectory(operationID), safeStreamKey(stepID))
	entries, err := os.ReadDir(stepDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !priorAttemptEntry(entry, currentAttempt) {
			continue
		}
		if err := removePriorAttempt(filepath.Join(stepDirectory, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// priorAttemptEntry reports whether entry is an attempt-N directory with
// 1 <= N < currentAttempt.
func priorAttemptEntry(entry os.DirEntry, currentAttempt int) bool {
	if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "attempt-") {
		return false
	}
	attempt, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "attempt-"))
	return err == nil && attempt >= 1 && attempt < currentAttempt
}

// removePriorAttempt removes an earlier attempt directory after checking,
// without following links, that it is still a real directory.
func removePriorAttempt(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prior stream attempt path is not a real directory")
	}
	return os.RemoveAll(path)
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

// stagingUsageLocked totals the staged bytes under the root, plus the unused
// maximum of every incomplete sink, and checks each staged file against its
// record. It fails on the first file, in walk order, that breaks a check.
func (b *FileServerBackupTransferBroker) stagingUsageLocked() (int64, error) {
	tally := &stagingTally{budget: b.maxStagingBytes}
	if err := filepath.WalkDir(b.root, tally.visit); err != nil {
		return 0, err
	}
	return tally.used, nil
}

// stagingTally accumulates staging usage against a byte budget.
type stagingTally struct {
	budget int64
	used   int64
}

// visit is the WalkDir callback that counts one staged byte file.
func (t *stagingTally) visit(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() || filepath.Ext(path) != ".bin" {
		return nil
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("invalid file in stream staging directory")
	}
	return t.countStagedFile(path, info.Size())
}

// countStagedFile adds a staged file's size, checks its record, then adds
// the reservation an incomplete sink still holds.
func (t *stagingTally) countStagedFile(path string, size int64) error {
	if err := t.add(size, "existing backup staging data exceeds the configured budget"); err != nil {
		return err
	}
	record, err := readUsageRecord(path + ".json")
	if err != nil {
		return err
	}
	if err := checkUsageRecord(record, size, t.budget); err != nil {
		return err
	}
	if err := t.reserve(record, size); err != nil {
		return err
	}
	if t.used > t.budget {
		return errors.New("existing backup staging data exceeds the configured budget")
	}
	return nil
}

// add counts size bytes, or returns message if they do not fit the budget.
func (t *stagingTally) add(size int64, message string) error {
	if t.used > t.budget || size > t.budget-t.used {
		return errors.New(message)
	}
	t.used += size
	return nil
}

// reserve counts the bytes an incomplete sink may still write.
func (t *stagingTally) reserve(record stagedStreamRecord, size int64) error {
	if !record.StoresBytes || record.Complete {
		return nil
	}
	reserved := record.MaximumBytes - size
	if reserved <= 0 {
		return nil
	}
	return t.add(reserved, "existing backup staging reservation exceeds the configured budget")
}

// readUsageRecord reads the transfer record beside a staged byte file. Unlike
// readStagedStreamRecord it follows symlinks, as the usage check always has.
func readUsageRecord(path string) (stagedStreamRecord, error) {
	var record stagedStreamRecord
	encoded, err := os.ReadFile(path)
	if err != nil {
		return record, errors.New("staged byte file has no matching transfer record")
	}
	if json.Unmarshal(encoded, &record) != nil {
		return record, errors.New("staged byte file has an invalid transfer record")
	}
	return record, nil
}

// checkUsageRecord checks a staged file's record: identity and bounds first,
// then, for a completed record, its measurements.
func checkUsageRecord(record stagedStreamRecord, size, budget int64) error {
	if !usageRecordValid(record, size, budget) {
		return errors.New("staged byte file has invalid size bounds or identity metadata")
	}
	if record.Complete && !usageRecordMeasured(record, size) {
		return errors.New("completed staged bytes do not match their transfer record")
	}
	return nil
}

// usageRecordValid reports whether a record stores bytes, names its stream,
// and has a maximum that holds the file and fits the budget.
func usageRecordValid(record stagedStreamRecord, size, budget int64) bool {
	return record.StoresBytes && usageRecordIdentified(record) &&
		record.MaximumBytes >= size && withinBudget(record.MaximumBytes, budget)
}

func usageRecordIdentified(record stagedStreamRecord) bool {
	return recordNamesStream(record) && !record.ExpiresAt.IsZero() && isSHA256(record.InputSHA256)
}

func recordNamesStream(record stagedStreamRecord) bool {
	return record.OperationID != "" && record.StepID != "" && record.Attempt >= 1 && record.DataSetID != ""
}

func withinBudget(maximumBytes, budget int64) bool {
	return maximumBytes >= 1 && maximumBytes <= budget
}

// usageRecordMeasured reports whether a completed record measured exactly
// the staged file, within its maximum, with a SHA-256 digest.
func usageRecordMeasured(record stagedStreamRecord, size int64) bool {
	return record.ActualSize == size && record.ActualSize <= record.MaximumBytes && isSHA256(record.ActualSHA256)
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (b *FileServerBackupTransferBroker) sweepExpiredLocked(now time.Time) (int, error) {
	entries, err := os.ReadDir(b.root)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !isSafeStreamKey(entry.Name()) {
			continue
		}
		path := filepath.Join(b.root, entry.Name())
		touched, err := directoryLatestModTime(path)
		if err != nil {
			return removed, err
		}
		if !touched.IsZero() && !touched.Add(b.artifactLifetime).After(now) {
			if err := os.RemoveAll(path); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

func directoryLatestModTime(root string) (time.Time, error) {
	var latest time.Time
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest, err
}

func isSafeStreamKey(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// stagedStream is the state a bounded sink and a bounded source share: the
// staged file, its record, and what has crossed the stream so far. The
// methods below expect the caller to hold mu.
type stagedStream struct {
	mu       sync.Mutex
	file     *os.File
	metaPath string
	record   stagedStreamRecord
	hasher   hash.Hash
	actual   int64
	failed   bool
	closed   bool
}

// streamTargetMissing reports whether a stream has no file or record path.
func streamTargetMissing(file *os.File, metaPath string) bool {
	return file == nil || metaPath == ""
}

// recordLive reports whether a stream's record has not yet expired.
func recordLive(record stagedStreamRecord) bool {
	return time.Now().UTC().Before(record.ExpiresAt)
}

// startCall rejects a Read or Write on a closed stream and fails an expired one.
func (s *stagedStream) startCall() error {
	if s.closed {
		return os.ErrClosed
	}
	if !recordLive(s.record) {
		s.failed = true
		return ErrStreamExpired
	}
	return nil
}

// measure hashes and counts bytes that crossed the stream.
func (s *stagedStream) measure(value []byte) {
	// hash.Hash.Write never returns an error.
	_, _ = s.hasher.Write(value)
	s.actual += int64(len(value))
}

// markClosed closes the stream and reports false if it was already closed.
func (s *stagedStream) markClosed() bool {
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

// recordMeasurement stores the measured size and digest in the record.
func (s *stagedStream) recordMeasurement() {
	s.record.ActualSize = s.actual
	s.record.ActualSHA256 = hex.EncodeToString(s.hasher.Sum(nil))
}

// storeRecord writes the measured record with its completion flag.
func (s *stagedStream) storeRecord(complete bool) error {
	s.record.Complete = complete
	return writeStagedStreamRecord(s.metaPath, s.record)
}

type stagedStreamWriter struct {
	stagedStream
}

func newStagedStreamWriter(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamWriter, error) {
	if streamTargetMissing(file, metaPath) || !record.StoresBytes || record.Direction != "adapter_to_controller" {
		return nil, errors.New("valid bounded stream sink is required")
	}
	return &stagedStreamWriter{stagedStream: stagedStream{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}}, nil
}

func (writer *stagedStreamWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.startCall(); err != nil {
		return 0, err
	}
	if int64(len(value)) > writer.record.MaximumBytes-writer.actual {
		writer.failed = true
		return 0, ErrStreamTooLarge
	}
	written, err := writer.file.Write(value)
	if written > 0 {
		writer.measure(value[:written])
	}
	if err != nil {
		writer.failed = true
	}
	return written, err
}

func (writer *stagedStreamWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if !writer.markClosed() {
		return nil
	}
	writer.syncAndClose()
	writer.recordMeasurement()
	if !recordLive(writer.record) || !recordMeetsOwnExpectation(writer.record) {
		writer.failed = true
	}
	if err := writer.storeRecord(!writer.failed); err != nil {
		return err
	}
	if writer.failed {
		return errors.New("bounded stream sink was incomplete, expired, oversized, or mismatched")
	}
	return nil
}

// syncAndClose flushes and closes the staged file. Either failure fails the
// sink, which Close then reports with its own error.
func (writer *stagedStreamWriter) syncAndClose() {
	if err := writer.file.Sync(); err != nil {
		writer.failed = true
	}
	if err := writer.file.Close(); err != nil {
		writer.failed = true
	}
}

type stagedStreamReader struct {
	stagedStream
	eof bool
}

func newStagedStreamReader(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamReader, error) {
	if streamTargetMissing(file, metaPath) || !sourceStreamRecord(record) {
		return nil, errors.New("valid bounded stream source is required")
	}
	return &stagedStreamReader{stagedStream: stagedStream{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}}, nil
}

// sourceStreamRecord reports whether a record describes a source stream of
// already staged bytes with a known size.
func sourceStreamRecord(record stagedStreamRecord) bool {
	return !record.StoresBytes && record.Direction == "controller_to_adapter" && record.ExpectedSize >= 0
}

func (reader *stagedStreamReader) Read(value []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := reader.startCall(); err != nil {
		return 0, err
	}
	read, err := reader.file.Read(value)
	if read > 0 {
		if int64(read) > reader.record.MaximumBytes-reader.actual {
			reader.failed = true
			return 0, ErrStreamTooLarge
		}
		reader.measure(value[:read])
	}
	if errors.Is(err, io.EOF) {
		reader.eof = true
	} else if err != nil {
		reader.failed = true
	}
	return read, err
}

func (reader *stagedStreamReader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.markClosed() {
		return nil
	}
	closeErr := reader.file.Close()
	reader.recordMeasurement()
	complete := reader.drained(closeErr) && recordLive(reader.record) &&
		reader.actual == reader.record.ExpectedSize && recordChecksumMet(reader.record)
	if err := reader.storeRecord(complete); err != nil {
		return err
	}
	if complete {
		return nil
	}
	if closeErr != nil {
		return closeErr
	}
	return errors.New("bounded stream source was incomplete, expired, oversized, or mismatched")
}

// drained reports whether the source was read to its end without a failure
// and its file closed cleanly.
func (reader *stagedStreamReader) drained(closeErr error) bool {
	return reader.eof && !reader.failed && closeErr == nil
}
