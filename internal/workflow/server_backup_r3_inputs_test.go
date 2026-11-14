package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin ServerBackupWorkflow.ResolveStepInput and
// StreamTransfersForStep: the exact resolved inputs, the stream expectations
// handed to the broker, and every rejection message in check order.

// backupR3Resolve resolves stepID's input in operation with the fixture
// workflow and returns the canonical JSON or the error text.
func backupR3Resolve(t *testing.T, f backupR3Fixture, operation store.OperationRecord, step store.OperationStep) string {
	t.Helper()
	workflow := f.backupR3Workflow(f.server, backupR3Streams{})
	resolved, err := workflow.ResolveStepInput(context.Background(), operation, step)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(mustJSON(decodeObject(resolved)))
}

// backupR3PlannedInput returns stepID's plan input with extra fields set.
func backupR3PlannedInput(t *testing.T, f backupR3Fixture, stepID string, extra map[string]any) string {
	t.Helper()
	input := decodeObject(operationStep(f.operation, stepID).Input)
	for key, value := range extra {
		input[key] = value
	}
	return string(backupJSON(t, input))
}

func TestBackupR3ResolveStepInputResults(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	objectRef := stepField(operationStep(f.complete, backupR3Upload).Output, "object_ref")
	checksum := map[string]any{"algorithm": "sha256", "digest": backupTestDigest}
	manifest := decodeObject(stepField(operationStep(f.operation, backupValidateStep).Input, "manifest"))
	manifest["consistency"] = map[string]any{"mode": "offline", "evidence": "runtime stopped"}
	manifest["objects"] = []any{map[string]any{"data_set_id": "world", "object_ref": objectRef, "size_bytes": 32,
		"checksum_algorithm": "sha256", "checksum": backupTestDigest}}
	for _, test := range []struct {
		step string
		want string
	}{
		{backupR3Capture, backupR3PlannedInput(t, f, backupR3Capture, map[string]any{"preparation_id": "preparation-1"})},
		{backupR3Upload, backupR3PlannedInput(t, f, backupR3Upload, map[string]any{"expected_size_bytes": 32, "expected_checksum": checksum})},
		{backupR3Readback, backupR3PlannedInput(t, f, backupR3Readback, map[string]any{"object_ref": objectRef, "expected_size_bytes": 32, "expected_checksum": checksum})},
		{backupValidateStep, backupR3PlannedInput(t, f, backupValidateStep, map[string]any{"manifest": manifest,
			"object_streams": []any{map[string]any{"object_ref": objectRef}}})},
		{backupInspectInstanceStep, backupR3PlannedInput(t, f, backupInspectInstanceStep, nil)},
	} {
		got := backupR3Resolve(t, f, f.complete, operationStep(f.complete, test.step))
		if got != test.want {
			t.Errorf("ResolveStepInput(%s) =\n%s\nwant\n%s", test.step, got, test.want)
		}
	}
}

func TestBackupR3ResolveStepInputGuards(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	step := operationStep(f.complete, backupR3Capture)
	workflow := f.backupR3Workflow(f.server, backupR3Streams{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	const guard = "server.backup input resolution requires an operation and context"
	otherType := backupR3Copy(f.complete)
	otherType.OperationType = "server.start"
	badParameters := backupR3Copy(f.complete)
	badParameters.RequestSnapshot.Parameters = json.RawMessage(`{"backup_profile":`)
	badSnapshot := backupR3Copy(f.complete)
	badSnapshot.RequestSnapshot.OperationType = "server.start"
	badInput, nullInput := step, step
	badInput.Input = json.RawMessage(`nope`)
	nullInput.Input = json.RawMessage(`null`)
	otherStorage := f.server
	otherStorage.StorageProfile = "other"
	for _, test := range []struct {
		name      string
		workflow  *ServerBackupWorkflow
		ctx       context.Context
		operation store.OperationRecord
		step      store.OperationStep
		want      string
	}{
		{"nil context", workflow, nil, f.complete, step, guard},
		{"nil workflow", nil, context.Background(), f.complete, step, guard},
		{"other operation", workflow, context.Background(), otherType, step, guard},
		{"cancelled", workflow, cancelled, f.complete, step, "context canceled"},
		{"undecodable input", workflow, context.Background(), f.complete, badInput, "decode planned " + step.StepID + " input"},
		{"null input", workflow, context.Background(), f.complete, nullInput, "decode planned " + step.StepID + " input"},
		{"undecodable parameters", workflow, context.Background(), badParameters, step, "decode accepted backup profile"},
		{"snapshot of another operation", workflow, context.Background(), badSnapshot, step, "accepted operation does not bind a backup profile"},
		{"profile for another storage profile", f.backupR3Workflow(otherStorage, backupR3Streams{}), context.Background(), f.complete, step,
			"trusted backup profile must bind a release, offline consistency, bounded data sets/checks, and the server storage profile"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.workflow.ResolveStepInput(test.ctx, test.operation, test.step)
			if got := backupR3ErrText(err); got != test.want {
				t.Fatalf("ResolveStepInput = %q, want %q", got, test.want)
			}
		})
	}
	unknown := f.backupR3Workflow(f.server, backupR3Streams{})
	unknown.servers = testBackupServerRepository{}
	if _, err := unknown.ResolveStepInput(context.Background(), f.complete, step); backupR3ErrText(err) != store.ErrNotFound.Error() {
		t.Fatalf("ResolveStepInput without the server = %v, want %v", err, store.ErrNotFound)
	}
}

func TestBackupR3ResolveStepInputRejections(t *testing.T) {
	const capture = "error: capture input requires an exact completed backup preparation"
	const upload = "error: storage upload input requires a complete bounded capture"
	const readbackObject = "error: storage readback input requires the exact uploaded object"
	const prepared = "error: backup validation requires a completed, evidence-bearing preparation"
	const lists = "error: planned manifest and stream lists do not cover the trusted data sets"
	const validCapture = `error: data set "world" has no complete, bounded capture output`
	const validUpload = `error: data set "world" has no exact completed upload`
	const validReadback = `error: data set "world" has no exact completed storage readback`
	f := newBackupR3Fixture(t, false)
	withPlanned := func(stepID, field string, value any) func(*testing.T, *store.OperationRecord) {
		return func(t *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, stepID, func(s *store.OperationStep) {
				input := decodeObject(s.Input)
				if value == nil {
					delete(input, field)
				} else {
					input[field] = value
				}
				s.Input = backupJSON(t, input)
			})
		}
	}
	withManifestObject := func(object any) func(*testing.T, *store.OperationRecord) {
		return func(t *testing.T, o *store.OperationRecord) {
			manifest := decodeObject(stepField(operationStep(*o, backupValidateStep).Input, "manifest"))
			manifest["objects"] = []any{object}
			withPlanned(backupValidateStep, "manifest", manifest)(t, o)
		}
	}
	otherUpload := func(t *testing.T, o *store.OperationRecord) {
		ref := json.RawMessage(`{"adapter_id":"storage.fake","container":"backups","environment_id":"staging","key":"server-1/` + o.OperationID + `/world","storage_profile_id":"storage-profile","version_id":"v2"}`)
		setBackupOutputField(t, o, backupR3Readback, "object_ref", ref)
		backupR3SetInputField(t, o, backupR3Readback, "object_ref", ref)
	}
	for _, test := range []struct {
		name   string
		step   string
		stepID string
		change func(*testing.T, *store.OperationRecord)
		want   string
	}{
		{"capture outside the plan", backupR3Capture, "capture-other", nil, "error: capture step is not part of the trusted backup plan"},
		{"capture before preparation", backupR3Capture, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupPrepareStep) }, capture},
		{"capture of another backup", backupR3Capture, "", withPlanned(backupR3Capture, "backup_id", "other"), capture},
		{"capture of another generation", backupR3Capture, "", withPlanned(backupR3Capture, "data_generation_id", "other"), capture},
		{"capture with a different prepared set", backupR3Capture, "", func(t *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, backupPrepareStep, func(s *store.OperationStep) {
				input := decodeObject(s.Input)
				input["data_set_ids"] = []string{"world", "nether"}
				s.ResolvedInput = backupJSON(t, input)
			})
			setBackupOutputField(t, o, backupPrepareStep, "data_set_ids", []string{"world", "nether"})
		}, capture},

		{"upload outside the plan", backupR3Upload, "upload-other", nil, "error: upload step is not part of the trusted backup plan"},
		{"upload before capture", backupR3Upload, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Capture) }, upload},
		{"upload of an oversized capture", backupR3Upload, "", backupR3Field(backupR3Capture, "size_bytes", 65), upload},
		{"upload of an empty capture", backupR3Upload, "", backupR3Field(backupR3Capture, "size_bytes", 0), ""},

		{"readback outside the plan", backupR3Readback, "readback-other", nil, "error: readback step is not part of the trusted backup plan"},
		{"readback before capture", backupR3Readback, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Capture) },
			"error: storage readback input requires a completed capture"},
		{"readback before upload", backupR3Readback, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Upload) }, readbackObject},
		{"readback of a partial upload", backupR3Readback, "", backupR3Field(backupR3Upload, "upload_state", "partial"), readbackObject},
		{"readback of an oversized capture", backupR3Readback, "", backupR3Field(backupR3Capture, "size_bytes", 65), ""},

		{"validation before preparation", backupValidateStep, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupPrepareStep) }, prepared},
		{"validation without objects", backupValidateStep, "", func(t *testing.T, o *store.OperationRecord) {
			manifest := decodeObject(stepField(operationStep(*o, backupValidateStep).Input, "manifest"))
			delete(manifest, "objects")
			withPlanned(backupValidateStep, "manifest", manifest)(t, o)
		}, lists},
		{"validation without streams", backupValidateStep, "", withPlanned(backupValidateStep, "object_streams", nil), lists},
		{"validation without a manifest", backupValidateStep, "", withPlanned(backupValidateStep, "manifest", nil), lists},
		{"validation before capture", backupValidateStep, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Capture) }, validCapture},
		{"validation of an oversized capture", backupValidateStep, "", backupR3Field(backupR3Capture, "size_bytes", 65), validCapture},
		{"validation before upload", backupValidateStep, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Upload) }, validUpload},
		{"validation of a partial upload", backupValidateStep, "", backupR3Field(backupR3Upload, "upload_state", "partial"), validUpload},
		{"validation of a short upload", backupValidateStep, "", backupR3Field(backupR3Upload, "bytes_received", 31), validUpload},
		{"validation before readback", backupValidateStep, "", func(_ *testing.T, o *store.OperationRecord) { backupR3Pending(o, backupR3Readback) }, validReadback},
		{"validation of a partial readback", backupValidateStep, "", backupR3Field(backupR3Readback, "transfer_state", "partial"), validReadback},
		{"validation of a short readback", backupValidateStep, "", backupR3Field(backupR3Readback, "bytes_sent", 31), validReadback},
		{"validation of a readback algorithm", backupValidateStep, "", backupR3Field(backupR3Readback, "computed_checksum", map[string]any{"algorithm": "md5", "digest": backupTestDigest}), validReadback},
		{"validation of a readback digest", backupValidateStep, "", backupR3Field(backupR3Readback, "computed_checksum", map[string]any{"algorithm": "sha256", "digest": backupR3OtherDigest}), validReadback},
		{"validation of another object version", backupValidateStep, "", otherUpload, validReadback},
		{"validation of a misbound manifest object", backupValidateStep, "", withManifestObject(map[string]any{"data_set_id": "other"}),
			"error: manifest object 0 is bound to the wrong data set"},
		{"validation of a manifest object that is not an object", backupValidateStep, "", withManifestObject("world"),
			"error: manifest object 0 is bound to the wrong data set"},
	} {
		t.Run(test.name, func(t *testing.T) {
			operation := backupR3Copy(f.complete)
			if test.change != nil {
				test.change(t, &operation)
			}
			step := operationStep(operation, test.step)
			if test.stepID != "" {
				step.StepID = test.stepID
			}
			got := backupR3Resolve(t, f, operation, step)
			if test.want == "" {
				if strings.HasPrefix(got, "error: ") {
					t.Fatalf("ResolveStepInput = %s, want a resolved input", got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("ResolveStepInput = %s, want %s", got, test.want)
			}
		})
	}
}

func TestBackupR3ResolveValidationInputWithoutConsistency(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	operation := backupR3Copy(f.complete)
	backupR3SetStep(&operation, backupValidateStep, func(s *store.OperationStep) {
		input := decodeObject(s.Input)
		manifest := asStringMap(input["manifest"])
		manifest["consistency"] = "offline"
		s.Input = backupJSON(t, input)
	})
	got := decodeObject(json.RawMessage(backupR3Resolve(t, f, operation, operationStep(operation, backupValidateStep))))
	if consistency := asStringMap(got["manifest"])["consistency"]; consistency != "offline" {
		t.Fatalf("resolved consistency = %#v, want the planned value left as is", consistency)
	}
}

// backupR3NopSink is a stream sink that discards its input.
type backupR3NopSink struct{ io.Writer }

func (backupR3NopSink) Close() error { return nil }

// backupR3Transfers returns one valid transfer per expectation.
func backupR3Transfers(expectations []ServerBackupStreamExpectation, direction string) []adapter.StreamTransfer {
	transfers := make([]adapter.StreamTransfer, 0, len(expectations))
	for i, expectation := range expectations {
		transfer := adapter.StreamTransfer{
			Reference: capturestream.Reference{TransferID: fmt.Sprintf("%064x", i+1), Direction: direction,
				MaximumBytes: expectation.MaximumBytes, ExpiresAt: time.Now().UTC().Add(30 * time.Second)},
			Verify: func() error { return nil },
		}
		if direction == "controller_to_adapter" {
			transfer.Source = io.NopCloser(strings.NewReader(""))
		} else {
			transfer.Sink = backupR3NopSink{io.Discard}
		}
		transfers = append(transfers, transfer)
	}
	return transfers
}

// backupR3StreamCase issues stream transfers for one step.
type backupR3StreamCase struct {
	name     string
	step     string
	stepID   string
	change   func(*testing.T, *store.OperationRecord)
	deadline time.Duration
	zero     bool
	alter    func([]adapter.StreamTransfer) ([]adapter.StreamTransfer, error)
	want     string
	wantSeen string
}

func (test backupR3StreamCase) run(t *testing.T, f backupR3Fixture, operation store.OperationRecord) {
	t.Helper()
	if test.change != nil {
		test.change(t, &operation)
	}
	seen := "<not issued>"
	streams := backupR3Streams{issue: func(step store.OperationStep, expectations []ServerBackupStreamExpectation, direction string) ([]adapter.StreamTransfer, error) {
		seen = fmt.Sprintf("%s %+v", direction, expectations)
		transfers := backupR3Transfers(expectations, direction)
		if test.alter == nil {
			return transfers, nil
		}
		return test.alter(transfers)
	}}
	step := operationStep(operation, test.step)
	if test.stepID != "" {
		step.StepID = test.stepID
	}
	deadline := time.Now().Add(time.Minute)
	if test.deadline != 0 {
		deadline = time.Now().Add(test.deadline)
	}
	if test.zero {
		deadline = time.Time{}
	}
	transfers, err := f.backupR3Workflow(f.server, streams).StreamTransfersForStep(context.Background(), operation, step, deadline)
	got := backupR3ErrText(err)
	if err == nil {
		got = fmt.Sprintf("%d transfers", len(transfers))
	}
	if got != test.want || (test.wantSeen != "" && seen != test.wantSeen) {
		t.Fatalf("StreamTransfersForStep = %q (issued %s), want %q (issued %s)", got, seen, test.want, test.wantSeen)
	}
}

// backupR3StreamOperation is the complete backup with the storage steps'
// inputs resolved as the engine resolves them.
func backupR3StreamOperation(t *testing.T, f backupR3Fixture) store.OperationRecord {
	t.Helper()
	operation := backupR3Copy(f.complete)
	workflow := f.backupR3Workflow(f.server, backupR3Streams{})
	for _, stepID := range []string{backupR3Upload, backupR3Readback} {
		index := backupStepIndex(operation, stepID)
		resolved, err := workflow.ResolveStepInput(context.Background(), operation, operation.Steps[index])
		if err != nil {
			t.Fatal(err)
		}
		operation.Steps[index].ResolvedInput = resolved
	}
	return operation
}

func TestBackupR3StreamExpectations(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	operation := backupR3StreamOperation(t, f)
	withObjects := func(objects ...any) func(*testing.T, *store.OperationRecord) {
		return func(t *testing.T, o *store.OperationRecord) {
			manifest := decodeObject(stepField(effectiveStepInput(operationStep(*o, backupValidateStep)), "manifest"))
			manifest["objects"] = objects
			backupR3SetInputField(t, o, backupValidateStep, "manifest", manifest)
		}
	}
	rawInput := func(stepID, raw string) func(*testing.T, *store.OperationRecord) {
		return func(_ *testing.T, o *store.OperationRecord) {
			backupR3SetStep(o, stepID, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(raw) })
		}
	}
	digest := backupTestDigest
	for _, test := range []backupR3StreamCase{
		{name: "inspection", step: backupInspectInstanceStep, want: "0 transfers", wantSeen: "<not issued>"},
		{name: "capture", step: backupR3Capture, want: "1 transfers", wantSeen: "adapter_to_controller [{DataSetID:world SizeBytes:-1 MaximumBytes:64 SHA256:}]"},
		{name: "capture outside the plan", step: backupR3Capture, stepID: "capture-other", want: "capture stream is outside the trusted backup plan"},
		{name: "upload", step: backupR3Upload, want: "1 transfers", wantSeen: "controller_to_adapter [{DataSetID:world SizeBytes:32 MaximumBytes:32 SHA256:" + digest + "}]"},
		{name: "readback", step: backupR3Readback, want: "1 transfers", wantSeen: "adapter_to_controller [{DataSetID:world SizeBytes:32 MaximumBytes:32 SHA256:" + digest + "}]"},
		{name: "upload outside the plan", step: backupR3Upload, stepID: "upload-other", want: "storage stream is outside the trusted backup plan"},
		{name: "readback outside the plan", step: backupR3Readback, stepID: "readback-other", want: "storage stream is outside the trusted backup plan"},
		{name: "upload input not JSON", step: backupR3Upload, change: rawInput(backupR3Upload, `nope`), want: "invalid character 'o' in literal null (expecting 'u')"},
		{name: "upload without a size", step: backupR3Upload, change: backupR3InputField(backupR3Upload, "expected_size_bytes", nil), want: "storage stream input has no exact expected size"},
		{name: "upload without a checksum", step: backupR3Upload, change: backupR3InputField(backupR3Upload, "expected_checksum", nil), want: "1 transfers",
			wantSeen: "controller_to_adapter [{DataSetID:world SizeBytes:32 MaximumBytes:32 SHA256:}]"},
		{name: "empty upload", step: backupR3Upload, change: backupR3InputField(backupR3Upload, "expected_size_bytes", 0), want: "1 transfers",
			wantSeen: "controller_to_adapter [{DataSetID:world SizeBytes:0 MaximumBytes:1 SHA256:" + digest + "}]"},
		{name: "validation", step: backupValidateStep, want: "1 transfers", wantSeen: "controller_to_adapter [{DataSetID:world SizeBytes:32 MaximumBytes:32 SHA256:" + digest + "}]"},
		{name: "validation input not JSON", step: backupValidateStep, change: rawInput(backupValidateStep, `nope`), want: "invalid character 'o' in literal null (expecting 'u')"},
		{name: "validation without objects", step: backupValidateStep, change: withObjects(), want: "0 transfers", wantSeen: "<not issued>"},
		{name: "validation object without a size", step: backupValidateStep, change: withObjects(map[string]any{"data_set_id": "world"}),
			want: "validation manifest has an invalid object size"},
		{name: "validation object without a data set", step: backupValidateStep, change: withObjects(map[string]any{"size_bytes": 3, "checksum": "c"}),
			want: "validation manifest has an object without a data set ID"},
		{name: "validation of two empty objects", step: backupValidateStep,
			change: withObjects(map[string]any{"data_set_id": "a", "size_bytes": 0}, map[string]any{"data_set_id": "b", "size_bytes": 7, "checksum": "c"}),
			want:   "2 transfers", wantSeen: "controller_to_adapter [{DataSetID:a SizeBytes:0 MaximumBytes:1 SHA256:} {DataSetID:b SizeBytes:7 MaximumBytes:7 SHA256:c}]"},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, f, backupR3Copy(operation)) })
	}
}

func TestBackupR3StreamTransferChecks(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	operation := backupR3StreamOperation(t, f)
	const schema = "stream broker issued a handle that does not match the registered schema"
	const unsafe = "stream broker issued a handle with an unsafe direction, bound, or expiry"
	const binding = "stream broker did not bind its handle to exactly one correct-direction stream"
	each := func(change func(*adapter.StreamTransfer)) func([]adapter.StreamTransfer) ([]adapter.StreamTransfer, error) {
		return func(transfers []adapter.StreamTransfer) ([]adapter.StreamTransfer, error) {
			for i := range transfers {
				change(&transfers[i])
			}
			return transfers, nil
		}
	}
	expires := func(after time.Duration) func(*adapter.StreamTransfer) {
		return func(transfer *adapter.StreamTransfer) { transfer.Reference.ExpiresAt = time.Now().UTC().Add(after) }
	}
	twoObjects := func(t *testing.T, o *store.OperationRecord) {
		manifest := decodeObject(stepField(effectiveStepInput(operationStep(*o, backupValidateStep)), "manifest"))
		manifest["objects"] = []any{map[string]any{"data_set_id": "a", "size_bytes": 1}, map[string]any{"data_set_id": "b", "size_bytes": 1}}
		backupR3SetInputField(t, o, backupValidateStep, "manifest", manifest)
	}
	for _, test := range []backupR3StreamCase{
		{name: "broker failure", step: backupR3Upload, alter: func([]adapter.StreamTransfer) ([]adapter.StreamTransfer, error) {
			return nil, fmt.Errorf("broker unavailable")
		}, want: "broker unavailable"},
		{name: "missing transfer", step: backupR3Upload, alter: func([]adapter.StreamTransfer) ([]adapter.StreamTransfer, error) { return nil, nil },
			want: "stream broker did not issue exactly one transfer for every planned stream"},
		{name: "zero deadline", step: backupR3Upload, zero: true, want: "stream transfer issuance has no live invocation deadline"},
		{name: "past deadline", step: backupR3Upload, deadline: -time.Second, want: "stream transfer issuance has no live invocation deadline"},
		{name: "malformed transfer ID", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.TransferID = "short" }), want: schema},
		{name: "zero bound", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.MaximumBytes = 0 }), want: schema},
		{name: "wrong direction", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.Direction = "adapter_to_controller" }), want: unsafe},
		{name: "wrong bound", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.MaximumBytes = 33 }), want: unsafe},
		{name: "zero expiry", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.ExpiresAt = time.Time{} }), want: unsafe},
		{name: "expired", step: backupR3Upload, alter: each(expires(-time.Second)), want: unsafe},
		{name: "expires after the deadline", step: backupR3Upload, alter: each(expires(2 * time.Minute)), want: unsafe},
		{name: "expires after five minutes", step: backupR3Upload, deadline: 10 * time.Minute, alter: each(expires(6 * time.Minute)), want: unsafe},
		{name: "expires within five minutes", step: backupR3Upload, deadline: 10 * time.Minute, alter: each(expires(4 * time.Minute)), want: "1 transfers"},
		{name: "reused transfer ID", step: backupValidateStep, change: twoObjects,
			alter: each(func(transfer *adapter.StreamTransfer) { transfer.Reference.TransferID = strings.Repeat("a", 64) }),
			want:  "stream broker reused a transfer ID within one invocation"},
		{name: "no verifier", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Verify = nil }), want: binding},
		{name: "upload without a source", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Source = nil }), want: binding},
		{name: "upload with a sink", step: backupR3Upload, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Sink = backupR3NopSink{io.Discard} }), want: binding},
		{name: "readback without a sink", step: backupR3Readback, alter: each(func(transfer *adapter.StreamTransfer) { transfer.Sink = nil }), want: binding},
		{name: "readback with a source", step: backupR3Readback, alter: each(func(transfer *adapter.StreamTransfer) {
			transfer.Source = io.NopCloser(strings.NewReader(""))
		}), want: binding},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, f, backupR3Copy(operation)) })
	}
}

func TestBackupR3StreamTransferGuards(t *testing.T) {
	f := newBackupR3Fixture(t, false)
	step := operationStep(f.complete, backupR3Capture)
	const want = "initialized backup workflow and stream context are required"
	withoutBroker := f.backupR3Workflow(f.server, nil)
	for name, call := range map[string]func() error{
		"nil context": func() error {
			_, err := f.backupR3Workflow(f.server, backupR3Streams{}).StreamTransfersForStep(nil, f.complete, step, time.Now().Add(time.Minute))
			return err
		},
		"nil workflow": func() error {
			_, err := (*ServerBackupWorkflow)(nil).StreamTransfersForStep(context.Background(), f.complete, step, time.Now().Add(time.Minute))
			return err
		},
		"nil broker": func() error {
			_, err := withoutBroker.StreamTransfersForStep(context.Background(), f.complete, step, time.Now().Add(time.Minute))
			return err
		},
	} {
		if got := backupR3ErrText(call()); got != want {
			t.Errorf("%s: StreamTransfersForStep = %q, want %q", name, got, want)
		}
	}
}
