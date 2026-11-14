package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

type brokerR3InputCase struct {
	name         string
	step         string
	mutate       func(map[string]any)
	expectations []ServerBackupStreamExpectation
	want         string
}

func brokerR3ObjectAt(input map[string]any, index int) map[string]any {
	return input["manifest"].(map[string]any)["objects"].([]any)[index].(map[string]any)
}

func brokerR3SetIn(key, field string, value any) func(map[string]any) {
	return func(input map[string]any) { input[key].(map[string]any)[field] = value }
}

func brokerR3Set(key string, value any) func(map[string]any) {
	return func(input map[string]any) { input[key] = value }
}

func brokerR3InputCases() []brokerR3InputCase {
	world, nether := brokerR3Staged("world"), brokerR3Staged("nether")
	both := []ServerBackupStreamExpectation{world, nether}
	prod := map[string]any{"environment_id": "prod"}
	return []brokerR3InputCase{
		{name: "capture ok", step: "capture"},
		{name: "capture backup id", step: "capture", mutate: brokerR3Set("backup_id", "other"), want: brokerR3ErrCapture},
		{name: "capture data set", step: "capture", mutate: brokerR3Set("data_set_id", "nether"), want: brokerR3ErrCapture},
		{name: "capture target", step: "capture", mutate: brokerR3SetIn("target", "server_id", "other"), want: brokerR3ErrCapture},
		{name: "capture instance", step: "capture", mutate: brokerR3SetIn("target", "instance_ref", map[string]any{"external_id": "vm-2"}), want: brokerR3ErrCapture},
		{name: "get ok", step: "get"},
		{name: "get missing size", step: "get", mutate: func(i map[string]any) { delete(i, "expected_size_bytes") }, want: brokerR3ErrStorage},
		{name: "get size", step: "get", mutate: brokerR3Set("expected_size_bytes", 5), want: brokerR3ErrStorage},
		{name: "get algorithm", step: "get", mutate: brokerR3SetIn("expected_checksum", "algorithm", "md5"), want: brokerR3ErrStorage},
		{name: "get digest", step: "get", mutate: brokerR3SetIn("expected_checksum", "digest", "x"), want: brokerR3ErrStorage},
		{name: "get environment", step: "get", mutate: brokerR3Set("object_ref", prod), want: brokerR3ErrGetEnv},
		{name: "get ignores target", step: "get", mutate: brokerR3Set("target", prod)},
		{name: "put ok", step: "put"},
		{name: "put checksum precedes environment", step: "put", mutate: func(i map[string]any) {
			brokerR3SetIn("expected_checksum", "digest", "x")(i)
			i["target"] = prod
		}, want: brokerR3ErrStorage},
		{name: "put environment", step: "put", mutate: brokerR3Set("target", prod), want: brokerR3ErrPutEnv},
		{name: "put ignores object ref", step: "put", mutate: brokerR3Set("object_ref", prod)},
		{name: "game ok", step: "game", expectations: both},
		{name: "game backup id", step: "game", mutate: brokerR3Set("backup_id", "other"), expectations: both, want: brokerR3ErrGameBinding},
		{name: "game environment", step: "game", mutate: brokerR3Set("environment_id", "prod"), expectations: both, want: brokerR3ErrGameBinding},
		{name: "game object count", step: "game", want: brokerR3ErrGameCount},
		{name: "game stream count", step: "game", mutate: func(i map[string]any) { i["object_streams"] = i["object_streams"].([]any)[:1] }, expectations: both, want: brokerR3ErrGameCount},
		{name: "game object size missing", step: "game", mutate: func(i map[string]any) { delete(brokerR3ObjectAt(i, 1), "size_bytes") }, expectations: both, want: brokerR3ErrGameObject},
		{name: "game object data set", step: "game", mutate: func(i map[string]any) { brokerR3ObjectAt(i, 0)["data_set_id"] = "end" }, expectations: both, want: brokerR3ErrGameObject},
		{name: "game object order", step: "game", expectations: []ServerBackupStreamExpectation{nether, world}, want: brokerR3ErrGameObject},
		{name: "game object size", step: "game", mutate: func(i map[string]any) { brokerR3ObjectAt(i, 1)["size_bytes"] = 1 }, expectations: both, want: brokerR3ErrGameObject},
		{name: "game object checksum", step: "game", mutate: func(i map[string]any) { brokerR3ObjectAt(i, 1)["checksum"] = "x" }, expectations: both, want: brokerR3ErrGameObject},
		{name: "game stream ref", step: "game", mutate: func(i map[string]any) {
			i["object_streams"].([]any)[1].(map[string]any)["object_ref"] = map[string]any{"external_id": "other"}
		}, expectations: both, want: brokerR3ErrGameObject},
		{name: "unsupported", step: "unsupported", want: "unsupported stream capability"},
	}
}

func TestBrokerR3ValidateOperationInput(t *testing.T) {
	broker := brokerR3New(t, 16)
	operation, capture := captureStreamFixture()
	world, nether := brokerR3Staged("world"), brokerR3Staged("nether")
	unsupported := brokerR3ReadbackStep(t, world)
	unsupported.CapabilityName = "storage.object.delete"
	steps := map[string]store.OperationStep{
		"capture": capture, "get": brokerR3ReadbackStep(t, world), "put": brokerR3UploadStep(t, world),
		"game": brokerR3ValidateStep(t, operation, []ServerBackupStreamExpectation{world, nether}), "unsupported": unsupported,
	}
	for _, tc := range brokerR3InputCases() {
		t.Run(tc.name, func(t *testing.T) {
			step := steps[tc.step]
			if tc.mutate != nil {
				step = brokerR3Rewrite(t, step, tc.mutate)
			}
			expectations := tc.expectations
			if expectations == nil {
				expectations = []ServerBackupStreamExpectation{world}
			}
			brokerR3WantResult(t, broker.validateOperationInput(operation, step, expectations), tc.want)
		})
	}
	t.Run("resolved input wins", func(t *testing.T) {
		step := steps["get"]
		step.ResolvedInput = brokerR3Rewrite(t, step, brokerR3Set("expected_size_bytes", 5)).Input
		brokerR3WantErr(t, broker.validateOperationInput(operation, step, []ServerBackupStreamExpectation{world}), brokerR3ErrStorage)
	})
}

type brokerR3UsageFile struct {
	name   string
	size   int
	record *stagedStreamRecord
	raw    string
}

func brokerR3UsageRecord(size, maximum int64, complete bool) stagedStreamRecord {
	sha := brokerR3Digest([]byte("x"))
	return stagedStreamRecord{
		OperationID: "op", StepID: "step", Attempt: 1, DataSetID: "world", MaximumBytes: maximum,
		ExpiresAt: time.Now(), InputSHA256: sha, StoresBytes: true, Complete: complete,
		ActualSize: size, ActualSHA256: sha,
	}
}

func brokerR3Usage(name string, size, maximum int64, complete bool) brokerR3UsageFile {
	record := brokerR3UsageRecord(size, maximum, complete)
	return brokerR3UsageFile{name: name, size: int(size), record: &record}
}

func brokerR3UsageMutated(size int64, mutate func(*stagedStreamRecord)) []brokerR3UsageFile {
	file := brokerR3Usage("a", size, 8, true)
	mutate(file.record)
	return []brokerR3UsageFile{file}
}

func brokerR3PlantUsage(t *testing.T, root string, files []brokerR3UsageFile) {
	t.Helper()
	for _, file := range files {
		path := filepath.Join(root, file.name+".bin")
		brokerR3Write(t, path, []byte(strings.Repeat("b", file.size)))
		if file.record != nil {
			file.raw = string(brokerR3JSON(t, file.record))
		}
		if file.raw != "" {
			brokerR3Write(t, path+".json", []byte(file.raw))
		}
	}
}

func brokerR3PlantDecoys(t *testing.T, root string) {
	brokerR3Write(t, filepath.Join(root, "note.txt"), []byte("12345678901234567"))
	if err := os.MkdirAll(filepath.Join(root, "dir.bin"), 0700); err != nil {
		t.Fatal(err)
	}
}

func brokerR3PlantSymlink(t *testing.T, root string) {
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "link.bin")); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerR3StagingUsage(t *testing.T) {
	const identity = "staged byte file has invalid size bounds or identity metadata"
	const completed = "completed staged bytes do not match their transfer record"
	const exceeded = "existing backup staging data exceeds the configured budget"
	use := brokerR3Usage
	mutated := brokerR3UsageMutated
	cases := []struct {
		name   string
		files  []brokerR3UsageFile
		extra  func(*testing.T, string)
		budget int64
		want   string
		usage  int64
	}{
		{name: "empty"},
		{name: "ignores other files", extra: brokerR3PlantDecoys},
		{name: "complete counts size", files: []brokerR3UsageFile{use("a", 3, 8, true)}, usage: 3},
		{name: "incomplete reserves maximum", files: []brokerR3UsageFile{use("a", 3, 8, false)}, usage: 8},
		{name: "incomplete at maximum", files: []brokerR3UsageFile{use("a", 8, 8, false)}, usage: 8},
		{name: "nested files", files: []brokerR3UsageFile{use("x/y/a", 3, 8, true), use("b", 2, 8, true)}, usage: 5},
		{name: "symlink", extra: brokerR3PlantSymlink, want: "invalid file in stream staging directory"},
		{name: "file above budget", files: []brokerR3UsageFile{use("a", 17, 16, true)}, want: exceeded},
		{name: "files above budget", files: []brokerR3UsageFile{use("a", 9, 9, true), use("b", 8, 8, true)}, want: exceeded},
		{name: "no record", files: []brokerR3UsageFile{{name: "a", size: 1}}, want: "staged byte file has no matching transfer record"},
		{name: "invalid record", files: []brokerR3UsageFile{{name: "a", size: 1, raw: "{"}}, want: "staged byte file has an invalid transfer record"},
		{name: "not stored", files: mutated(3, func(r *stagedStreamRecord) { r.StoresBytes = false }), want: identity},
		{name: "operation", files: mutated(3, func(r *stagedStreamRecord) { r.OperationID = "" }), want: identity},
		{name: "step", files: mutated(3, func(r *stagedStreamRecord) { r.StepID = "" }), want: identity},
		{name: "attempt", files: mutated(3, func(r *stagedStreamRecord) { r.Attempt = 0 }), want: identity},
		{name: "data set", files: mutated(3, func(r *stagedStreamRecord) { r.DataSetID = "" }), want: identity},
		{name: "maximum below size", files: mutated(3, func(r *stagedStreamRecord) { r.MaximumBytes = 2 }), want: identity},
		{name: "zero maximum", files: mutated(0, func(r *stagedStreamRecord) { r.MaximumBytes = 0 }), want: identity},
		{name: "maximum above budget", files: mutated(3, func(r *stagedStreamRecord) { r.MaximumBytes = 17 }), want: identity},
		{name: "expiry", files: mutated(3, func(r *stagedStreamRecord) { r.ExpiresAt = time.Time{} }), want: identity},
		{name: "input digest", files: mutated(3, func(r *stagedStreamRecord) { r.InputSHA256 = strings.Repeat("z", 64) }), want: identity},
		{name: "complete size", files: mutated(3, func(r *stagedStreamRecord) { r.ActualSize = 2 }), want: completed},
		{name: "complete digest", files: mutated(3, func(r *stagedStreamRecord) { r.ActualSHA256 = "abc" }), want: completed},
		{name: "incomplete ignores measurements", files: mutated(3, func(r *stagedStreamRecord) {
			r.Complete, r.ActualSize, r.ActualSHA256 = false, 0, ""
		}), usage: 8},
		{name: "reservation above budget", budget: 12, files: []brokerR3UsageFile{use("a", 0, 8, false), use("b", 2, 8, false)},
			want: "existing backup staging reservation exceeds the configured budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := tc.budget
			if budget == 0 {
				budget = 16
			}
			broker := brokerR3New(t, budget)
			brokerR3PlantUsage(t, broker.root, tc.files)
			if tc.extra != nil {
				tc.extra(t, broker.root)
			}
			usage, err := broker.stagingUsageLocked()
			brokerR3WantResult(t, err, tc.want)
			if usage != tc.usage {
				t.Fatalf("usage = %d, want %d", usage, tc.usage)
			}
		})
	}
	t.Run("missing root", func(t *testing.T) {
		broker := brokerR3New(t, 16)
		if err := os.RemoveAll(broker.root); err != nil {
			t.Fatal(err)
		}
		_, err := broker.stagingUsageLocked()
		brokerR3WantErr(t, err, "lstat "+broker.root+": no such file or directory")
	})
}

func TestBrokerR3SweepExpired(t *testing.T) {
	const guard = "broker and sweep clock are required"
	var nilBroker *FileServerBackupTransferBroker
	_, err := nilBroker.SweepExpired(time.Now())
	brokerR3WantErr(t, err, guard)
	broker := brokerR3New(t, 16)
	_, err = broker.SweepExpired(time.Time{})
	brokerR3WantErr(t, err, guard)

	expired := brokerR3PlantOperationDir(t, broker, "expired", time.Hour)
	fresh := brokerR3PlantOperationDir(t, broker, "fresh", time.Minute)
	unsafe := filepath.Join(broker.root, "not-a-stream-key")
	brokerR3Write(t, filepath.Join(unsafe, "file"), nil)
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(unsafe, stale, stale); err != nil {
		t.Fatal(err)
	}
	removed, err := broker.SweepExpired(time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("sweep = %d, %v; want 1 removal", removed, err)
	}
	brokerR3Exists(t, expired, false)
	brokerR3Exists(t, fresh, true)
	brokerR3Exists(t, unsafe, true)
}

func TestBrokerR3CleanupOperationGuards(t *testing.T) {
	const guard = "broker and operation ID are required for staging cleanup"
	var nilBroker *FileServerBackupTransferBroker
	brokerR3WantErr(t, nilBroker.CleanupOperation("op"), guard)
	brokerR3WantErr(t, brokerR3New(t, 16).CleanupOperation(""), guard)
}
