package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestFileBackupBrokerBoundsStreamsAndCleansOperationArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-staging")
	broker, err := NewFileServerBackupTransferBroker(root, 8, time.Minute, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, step := captureStreamFixture()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	transfers, err := broker.IssueStepStreamTransfers(ctx, operation, step,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: -1, MaximumBytes: 8}}, "adapter_to_controller")
	if err != nil || len(transfers) != 1 || transfers[0].Sink == nil {
		t.Fatalf("IssueStepStreamTransfers = %d transfers, %v", len(transfers), err)
	}
	writer := transfers[0].Sink
	if n, err := writer.Write([]byte("12345678")); err != nil || n != 8 {
		t.Fatalf("write within bound = %d, %v", n, err)
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, ErrStreamTooLarge) {
		t.Fatalf("oversized write error = %v, want ErrStreamTooLarge", err)
	}
	if err := writer.Close(); err == nil {
		t.Fatal("closing an oversized stream unexpectedly succeeded")
	}
	if usage, err := broker.stagingUsageLocked(); err != nil || usage != 8 {
		t.Fatalf("staging usage = %d, err=%v; want the exact 8-byte cap", usage, err)
	}
	if err := broker.CleanupOperation(operation.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(broker.operationDirectory(operation.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation staging remains after cleanup: %v", err)
	}
}

func TestFileBackupBrokerChecksCaptureIntegrityAndStorageSource(t *testing.T) {
	broker, err := NewFileServerBackupTransferBroker(filepath.Join(t.TempDir(), "private-staging"), 64, time.Minute, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, captureStep := captureStreamFixture()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data := []byte("world-data")
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	transfers, err := broker.IssueStepStreamTransfers(ctx, operation, captureStep,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: int64(len(data)), MaximumBytes: 32}}, "adapter_to_controller")
	if err != nil || len(transfers) != 1 || transfers[0].Sink == nil {
		t.Fatalf("issue capture stream: transfers=%d err=%v", len(transfers), err)
	}
	if _, err := transfers[0].Sink.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := transfers[0].Sink.Close(); err != nil {
		t.Fatal(err)
	}
	captureStep.Status = protocolstate.StepSucceeded
	operation.Steps = append(operation.Steps, captureStep)
	if err := broker.VerifyStepStreams(ctx, operation, captureStep,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: int64(len(data)), MaximumBytes: 32, SHA256: checksum}}); err != nil {
		t.Fatalf("verify completed capture: %v", err)
	}

	putInput := map[string]any{
		"expected_size_bytes": len(data),
		"expected_checksum":   map[string]any{"algorithm": "sha256", "digest": checksum},
		"target":              map[string]any{"environment_id": operation.Target.EnvironmentID},
	}
	encodedInput, err := json.Marshal(putInput)
	if err != nil {
		t.Fatal(err)
	}
	putStep := store.OperationStep{
		StepID: backupDataStepID("upload", "world"), Status: protocolstate.StepRunning, Attempt: 1,
		AdapterID: "storage.fake", CapabilityName: "storage.object.put", Input: encodedInput,
	}
	operation.Steps = append(operation.Steps, putStep)
	transfers, err = broker.IssueStepStreamTransfers(ctx, operation, putStep,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: int64(len(data)), MaximumBytes: 32, SHA256: checksum}}, "controller_to_adapter")
	if err != nil || len(transfers) != 1 || transfers[0].Source == nil {
		t.Fatalf("issue storage upload stream: transfers=%d err=%v", len(transfers), err)
	}
	reader := transfers[0].Source
	readBack, err := io.ReadAll(reader)
	if err != nil || string(readBack) != string(data) {
		t.Fatalf("upload stream bytes = %q, err=%v", readBack, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	putStep.Status = protocolstate.StepSucceeded
	if err := broker.VerifyStepStreams(ctx, operation, putStep,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: int64(len(data)), MaximumBytes: 32, SHA256: checksum}}); err != nil {
		t.Fatalf("verify storage upload stream: %v", err)
	}
}

func TestFileBackupBrokerReclaimsPriorSafeReadbackAttempt(t *testing.T) {
	broker, err := NewFileServerBackupTransferBroker(filepath.Join(t.TempDir(), "private-staging"), 16, time.Minute, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := captureStreamFixture()
	data := []byte("old!")
	digest := sha256.Sum256(data)
	checksum := hex.EncodeToString(digest[:])
	stepID := backupDataStepID("readback", "world")
	priorData, _, err := broker.streamPaths(operation.OperationID, stepID, 1, "world")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(priorData), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priorData, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"object_ref":          map[string]any{"environment_id": operation.Target.EnvironmentID},
		"expected_size_bytes": len(data),
		"expected_checksum":   map[string]any{"algorithm": "sha256", "digest": checksum},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := store.OperationStep{
		StepID: stepID, Status: protocolstate.StepRunning, Attempt: 2,
		AdapterID: "storage.fake", CapabilityName: "storage.object.get", Input: input,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	transfers, err := broker.IssueStepStreamTransfers(ctx, operation, step,
		[]ServerBackupStreamExpectation{{DataSetID: "world", SizeBytes: int64(len(data)), MaximumBytes: int64(len(data)), SHA256: checksum}}, "adapter_to_controller")
	if err != nil || len(transfers) != 1 || transfers[0].Sink == nil {
		t.Fatalf("issue retried readback sink: transfers=%d, err=%v", len(transfers), err)
	}
	if _, err := transfers[0].Sink.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := transfers[0].Sink.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(priorData)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prior safe readback attempt still occupies staging: %v", err)
	}
}

func captureStreamFixture() (store.OperationRecord, store.OperationStep) {
	instanceRef := json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)
	unitRef := json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`)
	inspectInput, _ := json.Marshal(map[string]any{"instance_ref": json.RawMessage(instanceRef)})
	operation := store.OperationRecord{
		OperationID: "backup-operation", OperationType: "server.backup",
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		Steps:  []store.OperationStep{{StepID: backupInspectInstanceStep, Input: inspectInput}},
	}
	target, _ := json.Marshal(map[string]any{
		"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID,
		"instance_ref": json.RawMessage(instanceRef),
	})
	input, _ := json.Marshal(map[string]any{
		"backup_id": operation.OperationID, "data_set_id": "world", "target": json.RawMessage(target),
		"unit_ref": json.RawMessage(unitRef),
	})
	step := store.OperationStep{
		StepID: backupDataStepID("capture", "world"), Status: protocolstate.StepRunning, Attempt: 1,
		AdapterID: "game.fake", CapabilityName: "game.backup.capture", Input: input,
	}
	return operation, step
}
