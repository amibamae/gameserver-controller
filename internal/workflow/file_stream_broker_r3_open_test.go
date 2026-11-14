package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBrokerR3IssueStopsAtPathPreparationFailures(t *testing.T) {
	t.Run("operation directory is a file", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CaptureCall(t)
		operationDir := broker.operationDirectory(call.operation.OperationID)
		brokerR3Write(t, operationDir, []byte("file"))
		brokerR3IssueFails(t, broker, call, "mkdir "+operationDir+": not a directory")
	})
	t.Run("metadata already present", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CaptureCall(t)
		dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		brokerR3Write(t, metaPath, []byte("existing"))
		brokerR3IssueFails(t, broker, call, brokerR3ErrMetaExists)
		brokerR3Exists(t, dataPath, false)
		brokerR3WantContent(t, metaPath, "existing")
	})
	t.Run("metadata path is a directory", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CaptureCall(t)
		dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		if err := os.MkdirAll(metaPath, 0700); err != nil {
			t.Fatal(err)
		}
		brokerR3IssueFails(t, broker, call, brokerR3ErrMetaExists)
		brokerR3Exists(t, dataPath, false)
	})
	t.Run("staging file cannot be created", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		call := brokerR3CaptureCall(t)
		dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
		if err := os.MkdirAll(dataPath, 0700); err != nil {
			t.Fatal(err)
		}
		brokerR3IssueFails(t, broker, call, "create bounded stream staging file: open "+dataPath+": file exists")
		brokerR3Exists(t, metaPath, false)
	})
}

func TestBrokerR3IssueReleasesEarlierSourcesWhenALaterOneFails(t *testing.T) {
	broker := brokerR3New(t, 256)
	operation, expectations := brokerR3ReadbackOperation(t, broker, "world", "nether")
	step := brokerR3ValidateStep(t, operation, expectations)
	_, firstMeta := brokerR3Paths(t, broker, operation.OperationID, step, "world")
	_, secondMeta := brokerR3Paths(t, broker, operation.OperationID, step, "nether")
	brokerR3Write(t, secondMeta, []byte("existing"))

	brokerR3IssueFails(t, broker, brokerR3StepCall(t, operation, step, expectations...), brokerR3ErrMetaExists)
	brokerR3Exists(t, firstMeta, false)
	brokerR3WantContent(t, secondMeta, "existing")
	for _, expectation := range expectations {
		readback := operationStep(operation, backupDataStepID("readback", expectation.DataSetID))
		source, _ := brokerR3Paths(t, broker, operation.OperationID, readback, expectation.DataSetID)
		brokerR3WantHandles(t, source, 0)
		brokerR3WantContent(t, source, string(brokerR3Data(expectation.DataSetID)))
	}
}

func TestBrokerR3IssueOpensOneSinkStream(t *testing.T) {
	broker := brokerR3New(t, 16)
	data := []byte("world")
	call := brokerR3CaptureCall(t)
	call.expectations = []ServerBackupStreamExpectation{brokerR3Expect("world", data, 8)}
	transfer := brokerR3IssueOK(t, broker, call)[0]
	brokerR3WantReference(t, transfer, brokerR3Sink, 8, true)

	operationDir := safeStreamKey(call.operation.OperationID)
	stepDir := operationDir + "/" + safeStreamKey(call.step.StepID) + "/"
	attemptDir := stepDir + "attempt-1/"
	wantTree := []string{
		"./", operationDir + "/", stepDir, attemptDir,
		attemptDir + safeStreamKey("world") + ".bin", attemptDir + safeStreamKey("world") + ".bin.json",
	}
	if got := brokerR3Tree(t, broker.root); !slices.Equal(got, wantTree) {
		t.Fatalf("tree = %v, want %v", got, wantTree)
	}
	dataPath, metaPath := brokerR3Paths(t, broker, call.operation.OperationID, call.step, "world")
	if info, err := os.Stat(dataPath); err != nil || info.Size() != 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("staging file = %v, %v", info, err)
	}
	brokerR3WantRecord(t, metaPath,
		broker.newRecord(call.operation, call.step, call.expectations[0], brokerR3Sink, transfer.Reference.ExpiresAt, true))

	brokerR3WantHandles(t, dataPath, 1)
	brokerR3WantErr(t, transfer.Verify(), brokerR3ErrInvocation)
	if _, err := transfer.Sink.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := transfer.Sink.Close(); err != nil {
		t.Fatal(err)
	}
	brokerR3WantHandles(t, dataPath, 0)
	brokerR3WantResult(t, transfer.Verify(), "")
}

func TestBrokerR3IssueOpensSourceStreamsInExpectationOrder(t *testing.T) {
	broker := brokerR3New(t, 256)
	operation, expectations := brokerR3ReadbackOperation(t, broker, "world", "nether")
	step := brokerR3ValidateStep(t, operation, expectations)
	transfers := brokerR3IssueOK(t, broker, brokerR3StepCall(t, operation, step, expectations...))
	first, second := transfers[0].Reference, transfers[1].Reference
	if first.TransferID == second.TransferID || !first.ExpiresAt.Equal(second.ExpiresAt) {
		t.Fatalf("references = %+v / %+v", first, second)
	}
	attemptDir := filepath.Join(broker.operationDirectory(operation.OperationID), safeStreamKey(step.StepID), "attempt-1")
	wantNames := []string{safeStreamKey("nether") + ".bin.json", safeStreamKey("world") + ".bin.json"}
	slices.Sort(wantNames)
	if got := brokerR3Names(t, attemptDir); !slices.Equal(got, wantNames) {
		t.Fatalf("validate step staging = %v, want %v", got, wantNames)
	}
	for index, transfer := range transfers {
		expectation := expectations[index]
		brokerR3WantReference(t, transfer, brokerR3Source, expectation.MaximumBytes, false)
		_, metaPath := brokerR3Paths(t, broker, operation.OperationID, step, expectation.DataSetID)
		brokerR3WantRecord(t, metaPath,
			broker.newRecord(operation, step, expectation, brokerR3Source, transfer.Reference.ExpiresAt, false))
		readback := operationStep(operation, backupDataStepID("readback", expectation.DataSetID))
		source, _ := brokerR3Paths(t, broker, operation.OperationID, readback, expectation.DataSetID)
		brokerR3DrainSource(t, transfer, source, string(brokerR3Data(expectation.DataSetID)))
		// Verify re-validates the step input against this single stream, so a
		// multi-object manifest is reported as a count mismatch (current behaviour).
		brokerR3WantErr(t, transfer.Verify(), brokerR3ErrGameCount)
	}
}

func TestBrokerR3IssueSingleSourceStreamVerifies(t *testing.T) {
	broker := brokerR3New(t, 256)
	operation, expectations := brokerR3ReadbackOperation(t, broker, "world")
	step := brokerR3ValidateStep(t, operation, expectations)
	transfer := brokerR3IssueOK(t, broker, brokerR3StepCall(t, operation, step, expectations...))[0]
	brokerR3WantErr(t, transfer.Verify(), brokerR3ErrInvocation)
	readback := operationStep(operation, backupDataStepID("readback", "world"))
	source, _ := brokerR3Paths(t, broker, operation.OperationID, readback, "world")
	brokerR3DrainSource(t, transfer, source, string(brokerR3Data("world")))
	brokerR3WantResult(t, transfer.Verify(), "")
}
