package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBrokerR3IssueRejectsInactiveStepsBeforeSweeping(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	many := func(count int) func(*brokerR3Call) {
		return func(c *brokerR3Call) {
			for len(c.expectations) < count {
				c.expectations = append(c.expectations, c.expectations[0])
			}
		}
	}
	cases := map[string]func(*brokerR3Call){
		"nil context":       func(c *brokerR3Call) { c.ctx = nil },
		"cancelled context": func(c *brokerR3Call) { c.ctx = cancelled },
		"operation id":      func(c *brokerR3Call) { c.operation.OperationID = "" },
		"operation type":    func(c *brokerR3Call) { c.operation.OperationType = "server.restore" },
		"step id":           func(c *brokerR3Call) { c.step.StepID = "" },
		"attempt":           func(c *brokerR3Call) { c.step.Attempt = 0 },
		"status":            func(c *brokerR3Call) { c.step.Status = protocolstate.StepSucceeded },
		"no expectations":   func(c *brokerR3Call) { c.expectations = nil },
		"129 expectations":  many(129),
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			broker := brokerR3New(t, 8)
			expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
			call := brokerR3CaptureCall(t)
			mutate(&call)
			brokerR3IssueFails(t, broker, call, brokerR3ErrInactive)
			brokerR3Exists(t, expired, true)
		})
	}
	t.Run("nil broker", func(t *testing.T) {
		var broker *FileServerBackupTransferBroker
		call := brokerR3CaptureCall(t)
		transfers, err := broker.IssueStepStreamTransfers(call.ctx, call.operation, call.step, call.expectations, call.direction)
		brokerR3WantErr(t, err, brokerR3ErrInactive)
		brokerR3ReleaseAll(t, transfers)
	})
	t.Run("128 expectations pass the guard", func(t *testing.T) {
		call := brokerR3CaptureCall(t)
		many(128)(&call)
		brokerR3IssueFails(t, brokerR3New(t, 8), call, brokerR3ErrSinkCount)
	})
}

func TestBrokerR3IssueChecksDeadlineThenRouteThenExpiryBeforeSweeping(t *testing.T) {
	pastDeadline := func(c *brokerR3Call) {
		c.ctx = brokerR3FixedDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	}
	unknown := func(c *brokerR3Call) { c.step.CapabilityName = "game.backup.unknown" }
	cases := []struct {
		name   string
		mutate []func(*brokerR3Call)
		want   string
	}{
		{"no deadline", []func(*brokerR3Call){func(c *brokerR3Call) { c.ctx = context.Background() }}, brokerR3ErrNoDeadline},
		{"no deadline wins over unknown capability", []func(*brokerR3Call){unknown, func(c *brokerR3Call) { c.ctx = context.Background() }}, brokerR3ErrNoDeadline},
		{"unknown capability", []func(*brokerR3Call){unknown}, brokerR3ErrDirection},
		{"capture with source direction", []func(*brokerR3Call){func(c *brokerR3Call) { c.direction = brokerR3Source }}, brokerR3ErrDirection},
		{"capture with empty direction", []func(*brokerR3Call){func(c *brokerR3Call) { c.direction = "" }}, brokerR3ErrDirection},
		{"upload with sink direction", []func(*brokerR3Call){func(c *brokerR3Call) {
			c.step = brokerR3UploadStep(t, brokerR3Staged("world"))
		}}, brokerR3ErrDirection},
		{"deadline already passed", []func(*brokerR3Call){pastDeadline}, brokerR3ErrExpired},
		{"bad route wins over a passed deadline", []func(*brokerR3Call){pastDeadline, func(c *brokerR3Call) { c.direction = brokerR3Source }}, brokerR3ErrDirection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 8)
			expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
			call := brokerR3CaptureCall(t)
			for _, mutate := range tc.mutate {
				mutate(&call)
			}
			brokerR3IssueFails(t, broker, call, tc.want)
			brokerR3Exists(t, expired, true)
		})
	}
}

func TestBrokerR3IssueExpiresAtEarlierOfLifetimeAndDeadline(t *testing.T) {
	t.Run("lifetime", func(t *testing.T) {
		call := brokerR3CaptureCall(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		call.ctx = ctx
		earliest := time.Now().UTC().Add(time.Minute)
		transfers := brokerR3IssueOK(t, brokerR3New(t, 8), call)
		latest := time.Now().UTC().Add(time.Minute)
		expiresAt := transfers[0].Reference.ExpiresAt
		if expiresAt.Before(earliest) || expiresAt.After(latest) || expiresAt.Location() != time.UTC {
			t.Fatalf("ExpiresAt = %v, want UTC within [%v, %v]", expiresAt, earliest, latest)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		broker := brokerR3New(t, 8)
		call := brokerR3CaptureCall(t)
		deadline := time.Now().Add(20 * time.Second).In(time.FixedZone("east", 3600))
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		call.ctx = ctx
		expiresAt := brokerR3IssueOK(t, broker, call)[0].Reference.ExpiresAt
		if !expiresAt.Equal(deadline) || expiresAt.Location() != time.UTC {
			t.Fatalf("ExpiresAt = %v, want deadline %v in UTC", expiresAt, deadline)
		}
		_, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		if record := brokerR3ReadRecord(t, metaPath); !record.ExpiresAt.Equal(deadline) {
			t.Fatalf("record ExpiresAt = %v, want %v", record.ExpiresAt, deadline)
		}
	})
}

func TestBrokerR3IssueSweepsExpiredStagingBeforeValidatingInput(t *testing.T) {
	broker := brokerR3New(t, 8)
	expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
	fresh := brokerR3PlantOperationDir(t, broker, "fresh-operation", time.Minute)
	call := brokerR3CaptureCall(t)
	call.step = brokerR3Rewrite(t, call.step, func(input map[string]any) { input["backup_id"] = "other-operation" })
	_, err := call.issue(t, broker)
	brokerR3WantErr(t, err, brokerR3ErrCapture)
	brokerR3Exists(t, expired, false)
	brokerR3Exists(t, fresh, true)
	brokerR3Exists(t, broker.operationDirectory(call.operation.OperationID), false)

	t.Run("sweep failure", func(t *testing.T) {
		broker := brokerR3New(t, 8)
		if err := os.RemoveAll(broker.root); err != nil {
			t.Fatal(err)
		}
		_, err := brokerR3CaptureCall(t).issue(t, broker)
		brokerR3WantErr(t, err, "open "+broker.root+": no such file or directory")
		brokerR3Exists(t, broker.root, false)
	})
}

// brokerR3PlantAttempts fills a step directory with prior attempts and decoys.
func brokerR3PlantAttempts(t *testing.T, broker *FileServerBackupTransferBroker, operationID, stepID string) string {
	t.Helper()
	stepDir := filepath.Join(broker.operationDirectory(operationID), safeStreamKey(stepID))
	for _, dir := range []string{"attempt-1", "attempt-2", "attempt-0", "attempt-4", "attempt-x", "notes"} {
		brokerR3Write(t, filepath.Join(stepDir, dir, "old.part"), []byte("old"))
	}
	brokerR3Write(t, filepath.Join(stepDir, "attempt-01"), []byte("file"))
	return stepDir
}

func TestBrokerR3IssueClearsOnlyPriorReadbackAttempts(t *testing.T) {
	expectation := brokerR3Staged("world")
	readbackCall := func(t *testing.T, attempt int) brokerR3Call {
		operation, _ := captureStreamFixture()
		step := brokerR3ReadbackStep(t, expectation)
		step.Attempt = attempt
		return brokerR3StepCall(t, operation, step, expectation)
	}
	t.Run("readback retry", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := readbackCall(t, 3)
		stepDir := brokerR3PlantAttempts(t, broker, call.operation.OperationID, call.step.StepID)
		brokerR3IssueOK(t, broker, call)
		for _, removed := range []string{"attempt-1", "attempt-2"} {
			brokerR3Exists(t, filepath.Join(stepDir, removed), false)
		}
		for _, kept := range []string{"attempt-0", "attempt-01", "attempt-4", "attempt-x", "notes"} {
			brokerR3Exists(t, filepath.Join(stepDir, kept), true)
		}
		dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		brokerR3Exists(t, dataPath, true)
		brokerR3Exists(t, metaPath, true)
	})
	t.Run("capture retry keeps prior attempts", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CaptureCall(t)
		call.step.Attempt = 3
		stepDir := brokerR3PlantAttempts(t, broker, call.operation.OperationID, call.step.StepID)
		brokerR3IssueOK(t, broker, call)
		brokerR3Exists(t, filepath.Join(stepDir, "attempt-1"), true)
		brokerR3Exists(t, filepath.Join(stepDir, "attempt-2"), true)
	})
	t.Run("input validation precedes clearing", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := readbackCall(t, 3)
		call.step = brokerR3Rewrite(t, call.step, func(input map[string]any) { input["expected_size_bytes"] = 99 })
		brokerR3PlantAttempts(t, broker, call.operation.OperationID, call.step.StepID)
		brokerR3IssueFails(t, broker, call, brokerR3ErrStorage)
	})
	t.Run("clearing failure precedes sink count", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := readbackCall(t, 2)
		call.expectations = append(call.expectations, brokerR3Staged("nether"))
		stepDir := filepath.Join(broker.operationDirectory(call.operation.OperationID), safeStreamKey(call.step.StepID))
		brokerR3Write(t, stepDir, []byte("not a directory"))
		brokerR3IssueFails(t, broker, call, "open "+stepDir+": not a directory")
	})
}

func TestBrokerR3IssueRejectsSinkCountAndExpectationBounds(t *testing.T) {
	digest := brokerR3Digest([]byte("data"))
	readback := func(dataSetID string, size, maximum int64) func(*testing.T) brokerR3Call {
		return func(t *testing.T) brokerR3Call {
			operation, _ := captureStreamFixture()
			step := brokerR3StorageStep(t, "storage.object.get", backupDataStepID("readback", "world"), size, digest)
			return brokerR3StepCall(t, operation, step,
				ServerBackupStreamExpectation{DataSetID: dataSetID, SizeBytes: size, MaximumBytes: maximum, SHA256: digest})
		}
	}
	twoCaptures := func(t *testing.T) brokerR3Call {
		call := brokerR3CaptureCall(t)
		call.expectations = append(call.expectations, ServerBackupStreamExpectation{DataSetID: "nether", SizeBytes: -1, MaximumBytes: 8})
		return call
	}
	unknownSourceSize := func(t *testing.T) brokerR3Call {
		operation, _ := captureStreamFixture()
		step := brokerR3StorageStep(t, "storage.object.put", backupDataStepID("upload", "world"), -1, digest)
		return brokerR3StepCall(t, operation, step,
			ServerBackupStreamExpectation{DataSetID: "world", SizeBytes: -1, MaximumBytes: 8, SHA256: digest})
	}
	cases := []struct {
		name string
		call func(*testing.T) brokerR3Call
		want string
	}{
		{"two capture sinks", twoCaptures, brokerR3ErrSinkCount},
		{"empty data set", readback("", 4, 8), brokerR3ErrBounds},
		{"zero maximum", readback("world", 0, 0), brokerR3ErrBounds},
		{"maximum above budget", readback("world", 4, 17), brokerR3ErrBounds},
		{"size below unknown", readback("world", -2, 8), brokerR3ErrBounds},
		{"size above maximum", readback("world", 9, 8), brokerR3ErrBounds},
		{"unknown size on a source precedes source verification", unknownSourceSize, brokerR3ErrBounds},
		{"unknown size on a sink is allowed", readback("world", -1, 8), ""},
		{"maximum equal to budget is allowed", readback("world", 4, 16), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 16)
			if tc.want == "" {
				brokerR3IssueOK(t, broker, tc.call(t))
				return
			}
			brokerR3IssueFails(t, broker, tc.call(t), tc.want)
		})
	}
}

func TestBrokerR3IssueRejectsRepeatedDataSets(t *testing.T) {
	broker := brokerR3New(t, 256)
	operation, expectations := brokerR3ReadbackOperation(t, broker, "world")
	repeated := []ServerBackupStreamExpectation{expectations[0], expectations[0]}
	step := brokerR3ValidateStep(t, operation, repeated)
	brokerR3IssueFails(t, broker, brokerR3StepCall(t, operation, step, repeated...), brokerR3ErrRepeat)
}

func TestBrokerR3IssueVerifiesEverySourceBeforeOpeningAny(t *testing.T) {
	expectation := brokerR3Staged("world")
	upload := func(steps ...store.OperationStep) func(*testing.T, *FileServerBackupTransferBroker) brokerR3Call {
		return func(t *testing.T, _ *FileServerBackupTransferBroker) brokerR3Call {
			operation, _ := captureStreamFixture()
			operation.Steps = append(operation.Steps, steps...)
			return brokerR3StepCall(t, operation, brokerR3UploadStep(t, expectation), expectation)
		}
	}
	_, running := captureStreamFixture()
	noAttempt, succeeded := running, running
	noAttempt.Status, noAttempt.Attempt = protocolstate.StepSucceeded, 0
	succeeded.Status = protocolstate.StepSucceeded
	cases := []struct {
		name string
		call func(*testing.T, *FileServerBackupTransferBroker) brokerR3Call
		want string
	}{
		{"no capture step", upload(), brokerR3ErrNoSource},
		{"capture still running", upload(running), brokerR3ErrNoSource},
		{"capture succeeded without an attempt", upload(noAttempt), brokerR3ErrNoSource},
		{"capture succeeded without staged bytes", upload(succeeded), brokerR3ErrSourceRecord},
		{"staged record differs from expectation", brokerR3UploadOtherBytes, brokerR3ErrSourceRecord},
		{"staged bytes tampered", brokerR3UploadTampered, brokerR3ErrSourceHash},
		{"second game validation source tampered", brokerR3ValidateTampered, brokerR3ErrSourceHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 256)
			brokerR3IssueFails(t, broker, tc.call(t, broker), tc.want)
		})
	}
}

func brokerR3UploadOtherBytes(t *testing.T, broker *FileServerBackupTransferBroker) brokerR3Call {
	operation, _, _ := brokerR3Captured(t, broker)
	other := brokerR3Expect("world", []byte("other-bytes"), 16)
	return brokerR3StepCall(t, operation, brokerR3UploadStep(t, other), other)
}

func brokerR3UploadTampered(t *testing.T, broker *FileServerBackupTransferBroker) brokerR3Call {
	operation, capture, captured := brokerR3Captured(t, broker)
	dataPath, _ := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
	brokerR3Write(t, dataPath, []byte("BYTES-OF-WORLD"))
	return brokerR3StepCall(t, operation, brokerR3UploadStep(t, captured), captured)
}

func brokerR3ValidateTampered(t *testing.T, broker *FileServerBackupTransferBroker) brokerR3Call {
	operation, expectations := brokerR3ReadbackOperation(t, broker, "world", "nether")
	readback := operationStep(operation, backupDataStepID("readback", "nether"))
	dataPath, _ := brokerR3Paths(t, broker, operation.OperationID, readback, "nether")
	brokerR3Write(t, dataPath, []byte("BYTES-OF-NETHER"))
	return brokerR3StepCall(t, operation, brokerR3ValidateStep(t, operation, expectations), expectations...)
}

func TestBrokerR3IssueEnforcesStagingBudget(t *testing.T) {
	readback := func(t *testing.T, operation store.OperationRecord, maximum int64) brokerR3Call {
		expectation := brokerR3Expect("world", []byte("x"), maximum)
		return brokerR3StepCall(t, operation, brokerR3ReadbackStep(t, expectation), expectation)
	}
	t.Run("completed bytes count by size", func(t *testing.T) {
		broker := brokerR3New(t, 20)
		operation, _, _ := brokerR3Captured(t, broker)
		brokerR3IssueFails(t, broker, readback(t, operation, 7), brokerR3ErrBudget)
		brokerR3IssueOK(t, broker, readback(t, operation, 6))
	})
	t.Run("open sinks reserve their maximum", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		capture := brokerR3CaptureCall(t)
		brokerR3IssueOK(t, broker, capture)
		brokerR3IssueFails(t, broker, readback(t, capture.operation, 9), brokerR3ErrBudget)
		brokerR3IssueOK(t, broker, readback(t, capture.operation, 8))
	})
	t.Run("unreadable staging usage", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		brokerR3Write(t, filepath.Join(broker.root, "orphan.bin"), []byte("x"))
		operation, _ := captureStreamFixture()
		brokerR3IssueFails(t, broker, readback(t, operation, 4), "staged byte file has no matching transfer record")
	})
}
