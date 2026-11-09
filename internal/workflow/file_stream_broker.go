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
	"github.com/amibamae/gameserver-controller/internal/capturestream"
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
	if root == "" || maxStagingBytes < 1 || maxStagingBytes > 9007199254740991 ||
		transferLifetime <= 0 || transferLifetime > 5*time.Minute ||
		artifactLifetime < transferLifetime {
		return nil, errors.New("private staging path, bounded operation bytes, transfer lifetime (1ns..5m), and longer artifact lifetime are required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, fmt.Errorf("create private stream staging path: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("stream staging path must be a real directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("stream staging directory must not be accessible by group or other users")
	}
	return &FileServerBackupTransferBroker{
		root: abs, maxStagingBytes: maxStagingBytes,
		transferLifetime: transferLifetime, artifactLifetime: artifactLifetime,
	}, nil
}

// IssueStepStreamTransfers creates one-use references and direct in-process
// streams for the backup capture, storage, and game-validation steps.
func (b *FileServerBackupTransferBroker) IssueStepStreamTransfers(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation, direction string,
) ([]adapter.StreamTransfer, error) {
	if b == nil || ctx == nil || ctx.Err() != nil || operation.OperationID == "" ||
		operation.OperationType != "server.backup" || step.StepID == "" || step.Attempt < 1 ||
		step.Status != protocolstate.StepRunning || len(expectations) == 0 || len(expectations) > 128 {
		return nil, errors.New("active server.backup stream step and context are required")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errors.New("stream invocation deadline is required")
	}
	expectedDirection, sink, err := backupStreamRoute(step)
	if err != nil || direction != expectedDirection {
		return nil, errors.New("stream direction does not match the exact backup capability")
	}
	now := time.Now().UTC()
	expiresAt := now.Add(b.transferLifetime)
	if deadline.Before(expiresAt) {
		expiresAt = deadline.UTC()
	}
	if !expiresAt.After(now) {
		return nil, errors.New("stream invocation deadline has expired")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.sweepExpiredLocked(now); err != nil {
		return nil, err
	}
	if err := b.validateOperationInput(operation, step, expectations); err != nil {
		return nil, err
	}
	if step.CapabilityName == "storage.object.get" {
		if err := b.clearPriorStepAttempts(operation.OperationID, step.StepID, step.Attempt); err != nil {
			return nil, err
		}
	}
	if sink && len(expectations) != 1 {
		return nil, errors.New("one sink stream is allowed per backup capture or readback step")
	}
	seenDataSets := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if expectation.DataSetID == "" || expectation.MaximumBytes < 1 ||
			expectation.MaximumBytes > b.maxStagingBytes ||
			expectation.SizeBytes < -1 || expectation.SizeBytes > expectation.MaximumBytes ||
			(expectation.SizeBytes == -1 && !sink) {
			return nil, errors.New("stream expectation is outside the configured operation and transfer bounds")
		}
		if _, duplicate := seenDataSets[expectation.DataSetID]; duplicate {
			return nil, errors.New("backup stream step repeats a data set")
		}
		seenDataSets[expectation.DataSetID] = struct{}{}
	}
	if !sink {
		for _, expectation := range expectations {
			sourceStepID := backupStreamSourceStepID(step.CapabilityName, expectation.DataSetID)
			if err := b.verifySourceArtifact(operation, sourceStepID, expectation); err != nil {
				return nil, err
			}
		}
	}
	if sink {
		usage, err := b.stagingUsageLocked()
		if err != nil {
			return nil, err
		}
		reserved := int64(0)
		for _, expectation := range expectations {
			if expectation.MaximumBytes > b.maxStagingBytes-reserved {
				return nil, errors.New("backup stream staging reservation exceeds the configured budget")
			}
			reserved += expectation.MaximumBytes
		}
		if usage > b.maxStagingBytes-reserved {
			return nil, errors.New("backup stream staging budget is exhausted")
		}
	}

	transfers := make([]adapter.StreamTransfer, 0, len(expectations))
	createdPaths := make([]string, 0, len(expectations)*2)
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, transfer := range transfers {
			if transfer.Source != nil {
				_ = transfer.Source.Close()
			}
			if transfer.Sink != nil {
				_ = transfer.Sink.Close()
			}
		}
		for _, path := range createdPaths {
			_ = os.Remove(path)
		}
	}()
	for _, expectation := range expectations {
		transferID, err := randomTransferID()
		if err != nil {
			return nil, err
		}
		reference := capturestream.Reference{
			TransferID: transferID, Direction: direction,
			MaximumBytes: expectation.MaximumBytes, ExpiresAt: expiresAt,
		}
		record := b.newRecord(operation, step, expectation, direction, expiresAt, sink)
		dataPath, metaPath, err := b.streamPaths(operation.OperationID, step.StepID, step.Attempt, expectation.DataSetID)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dataPath), 0700); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(metaPath); err == nil || !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("stream attempt already has transfer metadata")
		}
		if sink {
			file, err := os.OpenFile(dataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return nil, fmt.Errorf("create bounded stream staging file: %w", err)
			}
			createdPaths = append(createdPaths, dataPath, metaPath)
			if err := writeStagedStreamRecord(metaPath, record); err != nil {
				_ = file.Close()
				_ = os.Remove(dataPath)
				return nil, err
			}
			writer, err := newStagedStreamWriter(file, metaPath, record)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			transfers = append(transfers, adapter.StreamTransfer{Reference: reference, Sink: writer,
				Verify: func() error { return b.verifyInvocationStream(operation, step, expectation, direction, true) }})
		} else {
			sourceStepID := backupStreamSourceStepID(step.CapabilityName, expectation.DataSetID)
			sourceStep := operationStep(operation, sourceStepID)
			sourcePath, _, pathErr := b.streamPaths(operation.OperationID, sourceStepID, sourceStep.Attempt, expectation.DataSetID)
			if pathErr != nil {
				return nil, pathErr
			}
			readerFile, err := os.Open(sourcePath)
			if err != nil {
				return nil, fmt.Errorf("open verified backup source stream: %w", err)
			}
			createdPaths = append(createdPaths, metaPath)
			if err := writeStagedStreamRecord(metaPath, record); err != nil {
				_ = readerFile.Close()
				return nil, err
			}
			reader, err := newStagedStreamReader(readerFile, metaPath, record)
			if err != nil {
				_ = readerFile.Close()
				return nil, err
			}
			transfers = append(transfers, adapter.StreamTransfer{Reference: reference, Source: reader,
				Verify: func() error { return b.verifyInvocationStream(operation, step, expectation, direction, false) }})
		}
	}
	committed = true
	return transfers, nil
}

// VerifyStepStreams independently checks the measured stream sidecars and, for
// capture/readback streams, re-reads the staged bytes to verify length and hash.
func (b *FileServerBackupTransferBroker) VerifyStepStreams(
	ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	expectations []ServerBackupStreamExpectation,
) error {
	if b == nil || ctx == nil || ctx.Err() != nil || operation.OperationID == "" ||
		operation.OperationType != "server.backup" || step.StepID == "" ||
		step.Status != protocolstate.StepSucceeded || step.Attempt < 1 || len(expectations) == 0 {
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
	seen := make(map[string]struct{}, len(expectations))
	for _, expectation := range expectations {
		if expectation.DataSetID == "" || expectation.SizeBytes < 0 || expectation.SizeBytes > expectation.MaximumBytes {
			return errors.New("stream verification expectation has an invalid data set or byte count")
		}
		if _, duplicate := seen[expectation.DataSetID]; duplicate {
			return errors.New("stream verification repeats a data set")
		}
		seen[expectation.DataSetID] = struct{}{}
		dataPath, metaPath, err := b.streamPaths(operation.OperationID, step.StepID, step.Attempt, expectation.DataSetID)
		if err != nil {
			return err
		}
		record, err := readStagedStreamRecord(metaPath)
		if err != nil {
			return errors.New("stream completion evidence is missing")
		}
		if !stagedRecordMatches(record, operation, step, expectation, direction, sink) ||
			!record.Complete || record.ActualSize != expectation.SizeBytes ||
			record.ActualSHA256 != expectation.SHA256 {
			return errors.New("stream completion evidence does not match the exact operation, step, data set, size, and checksum")
		}
		if sink {
			size, digest, err := hashFileBounded(dataPath, expectation.MaximumBytes)
			if err != nil || size != expectation.SizeBytes || digest != expectation.SHA256 {
				return errors.New("staged stream bytes do not match the adapter output and trusted expectation")
			}
		}
	}
	return nil
}

// verifyInvocationStream validates completion evidence while the workflow
// step is still running. The later completion validator repeats these checks
// against the durable, succeeded step record.
func (b *FileServerBackupTransferBroker) verifyInvocationStream(
	operation store.OperationRecord, step store.OperationStep,
	expectation ServerBackupStreamExpectation, direction string, storesBytes bool,
) error {
	if b == nil || operation.OperationType != "server.backup" || step.Status != protocolstate.StepRunning ||
		step.Attempt < 1 || expectation.DataSetID == "" {
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
	if err != nil || !stagedRecordMatches(record, operation, step, expectation, direction, storesBytes) ||
		!record.Complete || record.ActualSize < 0 || record.ActualSize > expectation.MaximumBytes ||
		(record.ExpectedSize >= 0 && record.ActualSize != record.ExpectedSize) ||
		(record.ExpectedSHA256 != "" && record.ActualSHA256 != record.ExpectedSHA256) {
		return errors.New("backup stream was not fully consumed or did not match its exact integrity expectation")
	}
	if storesBytes {
		size, digest, err := hashFileBounded(dataPath, expectation.MaximumBytes)
		if err != nil || size != record.ActualSize || digest != record.ActualSHA256 {
			return errors.New("staged backup bytes failed independent size or checksum verification")
		}
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

func (b *FileServerBackupTransferBroker) validateOperationInput(
	operation store.OperationRecord, step store.OperationStep, expectations []ServerBackupStreamExpectation,
) error {
	input := decodeObject(effectiveStepInput(step))
	switch step.CapabilityName {
	case "game.backup.capture":
		if stringValueMap(input, "backup_id") != operation.OperationID ||
			stringValueMap(input, "data_set_id") != expectations[0].DataSetID ||
			!backupTargetMatchesOperation(mustJSON(input["target"]), operation) {
			return errors.New("capture stream is not bound to the exact operation target and data set")
		}
	case "storage.object.put", "storage.object.get":
		size, sizeErr := integerFromAny(input["expected_size_bytes"])
		checksum := asStringMap(input["expected_checksum"])
		if sizeErr != nil || size != expectations[0].SizeBytes ||
			stringValueMap(checksum, "algorithm") != "sha256" ||
			stringValueMap(checksum, "digest") != expectations[0].SHA256 {
			return errors.New("storage stream is not bound to the exact expected size and checksum")
		}
		if step.CapabilityName == "storage.object.put" {
			target := asStringMap(input["target"])
			if stringValueMap(target, "environment_id") != operation.Target.EnvironmentID {
				return errors.New("storage upload target crosses its operation environment")
			}
		} else {
			objectRef := asStringMap(input["object_ref"])
			if stringValueMap(objectRef, "environment_id") != operation.Target.EnvironmentID {
				return errors.New("storage readback reference crosses its operation environment")
			}
		}
	case "game.backup.validate":
		if stringValueMap(input, "backup_id") != operation.OperationID ||
			stringValueMap(input, "environment_id") != operation.Target.EnvironmentID {
			return errors.New("game validation stream is not bound to its operation and environment")
		}
		manifest := asStringMap(input["manifest"])
		objects := asAnySlice(manifest["objects"])
		streams := asAnySlice(input["object_streams"])
		if len(objects) != len(expectations) || len(streams) != len(expectations) {
			return errors.New("game validation stream count differs from the manifest object count")
		}
		for index, expectation := range expectations {
			object := asStringMap(objects[index])
			stream := asStringMap(streams[index])
			size, sizeErr := integerFromAny(object["size_bytes"])
			if sizeErr != nil || stringValueMap(object, "data_set_id") != expectation.DataSetID ||
				size != expectation.SizeBytes || stringValueMap(object, "checksum") != expectation.SHA256 ||
				!equalJSON(mustJSON(stream["object_ref"]), mustJSON(object["object_ref"])) {
				return errors.New("game validation stream is not bound to the corresponding exact manifest object")
			}
		}
	default:
		return errors.New("unsupported stream capability")
	}
	return nil
}

func (b *FileServerBackupTransferBroker) verifySourceArtifact(
	operation store.OperationRecord, sourceStepID string, expectation ServerBackupStreamExpectation,
) error {
	if sourceStepID == "" {
		return errors.New("stream source step is not identified")
	}
	sourceStep := operationStep(operation, sourceStepID)
	if sourceStep.StepID == "" || sourceStep.Status != protocolstate.StepSucceeded || sourceStep.Attempt < 1 {
		return errors.New("stream source step has no completed attempt")
	}
	dataPath, metaPath, err := b.streamPaths(operation.OperationID, sourceStepID, sourceStep.Attempt, expectation.DataSetID)
	if err != nil {
		return err
	}
	record, err := readStagedStreamRecord(metaPath)
	if err != nil || record.OperationID != operation.OperationID || record.StepID != sourceStepID ||
		record.Attempt != sourceStep.Attempt || record.EnvironmentID != operation.Target.EnvironmentID ||
		record.DataSetID != expectation.DataSetID || record.Direction != "adapter_to_controller" ||
		!record.StoresBytes || !record.Complete || record.ActualSize != expectation.SizeBytes ||
		record.ActualSHA256 != expectation.SHA256 || record.MaximumBytes < expectation.SizeBytes ||
		record.ActualSize > expectation.MaximumBytes {
		return errors.New("verified staged source bytes are unavailable or do not match the transfer expectation")
	}
	size, digest, err := hashFileBounded(dataPath, expectation.MaximumBytes)
	if err != nil || size != expectation.SizeBytes || digest != expectation.SHA256 {
		return errors.New("staged source bytes failed independent size or checksum verification")
	}
	return nil
}

func stagedRecordMatches(
	record stagedStreamRecord, operation store.OperationRecord, step store.OperationStep,
	expectation ServerBackupStreamExpectation, direction string, storesBytes bool,
) bool {
	return record.OperationID == operation.OperationID && record.StepID == step.StepID &&
		record.Attempt == step.Attempt && record.AdapterID == step.AdapterID &&
		record.Capability == step.CapabilityName && record.EnvironmentID == operation.Target.EnvironmentID &&
		record.DataSetID == expectation.DataSetID && record.Direction == direction &&
		record.MaximumBytes == expectation.MaximumBytes && record.InputSHA256 == streamInputDigest(effectiveStepInput(step)) &&
		record.StoresBytes == storesBytes && !record.ExpiresAt.IsZero() &&
		(record.ExpectedSize < 0 || record.ExpectedSize == expectation.SizeBytes) &&
		(record.ExpectedSHA256 == "" || record.ExpectedSHA256 == expectation.SHA256)
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
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "attempt-") {
			continue
		}
		attempt, parseErr := strconv.Atoi(strings.TrimPrefix(entry.Name(), "attempt-"))
		if parseErr != nil || attempt < 1 || attempt >= currentAttempt {
			continue
		}
		path := filepath.Join(stepDirectory, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("prior stream attempt path is not a real directory")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
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

func (b *FileServerBackupTransferBroker) stagingUsageLocked() (int64, error) {
	var used int64
	err := filepath.WalkDir(b.root, func(path string, entry os.DirEntry, walkErr error) error {
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
		if used > b.maxStagingBytes || info.Size() > b.maxStagingBytes-used {
			return errors.New("existing backup staging data exceeds the configured budget")
		}
		used += info.Size()
		encoded, readErr := os.ReadFile(path + ".json")
		if readErr != nil {
			return errors.New("staged byte file has no matching transfer record")
		}
		var record stagedStreamRecord
		if json.Unmarshal(encoded, &record) != nil {
			return errors.New("staged byte file has an invalid transfer record")
		}
		if !record.StoresBytes || record.OperationID == "" || record.StepID == "" || record.Attempt < 1 ||
			record.DataSetID == "" || record.MaximumBytes < info.Size() ||
			record.MaximumBytes < 1 || record.MaximumBytes > b.maxStagingBytes || record.ExpiresAt.IsZero() ||
			!isSHA256(record.InputSHA256) {
			return errors.New("staged byte file has invalid size bounds or identity metadata")
		}
		if record.Complete && (record.ActualSize != info.Size() || record.ActualSize > record.MaximumBytes || !isSHA256(record.ActualSHA256)) {
			return errors.New("completed staged bytes do not match their transfer record")
		}
		if record.StoresBytes && !record.Complete {
			reserved := record.MaximumBytes - info.Size()
			if reserved > 0 {
				if used > b.maxStagingBytes || reserved > b.maxStagingBytes-used {
					return errors.New("existing backup staging reservation exceeds the configured budget")
				}
				used += reserved
			}
		}
		if used > b.maxStagingBytes {
			return errors.New("existing backup staging data exceeds the configured budget")
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return used, nil
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

type stagedStreamWriter struct {
	mu       sync.Mutex
	file     *os.File
	metaPath string
	record   stagedStreamRecord
	hasher   hash.Hash
	actual   int64
	failed   bool
	closed   bool
}

func newStagedStreamWriter(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamWriter, error) {
	if file == nil || metaPath == "" || !record.StoresBytes || record.Direction != "adapter_to_controller" {
		return nil, errors.New("valid bounded stream sink is required")
	}
	return &stagedStreamWriter{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}, nil
}

func (writer *stagedStreamWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	if !time.Now().UTC().Before(writer.record.ExpiresAt) {
		writer.failed = true
		return 0, ErrStreamExpired
	}
	if int64(len(value)) > writer.record.MaximumBytes-writer.actual {
		writer.failed = true
		return 0, ErrStreamTooLarge
	}
	written, err := writer.file.Write(value)
	if written > 0 {
		_, _ = writer.hasher.Write(value[:written])
		writer.actual += int64(written)
	}
	if err != nil {
		writer.failed = true
	}
	return written, err
}

func (writer *stagedStreamWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	if err := writer.file.Sync(); err != nil {
		writer.failed = true
	}
	if err := writer.file.Close(); err != nil {
		writer.failed = true
	}
	digest := hex.EncodeToString(writer.hasher.Sum(nil))
	if !time.Now().UTC().Before(writer.record.ExpiresAt) ||
		(writer.record.ExpectedSize >= 0 && writer.actual != writer.record.ExpectedSize) ||
		(writer.record.ExpectedSHA256 != "" && digest != writer.record.ExpectedSHA256) {
		writer.failed = true
	}
	writer.record.Complete = !writer.failed
	writer.record.ActualSize = writer.actual
	writer.record.ActualSHA256 = digest
	if err := writeStagedStreamRecord(writer.metaPath, writer.record); err != nil {
		return err
	}
	if writer.failed {
		return errors.New("bounded stream sink was incomplete, expired, oversized, or mismatched")
	}
	return nil
}

type stagedStreamReader struct {
	mu       sync.Mutex
	file     *os.File
	metaPath string
	record   stagedStreamRecord
	hasher   hash.Hash
	actual   int64
	eof      bool
	failed   bool
	closed   bool
}

func newStagedStreamReader(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamReader, error) {
	if file == nil || metaPath == "" || record.StoresBytes || record.Direction != "controller_to_adapter" ||
		record.ExpectedSize < 0 {
		return nil, errors.New("valid bounded stream source is required")
	}
	return &stagedStreamReader{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}, nil
}

func (reader *stagedStreamReader) Read(value []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.closed {
		return 0, os.ErrClosed
	}
	if !time.Now().UTC().Before(reader.record.ExpiresAt) {
		reader.failed = true
		return 0, ErrStreamExpired
	}
	read, err := reader.file.Read(value)
	if read > 0 {
		if int64(read) > reader.record.MaximumBytes-reader.actual {
			reader.failed = true
			return 0, ErrStreamTooLarge
		}
		_, _ = reader.hasher.Write(value[:read])
		reader.actual += int64(read)
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
	if reader.closed {
		return nil
	}
	reader.closed = true
	closeErr := reader.file.Close()
	digest := hex.EncodeToString(reader.hasher.Sum(nil))
	complete := reader.eof && !reader.failed && closeErr == nil &&
		time.Now().UTC().Before(reader.record.ExpiresAt) &&
		reader.actual == reader.record.ExpectedSize &&
		(reader.record.ExpectedSHA256 == "" || digest == reader.record.ExpectedSHA256)
	reader.record.Complete = complete
	reader.record.ActualSize = reader.actual
	reader.record.ActualSHA256 = digest
	if err := writeStagedStreamRecord(reader.metaPath, reader.record); err != nil {
		return err
	}
	if !complete {
		if closeErr != nil {
			return closeErr
		}
		return errors.New("bounded stream source was incomplete, expired, oversized, or mismatched")
	}
	return nil
}
