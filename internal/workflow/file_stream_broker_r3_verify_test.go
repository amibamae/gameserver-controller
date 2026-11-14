package workflow

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// brokerR3Invocation holds the arguments of one verifyInvocationStream call.
type brokerR3Invocation struct {
	operation   store.OperationRecord
	step        store.OperationStep
	expectation ServerBackupStreamExpectation
	direction   string
	storesBytes bool
}

func brokerR3ReadbackInvocation(t *testing.T) brokerR3Invocation {
	operation, _ := captureStreamFixture()
	expectation := brokerR3Expect("world", []byte("data"), 8)
	return brokerR3Invocation{
		operation: operation, step: brokerR3ReadbackStep(t, expectation), expectation: expectation,
		direction: brokerR3Sink, storesBytes: true,
	}
}

func (c brokerR3Invocation) verify(broker *FileServerBackupTransferBroker) error {
	return broker.verifyInvocationStream(c.operation, c.step, c.expectation, c.direction, c.storesBytes)
}

func TestBrokerR3VerifyInvocationStreamGuardsAndInput(t *testing.T) {
	const guard = "running server.backup stream and trusted expectation are required"
	cases := []struct {
		name   string
		mutate func(*brokerR3Invocation)
		want   string
	}{
		{"operation type", func(c *brokerR3Invocation) { c.operation.OperationType = "server.restore" }, guard},
		{"status", func(c *brokerR3Invocation) { c.step.Status = protocolstate.StepSucceeded }, guard},
		{"attempt", func(c *brokerR3Invocation) { c.step.Attempt = 0 }, guard},
		{"data set", func(c *brokerR3Invocation) { c.expectation.DataSetID = "" }, guard},
		{"input", func(c *brokerR3Invocation) {
			c.step = brokerR3Rewrite(t, c.step, func(input map[string]any) { input["expected_size_bytes"] = 5 })
		}, brokerR3ErrStorage},
		{"capability", func(c *brokerR3Invocation) { c.step.CapabilityName = "game.backup.unknown" }, "unsupported stream capability"},
		{"stream path", func(c *brokerR3Invocation) { c.step.StepID = "" }, brokerR3ErrPathIDs},
		{"missing record", func(*brokerR3Invocation) {}, brokerR3ErrInvocation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := brokerR3ReadbackInvocation(t)
			tc.mutate(&call)
			brokerR3WantErr(t, call.verify(brokerR3New(t, 16)), tc.want)
		})
	}
	t.Run("nil broker", func(t *testing.T) {
		brokerR3WantErr(t, brokerR3ReadbackInvocation(t).verify(nil), guard)
	})
}

func TestBrokerR3VerifyInvocationStreamRecords(t *testing.T) {
	other := brokerR3Digest([]byte("other"))
	cases := []struct {
		name      string
		mutate    func(*stagedStreamRecord)
		keepBytes bool
		want      string
	}{
		{"complete", func(*stagedStreamRecord) {}, true, ""},
		{"unknown expected size", func(r *stagedStreamRecord) { r.ExpectedSize = -1 }, true, ""},
		{"no expected checksum", func(r *stagedStreamRecord) { r.ExpectedSHA256 = "" }, true, ""},
		{"identity mismatch", func(r *stagedStreamRecord) { r.AdapterID = "other" }, true, brokerR3ErrInvocation},
		{"incomplete", func(r *stagedStreamRecord) { r.Complete = false }, true, brokerR3ErrInvocation},
		{"negative size", func(r *stagedStreamRecord) { r.ActualSize = -1 }, true, brokerR3ErrInvocation},
		{"size above maximum", func(r *stagedStreamRecord) { r.ExpectedSize, r.ActualSize = -1, 9 }, true, brokerR3ErrInvocation},
		{"size differs from expected", func(r *stagedStreamRecord) { r.ActualSize = 3 }, true, brokerR3ErrInvocation},
		{"checksum differs from expected", func(r *stagedStreamRecord) { r.ActualSHA256 = other }, true, brokerR3ErrInvocation},
		{"bytes differ from record", func(r *stagedStreamRecord) {
			r.ExpectedSize, r.ExpectedSHA256, r.ActualSize, r.ActualSHA256 = -1, "", 3, brokerR3Digest([]byte("dat"))
		}, true, brokerR3ErrInvokeHash},
		{"bytes missing", func(*stagedStreamRecord) {}, false, brokerR3ErrInvokeHash},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 16)
			call := brokerR3ReadbackInvocation(t)
			dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
			if tc.keepBytes {
				brokerR3Write(t, dataPath, []byte("data"))
			}
			record := brokerR3CompleteRecord(broker, call.operation, call.step, call.expectation, true)
			tc.mutate(&record)
			brokerR3WriteRecord(t, metaPath, record)
			brokerR3WantResult(t, call.verify(broker), tc.want)
		})
	}
}

func TestBrokerR3VerifyInvocationSourceNeedsNoBytes(t *testing.T) {
	broker := brokerR3New(t, 16)
	call := brokerR3ReadbackInvocation(t)
	call.step = brokerR3UploadStep(t, call.expectation)
	call.direction, call.storesBytes = brokerR3Source, false
	_, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
	brokerR3WriteRecord(t, metaPath, brokerR3CompleteRecord(broker, call.operation, call.step, call.expectation, false))
	brokerR3WantResult(t, call.verify(broker), "")
	call.storesBytes = true
	brokerR3WantErr(t, call.verify(broker), brokerR3ErrInvocation)
}

// brokerR3Completed holds the arguments of one VerifyStepStreams call.
type brokerR3Completed struct {
	ctx          context.Context
	operation    store.OperationRecord
	step         store.OperationStep
	expectations []ServerBackupStreamExpectation
}

func brokerR3CompletedReadback(t *testing.T) brokerR3Completed {
	operation, _ := captureStreamFixture()
	expectation := brokerR3Expect("world", []byte("data"), 8)
	step := brokerR3ReadbackStep(t, expectation)
	step.Status = protocolstate.StepSucceeded
	return brokerR3Completed{ctx: brokerR3Ctx(t), operation: operation, step: step, expectations: []ServerBackupStreamExpectation{expectation}}
}

func (c brokerR3Completed) verify(broker *FileServerBackupTransferBroker) error {
	return broker.VerifyStepStreams(c.ctx, c.operation, c.step, c.expectations)
}

func TestBrokerR3VerifyStepStreamsGuardsBeforeSweeping(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := map[string]func(*brokerR3Completed){
		"nil ctx":         func(c *brokerR3Completed) { c.ctx = nil },
		"cancelled":       func(c *brokerR3Completed) { c.ctx = cancelled },
		"operation id":    func(c *brokerR3Completed) { c.operation.OperationID = "" },
		"operation type":  func(c *brokerR3Completed) { c.operation.OperationType = "x" },
		"step id":         func(c *brokerR3Completed) { c.step.StepID = "" },
		"running":         func(c *brokerR3Completed) { c.step.Status = protocolstate.StepRunning },
		"attempt":         func(c *brokerR3Completed) { c.step.Attempt = 0 },
		"no expectations": func(c *brokerR3Completed) { c.expectations = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			broker := brokerR3New(t, 16)
			expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
			call := brokerR3CompletedReadback(t)
			mutate(&call)
			brokerR3WantErr(t, call.verify(broker), brokerR3ErrCompleted)
			brokerR3Exists(t, expired, true)
		})
	}
	t.Run("nil broker", func(t *testing.T) {
		brokerR3WantErr(t, brokerR3CompletedReadback(t).verify(nil), brokerR3ErrCompleted)
	})
	t.Run("route", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
		call := brokerR3CompletedReadback(t)
		call.step.CapabilityName = "game.backup.unknown"
		brokerR3WantErr(t, call.verify(broker), "capability has no bounded backup stream route")
		brokerR3Exists(t, expired, true)
	})
}

func TestBrokerR3VerifyStepStreamsOrdering(t *testing.T) {
	t.Run("sweep then validate", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		expired := brokerR3PlantOperationDir(t, broker, "expired-operation", time.Hour)
		call := brokerR3CompletedReadback(t)
		call.step = brokerR3Rewrite(t, call.step, func(input map[string]any) { input["expected_size_bytes"] = 5 })
		brokerR3WantErr(t, call.verify(broker), brokerR3ErrStorage)
		brokerR3Exists(t, expired, false)
	})
	t.Run("sweep failure", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		if err := os.RemoveAll(broker.root); err != nil {
			t.Fatal(err)
		}
		brokerR3WantErr(t, brokerR3CompletedReadback(t).verify(broker), "open "+broker.root+": no such file or directory")
	})
	for _, bad := range []ServerBackupStreamExpectation{
		{DataSetID: "", SizeBytes: 4, MaximumBytes: 8},
		{DataSetID: "world", SizeBytes: -1, MaximumBytes: 8},
		{DataSetID: "world", SizeBytes: 9, MaximumBytes: 8},
	} {
		call := brokerR3CompletedReadback(t)
		call.expectations = []ServerBackupStreamExpectation{bad}
		call.step = brokerR3StorageStep(t, "storage.object.get", "readback", bad.SizeBytes, bad.SHA256)
		call.step.Status = protocolstate.StepSucceeded
		brokerR3WantErr(t, call.verify(brokerR3New(t, 16)), "stream verification expectation has an invalid data set or byte count")
	}
}

func TestBrokerR3VerifyStepStreamsChecksRepeatAfterVerifyingFirst(t *testing.T) {
	broker := brokerR3New(t, 16)
	call := brokerR3CompletedReadback(t)
	expectation := call.expectations[0]
	call.expectations = []ServerBackupStreamExpectation{expectation, expectation}
	call.step = brokerR3ValidateStep(t, call.operation, call.expectations)
	call.step.Status = protocolstate.StepSucceeded
	brokerR3WantErr(t, call.verify(broker), brokerR3ErrNoEvidence)
	_, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
	brokerR3WriteRecord(t, metaPath, brokerR3CompleteRecord(broker, call.operation, call.step, expectation, false))
	brokerR3WantErr(t, call.verify(broker), "stream verification repeats a data set")
	call.expectations = call.expectations[:1]
	brokerR3WantErr(t, call.verify(broker), brokerR3ErrGameCount)
}

func TestBrokerR3VerifyStepStreamsRecords(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*stagedStreamRecord)
		bytes  string
		want   string
	}{
		{"complete", func(*stagedStreamRecord) {}, "data", ""},
		{"identity", func(r *stagedStreamRecord) { r.Direction = brokerR3Source }, "data", brokerR3ErrEvidence},
		{"incomplete", func(r *stagedStreamRecord) { r.Complete = false }, "data", brokerR3ErrEvidence},
		{"size", func(r *stagedStreamRecord) { r.ActualSize = 3 }, "data", brokerR3ErrEvidence},
		{"checksum", func(r *stagedStreamRecord) { r.ActualSHA256 = brokerR3Digest([]byte("x")) }, "data", brokerR3ErrEvidence},
		{"tampered bytes", func(*stagedStreamRecord) {}, "DATA", brokerR3ErrStagedBytes},
		{"missing bytes", func(*stagedStreamRecord) {}, "", brokerR3ErrStagedBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 16)
			call := brokerR3CompletedReadback(t)
			dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
			if tc.bytes != "" {
				brokerR3Write(t, dataPath, []byte(tc.bytes))
			}
			record := brokerR3CompleteRecord(broker, call.operation, call.step, call.expectations[0], true)
			tc.mutate(&record)
			brokerR3WriteRecord(t, metaPath, record)
			brokerR3WantResult(t, call.verify(broker), tc.want)
		})
	}
	t.Run("corrupt record", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CompletedReadback(t)
		_, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		brokerR3Write(t, metaPath, []byte("{"))
		brokerR3WantErr(t, call.verify(broker), brokerR3ErrNoEvidence)
	})
}

func TestBrokerR3VerifySourceArtifactSourceStep(t *testing.T) {
	broker := brokerR3New(t, 64)
	operation, capture, expectation := brokerR3Captured(t, broker)
	brokerR3WantErr(t, broker.verifySourceArtifact(operation, "", expectation), "stream source step is not identified")
	brokerR3WantErr(t, broker.verifySourceArtifact(operation, "missing-step", expectation), brokerR3ErrNoSource)
	for _, mutate := range []func(*store.OperationStep){
		func(s *store.OperationStep) { s.Status = protocolstate.StepFailed },
		func(s *store.OperationStep) { s.Attempt = 0 },
	} {
		changed := operation
		changed.Steps = slices.Clone(operation.Steps)
		mutate(&changed.Steps[len(changed.Steps)-1])
		brokerR3WantErr(t, broker.verifySourceArtifact(changed, capture.StepID, expectation), brokerR3ErrNoSource)
	}
	empty := expectation
	empty.DataSetID = ""
	brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, empty), brokerR3ErrPathIDs)
	brokerR3WantResult(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), "")

	dataPath, _ := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
	brokerR3Write(t, dataPath, []byte("bytes-of-worlD"))
	brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), brokerR3ErrSourceHash)
	if err := os.Remove(dataPath); err != nil {
		t.Fatal(err)
	}
	brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), brokerR3ErrSourceHash)
}

func TestBrokerR3VerifySourceArtifactRecord(t *testing.T) {
	keep := func(e ServerBackupStreamExpectation) ServerBackupStreamExpectation { return e }
	cases := []struct {
		name        string
		mutate      func(*stagedStreamRecord)
		expectation func(ServerBackupStreamExpectation) ServerBackupStreamExpectation
		want        string
	}{
		{"operation", func(r *stagedStreamRecord) { r.OperationID = "other" }, keep, brokerR3ErrSourceRecord},
		{"step", func(r *stagedStreamRecord) { r.StepID = "other" }, keep, brokerR3ErrSourceRecord},
		{"attempt", func(r *stagedStreamRecord) { r.Attempt = 2 }, keep, brokerR3ErrSourceRecord},
		{"environment", func(r *stagedStreamRecord) { r.EnvironmentID = "prod" }, keep, brokerR3ErrSourceRecord},
		{"data set", func(r *stagedStreamRecord) { r.DataSetID = "nether" }, keep, brokerR3ErrSourceRecord},
		{"direction", func(r *stagedStreamRecord) { r.Direction = brokerR3Source }, keep, brokerR3ErrSourceRecord},
		{"stores bytes", func(r *stagedStreamRecord) { r.StoresBytes = false }, keep, brokerR3ErrSourceRecord},
		{"complete", func(r *stagedStreamRecord) { r.Complete = false }, keep, brokerR3ErrSourceRecord},
		{"actual size", func(r *stagedStreamRecord) { r.ActualSize = 3 }, keep, brokerR3ErrSourceRecord},
		{"actual checksum", func(r *stagedStreamRecord) { r.ActualSHA256 = brokerR3Digest([]byte("x")) }, keep, brokerR3ErrSourceRecord},
		{"record maximum below size", func(r *stagedStreamRecord) { r.MaximumBytes = 13 }, keep, brokerR3ErrSourceRecord},
		{"record maximum equal to size", func(r *stagedStreamRecord) { r.MaximumBytes = 14 }, keep, ""},
		{"expectation maximum below size", func(*stagedStreamRecord) {}, func(e ServerBackupStreamExpectation) ServerBackupStreamExpectation {
			e.MaximumBytes = 13
			return e
		}, brokerR3ErrSourceRecord},
		{"adapter and capability are not checked", func(r *stagedStreamRecord) { r.AdapterID, r.Capability = "x", "y" }, keep, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 64)
			operation, capture, expectation := brokerR3Captured(t, broker)
			_, metaPath := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
			record := brokerR3ReadRecord(t, metaPath)
			tc.mutate(&record)
			brokerR3WriteRecord(t, metaPath, record)
			brokerR3WantResult(t, broker.verifySourceArtifact(operation, capture.StepID, tc.expectation(expectation)), tc.want)
		})
	}
}

func TestBrokerR3StagedRecordMatches(t *testing.T) {
	broker := brokerR3New(t, 16)
	call := brokerR3ReadbackInvocation(t)
	base := broker.newRecord(call.operation, call.step, call.expectation, brokerR3Sink, time.Now(), true)
	cases := []struct {
		name   string
		mutate func(*stagedStreamRecord)
		want   bool
	}{
		{"base", func(*stagedStreamRecord) {}, true},
		{"unknown expected size", func(r *stagedStreamRecord) { r.ExpectedSize = -1 }, true},
		{"no expected checksum", func(r *stagedStreamRecord) { r.ExpectedSHA256 = "" }, true},
		{"measurements are ignored", func(r *stagedStreamRecord) { r.Complete, r.ActualSize, r.ActualSHA256 = true, 99, "x" }, true},
		{"operation", func(r *stagedStreamRecord) { r.OperationID = "x" }, false},
		{"step", func(r *stagedStreamRecord) { r.StepID = "x" }, false},
		{"attempt", func(r *stagedStreamRecord) { r.Attempt = 2 }, false},
		{"adapter", func(r *stagedStreamRecord) { r.AdapterID = "x" }, false},
		{"capability", func(r *stagedStreamRecord) { r.Capability = "x" }, false},
		{"environment", func(r *stagedStreamRecord) { r.EnvironmentID = "x" }, false},
		{"data set", func(r *stagedStreamRecord) { r.DataSetID = "x" }, false},
		{"direction", func(r *stagedStreamRecord) { r.Direction = "x" }, false},
		{"maximum", func(r *stagedStreamRecord) { r.MaximumBytes = 7 }, false},
		{"input digest", func(r *stagedStreamRecord) { r.InputSHA256 = "x" }, false},
		{"stores bytes", func(r *stagedStreamRecord) { r.StoresBytes = false }, false},
		{"expires at", func(r *stagedStreamRecord) { r.ExpiresAt = time.Time{} }, false},
		{"expected size", func(r *stagedStreamRecord) { r.ExpectedSize = 3 }, false},
		{"expected checksum", func(r *stagedStreamRecord) { r.ExpectedSHA256 = "x" }, false},
	}
	for _, tc := range cases {
		record := base
		tc.mutate(&record)
		if got := stagedRecordMatches(record, call.operation, call.step, call.expectation, brokerR3Sink, true); got != tc.want {
			t.Errorf("%s: stagedRecordMatches = %v, want %v", tc.name, got, tc.want)
		}
	}
}
