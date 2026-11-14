package workflow

// Shared helpers for the file stream broker characterization tests. The tests
// pin exact error text, the order of checks, the transfers issued, and which
// staging files and handles exist after every success and failure path.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	brokerR3Env             = "staging"
	brokerR3Sink            = "adapter_to_controller"
	brokerR3Source          = "controller_to_adapter"
	brokerR3ErrInactive     = "active server.backup stream step and context are required"
	brokerR3ErrNoDeadline   = "stream invocation deadline is required"
	brokerR3ErrDirection    = "stream direction does not match the exact backup capability"
	brokerR3ErrExpired      = "stream invocation deadline has expired"
	brokerR3ErrSinkCount    = "one sink stream is allowed per backup capture or readback step"
	brokerR3ErrBounds       = "stream expectation is outside the configured operation and transfer bounds"
	brokerR3ErrRepeat       = "backup stream step repeats a data set"
	brokerR3ErrBudget       = "backup stream staging budget is exhausted"
	brokerR3ErrMetaExists   = "stream attempt already has transfer metadata"
	brokerR3ErrNoSource     = "stream source step has no completed attempt"
	brokerR3ErrSourceRecord = "verified staged source bytes are unavailable or do not match the transfer expectation"
	brokerR3ErrSourceHash   = "staged source bytes failed independent size or checksum verification"
	brokerR3ErrCapture      = "capture stream is not bound to the exact operation target and data set"
	brokerR3ErrStorage      = "storage stream is not bound to the exact expected size and checksum"
	brokerR3ErrPutEnv       = "storage upload target crosses its operation environment"
	brokerR3ErrGetEnv       = "storage readback reference crosses its operation environment"
	brokerR3ErrGameBinding  = "game validation stream is not bound to its operation and environment"
	brokerR3ErrGameCount    = "game validation stream count differs from the manifest object count"
	brokerR3ErrGameObject   = "game validation stream is not bound to the corresponding exact manifest object"
	brokerR3ErrPathIDs      = "operation, step, attempt, and data-set IDs are required for a stream path"
	brokerR3ErrInvocation   = "backup stream was not fully consumed or did not match its exact integrity expectation"
	brokerR3ErrInvokeHash   = "staged backup bytes failed independent size or checksum verification"
	brokerR3ErrCompleted    = "completed server.backup stream step and context are required"
	brokerR3ErrEvidence     = "stream completion evidence does not match the exact operation, step, data set, size, and checksum"
	brokerR3ErrNoEvidence   = "stream completion evidence is missing"
	brokerR3ErrStagedBytes  = "staged stream bytes do not match the adapter output and trusted expectation"
)

func brokerR3New(t *testing.T, maxBytes int64) *FileServerBackupTransferBroker {
	t.Helper()
	broker, err := NewFileServerBackupTransferBroker(filepath.Join(t.TempDir(), "staging"), maxBytes, time.Minute, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func brokerR3Ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// brokerR3FixedDeadline reports a deadline without ever being cancelled, so a
// past deadline reaches the broker's own expiry check.
type brokerR3FixedDeadline struct {
	context.Context
	deadline time.Time
}

func (c brokerR3FixedDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func brokerR3Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func brokerR3Expect(dataSetID string, data []byte, maximum int64) ServerBackupStreamExpectation {
	return ServerBackupStreamExpectation{
		DataSetID: dataSetID, SizeBytes: int64(len(data)), MaximumBytes: maximum, SHA256: brokerR3Digest(data),
	}
}

func brokerR3JSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// brokerR3StorageStep builds a running storage step whose input binds the
// expected size and checksum to the fixture environment.
func brokerR3StorageStep(t *testing.T, capability, stepID string, size int64, checksum string) store.OperationStep {
	t.Helper()
	input := map[string]any{
		"expected_size_bytes": size,
		"expected_checksum":   map[string]any{"algorithm": "sha256", "digest": checksum},
	}
	bound := map[string]any{"environment_id": brokerR3Env}
	if capability == "storage.object.put" {
		input["target"] = bound
	} else {
		input["object_ref"] = bound
	}
	return store.OperationStep{
		StepID: stepID, Status: protocolstate.StepRunning, Attempt: 1,
		AdapterID: "storage.fake", CapabilityName: capability, Input: brokerR3JSON(t, input),
	}
}

func brokerR3ReadbackStep(t *testing.T, expectation ServerBackupStreamExpectation) store.OperationStep {
	return brokerR3StorageStep(t, "storage.object.get", backupDataStepID("readback", expectation.DataSetID),
		expectation.SizeBytes, expectation.SHA256)
}

func brokerR3UploadStep(t *testing.T, expectation ServerBackupStreamExpectation) store.OperationStep {
	return brokerR3StorageStep(t, "storage.object.put", backupDataStepID("upload", expectation.DataSetID),
		expectation.SizeBytes, expectation.SHA256)
}

func brokerR3ValidateStep(t *testing.T, operation store.OperationRecord, expectations []ServerBackupStreamExpectation) store.OperationStep {
	t.Helper()
	objects := make([]any, 0, len(expectations))
	streams := make([]any, 0, len(expectations))
	for _, expectation := range expectations {
		ref := map[string]any{
			"adapter_id": "storage.fake", "environment_id": brokerR3Env,
			"resource_kind": "object", "external_id": "object-" + expectation.DataSetID,
		}
		objects = append(objects, map[string]any{
			"data_set_id": expectation.DataSetID, "size_bytes": expectation.SizeBytes,
			"checksum": expectation.SHA256, "object_ref": ref,
		})
		streams = append(streams, map[string]any{"object_ref": ref})
	}
	input := map[string]any{
		"backup_id": operation.OperationID, "environment_id": brokerR3Env,
		"manifest": map[string]any{"objects": objects}, "object_streams": streams,
	}
	return store.OperationStep{
		StepID: "validate-game-backup", Status: protocolstate.StepRunning, Attempt: 1,
		AdapterID: "game.fake", CapabilityName: "game.backup.validate", Input: brokerR3JSON(t, input),
	}
}

// brokerR3Rewrite decodes a step's input, applies mutate, and re-encodes it.
func brokerR3Rewrite(t *testing.T, step store.OperationStep, mutate func(map[string]any)) store.OperationStep {
	t.Helper()
	var input map[string]any
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatal(err)
	}
	mutate(input)
	step.Input = brokerR3JSON(t, input)
	return step
}

// brokerR3Call holds the arguments of one IssueStepStreamTransfers call.
type brokerR3Call struct {
	ctx          context.Context
	operation    store.OperationRecord
	step         store.OperationStep
	expectations []ServerBackupStreamExpectation
	direction    string
}

func brokerR3CaptureCall(t *testing.T) brokerR3Call {
	operation, step := captureStreamFixture()
	return brokerR3Call{
		ctx: brokerR3Ctx(t), operation: operation, step: step, direction: brokerR3Sink,
		expectations: []ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: -1, MaximumBytes: 8}},
	}
}

func brokerR3StepCall(t *testing.T, operation store.OperationRecord, step store.OperationStep, expectations ...ServerBackupStreamExpectation) brokerR3Call {
	direction, _, err := backupStreamRoute(step)
	if err != nil {
		t.Fatal(err)
	}
	return brokerR3Call{ctx: brokerR3Ctx(t), operation: operation, step: step, expectations: expectations, direction: direction}
}

// issue calls the broker and closes every issued stream when the test ends.
func (c brokerR3Call) issue(t *testing.T, broker *FileServerBackupTransferBroker) ([]adapter.StreamTransfer, error) {
	t.Helper()
	transfers, err := broker.IssueStepStreamTransfers(c.ctx, c.operation, c.step, c.expectations, c.direction)
	brokerR3ReleaseAll(t, transfers)
	return transfers, err
}

func brokerR3IssueOK(t *testing.T, broker *FileServerBackupTransferBroker, call brokerR3Call) []adapter.StreamTransfer {
	t.Helper()
	transfers, err := call.issue(t, broker)
	if err != nil || len(transfers) != len(call.expectations) {
		t.Fatalf("IssueStepStreamTransfers = %d transfers, %v; want %d", len(transfers), err, len(call.expectations))
	}
	return transfers
}

// brokerR3IssueFails pins the error and that the staging tree is unchanged.
func brokerR3IssueFails(t *testing.T, broker *FileServerBackupTransferBroker, call brokerR3Call, want string) {
	t.Helper()
	before := brokerR3Tree(t, broker.root)
	transfers, err := call.issue(t, broker)
	brokerR3WantErr(t, err, want)
	if transfers != nil {
		t.Fatalf("transfers = %v, want nil", transfers)
	}
	brokerR3SameTree(t, broker.root, before)
}

// brokerR3ReleaseAll closes issued streams at test end. Closing an unused
// stream reports it incomplete, which is expected here, so the error is logged.
func brokerR3ReleaseAll(t *testing.T, transfers []adapter.StreamTransfer) {
	t.Cleanup(func() {
		for _, transfer := range transfers {
			brokerR3CloseLogged(t, transfer.Source)
			brokerR3CloseLogged(t, transfer.Sink)
		}
	})
}

func brokerR3CloseLogged(t *testing.T, closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		t.Logf("release stream: %v", err)
	}
}

// brokerR3Data is the byte content staged for a data set.
func brokerR3Data(dataSetID string) []byte { return []byte("bytes-of-" + dataSetID) }

// brokerR3Staged is the trusted expectation for bytes staged by brokerR3Stage.
func brokerR3Staged(dataSetID string) ServerBackupStreamExpectation {
	return brokerR3Expect(dataSetID, brokerR3Data(dataSetID), 16)
}

// brokerR3Stage runs a sink step for dataSetID to completion and records it as
// succeeded in the operation.
func brokerR3Stage(
	t *testing.T, broker *FileServerBackupTransferBroker, operation *store.OperationRecord,
	step store.OperationStep, dataSetID string,
) store.OperationStep {
	t.Helper()
	transfers := brokerR3IssueOK(t, broker, brokerR3StepCall(t, *operation, step, brokerR3Staged(dataSetID)))
	if _, err := transfers[0].Sink.Write(brokerR3Data(dataSetID)); err != nil {
		t.Fatal(err)
	}
	if err := transfers[0].Sink.Close(); err != nil {
		t.Fatal(err)
	}
	step.Status = protocolstate.StepSucceeded
	operation.Steps = append(operation.Steps, step)
	return step
}

// brokerR3Captured stages completed capture bytes for the "world" data set.
func brokerR3Captured(t *testing.T, broker *FileServerBackupTransferBroker) (store.OperationRecord, store.OperationStep, ServerBackupStreamExpectation) {
	t.Helper()
	operation, capture := captureStreamFixture()
	capture = brokerR3Stage(t, broker, &operation, capture, "world")
	return operation, capture, brokerR3Staged("world")
}

// brokerR3ReadbackOperation stages completed readback bytes for each data set.
func brokerR3ReadbackOperation(t *testing.T, broker *FileServerBackupTransferBroker, dataSets ...string) (store.OperationRecord, []ServerBackupStreamExpectation) {
	t.Helper()
	operation, _ := captureStreamFixture()
	expectations := make([]ServerBackupStreamExpectation, 0, len(dataSets))
	for _, dataSet := range dataSets {
		expectation := brokerR3Staged(dataSet)
		brokerR3Stage(t, broker, &operation, brokerR3ReadbackStep(t, expectation), dataSet)
		expectations = append(expectations, expectation)
	}
	return operation, expectations
}

func brokerR3Paths(t *testing.T, broker *FileServerBackupTransferBroker, operationID string, step store.OperationStep, dataSetID string) (string, string) {
	t.Helper()
	dataPath, metaPath, err := broker.streamPaths(operationID, step.StepID, step.Attempt, dataSetID)
	if err != nil {
		t.Fatal(err)
	}
	return dataPath, metaPath
}

func brokerR3Tree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if entry.IsDir() {
			rel += "/"
		}
		paths = append(paths, rel)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func brokerR3SameTree(t *testing.T, root string, before []string) {
	t.Helper()
	if after := brokerR3Tree(t, root); !slices.Equal(before, after) {
		t.Fatalf("staging tree changed:\nbefore=%v\nafter =%v", before, after)
	}
}

func brokerR3Names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// brokerR3OpenHandles counts this process's open descriptors on path, or -1
// when the platform does not expose them.
func brokerR3OpenHandles(path string) int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && (target == path || target == path+" (deleted)") {
			count++
		}
	}
	return count
}

func brokerR3WantHandles(t *testing.T, path string, want int) {
	t.Helper()
	if got := brokerR3OpenHandles(path); got != -1 && got != want {
		t.Fatalf("open handles on %s = %d, want %d", path, got, want)
	}
}

func brokerR3WantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// brokerR3WantResult expects success when want is empty, else the exact error.
func brokerR3WantResult(t *testing.T, err error, want string) {
	t.Helper()
	if want != "" {
		brokerR3WantErr(t, err, want)
		return
	}
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}

func brokerR3Exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if got := err == nil; got != want {
		t.Fatalf("%s exists = %v (err=%v), want %v", path, got, err, want)
	}
}

func brokerR3WantContent(t *testing.T, path, want string) {
	t.Helper()
	if content, err := os.ReadFile(path); err != nil || string(content) != want {
		t.Fatalf("%s = %q, %v; want %q", path, content, err, want)
	}
}

// brokerR3PlantOperationDir creates an operation staging directory and backdates it.
func brokerR3PlantOperationDir(t *testing.T, broker *FileServerBackupTransferBroker, operationID string, age time.Duration) string {
	t.Helper()
	dir := broker.operationDirectory(operationID)
	file := filepath.Join(dir, "leftover")
	brokerR3Write(t, file, []byte("x"))
	stamp := time.Now().Add(-age)
	for _, path := range []string{file, dir} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func brokerR3ReadRecord(t *testing.T, metaPath string) stagedStreamRecord {
	t.Helper()
	record, err := readStagedStreamRecord(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// brokerR3WantRecord compares a stored record, treating ExpiresAt by instant.
func brokerR3WantRecord(t *testing.T, metaPath string, want stagedStreamRecord) {
	t.Helper()
	record := brokerR3ReadRecord(t, metaPath)
	if !record.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("record ExpiresAt = %v, want %v", record.ExpiresAt, want.ExpiresAt)
	}
	record.ExpiresAt = want.ExpiresAt
	if record != want {
		t.Fatalf("record = %+v, want %+v", record, want)
	}
}

func brokerR3WriteRecord(t *testing.T, metaPath string, record stagedStreamRecord) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(metaPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeStagedStreamRecord(metaPath, record); err != nil {
		t.Fatal(err)
	}
}

// brokerR3CompleteRecord is the record a fully consumed stream leaves behind.
func brokerR3CompleteRecord(broker *FileServerBackupTransferBroker, operation store.OperationRecord, step store.OperationStep, expectation ServerBackupStreamExpectation, storesBytes bool) stagedStreamRecord {
	direction := brokerR3Source
	if storesBytes {
		direction = brokerR3Sink
	}
	record := broker.newRecord(operation, step, expectation, direction, time.Now().Add(time.Minute), storesBytes)
	record.Complete, record.ActualSize, record.ActualSHA256 = true, expectation.SizeBytes, expectation.SHA256
	return record
}

func brokerR3Write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// brokerR3WantReference checks one issued transfer's reference and stream side.
func brokerR3WantReference(t *testing.T, transfer adapter.StreamTransfer, direction string, maximum int64, sink bool) {
	t.Helper()
	reference := transfer.Reference
	if !isSafeStreamKey(reference.TransferID) || reference.Direction != direction || reference.MaximumBytes != maximum {
		t.Fatalf("reference = %+v, want direction %s and maximum %d", reference, direction, maximum)
	}
	if (transfer.Sink != nil) != sink || (transfer.Source != nil) == sink || transfer.Verify == nil {
		t.Fatalf("transfer streams = sink %v source %v verify %v, want sink=%v", transfer.Sink, transfer.Source, transfer.Verify != nil, sink)
	}
}

// brokerR3DrainSource reads a source stream to the end, closes it, and checks
// that the staged source file is held open only until then.
func brokerR3DrainSource(t *testing.T, transfer adapter.StreamTransfer, sourcePath, want string) {
	t.Helper()
	brokerR3WantHandles(t, sourcePath, 1)
	content, err := io.ReadAll(transfer.Source)
	if err != nil || string(content) != want {
		t.Fatalf("source = %q, %v; want %q", content, err, want)
	}
	if err := transfer.Source.Close(); err != nil {
		t.Fatal(err)
	}
	brokerR3WantHandles(t, sourcePath, 0)
}
