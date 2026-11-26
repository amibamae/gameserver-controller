package workflow

// Round-4 characterization of check order in stagingUsageLocked and
// verifySourceArtifact: when several checks would fail, which error wins.

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	brokerR4ErrUsageIdentity  = "staged byte file has invalid size bounds or identity metadata"
	brokerR4ErrUsageCompleted = "completed staged bytes do not match their transfer record"
	brokerR4ErrUsageExceeded  = "existing backup staging data exceeds the configured budget"
	brokerR4ErrUsageReserved  = "existing backup staging reservation exceeds the configured budget"
	brokerR4ErrUsageNoRecord  = "staged byte file has no matching transfer record"
	brokerR4ErrUsageBadRecord = "staged byte file has an invalid transfer record"
)

// brokerR4UsageBroken is a usage file whose record fails one or more checks.
func brokerR4UsageBroken(name string, size int64, mutate func(*stagedStreamRecord)) brokerR3UsageFile {
	file := brokerR3Usage(name, size, 8, true)
	mutate(file.record)
	return file
}

func TestBrokerR4StagingUsageCheckOrder(t *testing.T) {
	noIdentity := func(r *stagedStreamRecord) { r.OperationID = "" }
	badMeasure := func(r *stagedStreamRecord) { r.ActualSize = 1 }
	cases := []struct {
		name   string
		files  []brokerR3UsageFile
		budget int64
		want   string
		usage  int64
	}{
		{name: "size over budget before missing record", files: []brokerR3UsageFile{{name: "a", size: 17}}, want: brokerR4ErrUsageExceeded},
		{name: "size over budget before invalid record", files: []brokerR3UsageFile{{name: "a", size: 17, raw: "{"}}, want: brokerR4ErrUsageExceeded},
		{name: "identity before completed measurements", files: []brokerR3UsageFile{
			brokerR4UsageBroken("a", 3, func(r *stagedStreamRecord) { noIdentity(r); badMeasure(r) }),
		}, want: brokerR4ErrUsageIdentity},
		{name: "first file in walk order wins", files: []brokerR3UsageFile{
			brokerR4UsageBroken("a", 3, badMeasure), brokerR4UsageBroken("b", 3, noIdentity),
		}, want: brokerR4ErrUsageCompleted},
		{name: "walk order is lexical", files: []brokerR3UsageFile{
			brokerR4UsageBroken("b", 3, badMeasure), brokerR4UsageBroken("a", 3, noIdentity),
		}, want: brokerR4ErrUsageIdentity},
		{name: "missing record before later identity", files: []brokerR3UsageFile{
			{name: "a", size: 1}, brokerR4UsageBroken("b", 3, noIdentity),
		}, want: brokerR4ErrUsageNoRecord},
		{name: "invalid record before later identity", files: []brokerR3UsageFile{
			{name: "a", size: 1, raw: "[]"}, brokerR4UsageBroken("b", 3, noIdentity),
		}, want: brokerR4ErrUsageBadRecord},
		{name: "reservation fills budget exactly", budget: 16, files: []brokerR3UsageFile{
			brokerR3Usage("a", 8, 8, true), brokerR3Usage("b", 0, 8, false),
		}, usage: 16},
		{name: "reservation one byte over", budget: 15, files: []brokerR3UsageFile{
			brokerR3Usage("a", 8, 8, true), brokerR3Usage("b", 0, 8, false),
		}, want: brokerR4ErrUsageReserved},
		{name: "size after reservation over budget", budget: 15, files: []brokerR3UsageFile{
			brokerR3Usage("a", 0, 8, false), brokerR3Usage("b", 8, 8, false),
		}, want: brokerR4ErrUsageExceeded},
		{name: "incomplete partial file reserves the rest", budget: 16, files: []brokerR3UsageFile{
			brokerR3Usage("a", 5, 8, false), brokerR3Usage("b", 3, 8, true),
		}, usage: 11},
		{name: "maximum equal to budget", budget: 8, files: []brokerR3UsageFile{brokerR3Usage("a", 3, 8, true)}, usage: 3},
		{name: "maximum above budget", budget: 7, files: []brokerR3UsageFile{brokerR3Usage("a", 3, 8, true)}, want: brokerR4ErrUsageIdentity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := tc.budget
			if budget == 0 {
				budget = 16
			}
			broker := brokerR3New(t, budget)
			brokerR3PlantUsage(t, broker.root, tc.files)
			usage, err := broker.stagingUsageLocked()
			brokerR3WantResult(t, err, tc.want)
			if usage != tc.usage {
				t.Fatalf("usage = %d, want %d", usage, tc.usage)
			}
		})
	}
}

func TestBrokerR4StagingUsageIdentityFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*stagedStreamRecord)
		want   string
	}{
		{"short input digest", func(r *stagedStreamRecord) { r.InputSHA256 = "ab" }, brokerR4ErrUsageIdentity},
		{"empty input digest", func(r *stagedStreamRecord) { r.InputSHA256 = "" }, brokerR4ErrUsageIdentity},
		{"negative attempt", func(r *stagedStreamRecord) { r.Attempt = -1 }, brokerR4ErrUsageIdentity},
		{"incomplete ignores actual digest", func(r *stagedStreamRecord) { r.Complete, r.ActualSHA256 = false, "bad" }, ""},
		{"adapter, capability and environment are not checked", func(r *stagedStreamRecord) {
			r.AdapterID, r.Capability, r.EnvironmentID = "", "", ""
		}, ""},
		{"direction and expectations are not checked", func(r *stagedStreamRecord) {
			r.Direction, r.ExpectedSize, r.ExpectedSHA256 = "x", 99, "y"
		}, ""},
		{"complete uppercase digest", func(r *stagedStreamRecord) {
			r.ActualSHA256 = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
		}, ""},
		{"complete non-hex digest", func(r *stagedStreamRecord) {
			r.ActualSHA256 = "zbcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
		}, brokerR4ErrUsageCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker := brokerR3New(t, 16)
			brokerR3PlantUsage(t, broker.root, []brokerR3UsageFile{brokerR4UsageBroken("a", 3, tc.mutate)})
			_, err := broker.stagingUsageLocked()
			brokerR3WantResult(t, err, tc.want)
		})
	}
}

func TestBrokerR4VerifySourceArtifactCheckOrder(t *testing.T) {
	t.Run("step before path", func(t *testing.T) {
		broker := brokerR3New(t, 64)
		operation, capture, expectation := brokerR3Captured(t, broker)
		expectation.DataSetID = ""
		changed := brokerR4WithLastStep(operation, func(s *store.OperationStep) { s.Attempt = 0 })
		brokerR3WantErr(t, broker.verifySourceArtifact(changed, capture.StepID, expectation), brokerR3ErrNoSource)
	})
	t.Run("missing record", func(t *testing.T) {
		broker := brokerR3New(t, 64)
		operation, capture, expectation := brokerR3Captured(t, broker)
		_, metaPath := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
		if err := os.Remove(metaPath); err != nil {
			t.Fatal(err)
		}
		brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), brokerR3ErrSourceRecord)
	})
	t.Run("record before bytes", func(t *testing.T) {
		broker := brokerR3New(t, 64)
		operation, capture, expectation := brokerR3Captured(t, broker)
		dataPath, metaPath := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
		record := brokerR3ReadRecord(t, metaPath)
		record.Complete = false
		brokerR3WriteRecord(t, metaPath, record)
		if err := os.Remove(dataPath); err != nil {
			t.Fatal(err)
		}
		brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), brokerR3ErrSourceRecord)
	})
	t.Run("bytes above expectation maximum", func(t *testing.T) {
		broker := brokerR3New(t, 64)
		operation, capture, expectation := brokerR3Captured(t, broker)
		dataPath, _ := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
		brokerR3Write(t, dataPath, []byte("bytes-of-world-and-more"))
		brokerR3WantErr(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), brokerR3ErrSourceHash)
	})
	t.Run("expired record still verifies", func(t *testing.T) {
		broker := brokerR3New(t, 64)
		operation, capture, expectation := brokerR3Captured(t, broker)
		_, metaPath := brokerR3Paths(t, broker, operation.OperationID, capture, "world")
		record := brokerR3ReadRecord(t, metaPath)
		record.ExpiresAt = time.Now().Add(-time.Hour)
		brokerR3WriteRecord(t, metaPath, record)
		brokerR3WantResult(t, broker.verifySourceArtifact(operation, capture.StepID, expectation), "")
	})
}

func brokerR4WithLastStep(operation store.OperationRecord, mutate func(*store.OperationStep)) store.OperationRecord {
	changed := operation
	changed.Steps = slices.Clone(operation.Steps)
	mutate(&changed.Steps[len(changed.Steps)-1])
	return changed
}

func TestBrokerR4StagedRecordMatchesExpectationBounds(t *testing.T) {
	broker := brokerR3New(t, 16)
	call := brokerR3ReadbackInvocation(t)
	base := broker.newRecord(call.operation, call.step, call.expectation, brokerR3Sink, time.Now(), true)
	cases := []struct {
		name   string
		mutate func(*stagedStreamRecord)
		want   bool
	}{
		{"expected size below unknown", func(r *stagedStreamRecord) { r.ExpectedSize = -2 }, true},
		{"unknown size with other checksum", func(r *stagedStreamRecord) { r.ExpectedSize, r.ExpectedSHA256 = -1, "x" }, false},
		{"unknown size and no checksum", func(r *stagedStreamRecord) { r.ExpectedSize, r.ExpectedSHA256 = -1, "" }, true},
		{"other size and no checksum", func(r *stagedStreamRecord) { r.ExpectedSize, r.ExpectedSHA256 = 3, "" }, false},
	}
	for _, tc := range cases {
		record := base
		tc.mutate(&record)
		if got := stagedRecordMatches(record, call.operation, call.step, call.expectation, brokerR3Sink, true); got != tc.want {
			t.Errorf("%s: stagedRecordMatches = %v, want %v", tc.name, got, tc.want)
		}
	}
	if stagedRecordMatches(base, call.operation, call.step, call.expectation, brokerR3Sink, false) {
		t.Error("stagedRecordMatches accepted a different storesBytes")
	}
	if stagedRecordMatches(base, call.operation, call.step, call.expectation, brokerR3Source, true) {
		t.Error("stagedRecordMatches accepted a different direction")
	}
}
