package captureinvoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin New's argument checks, every operand of
// the capture predicates, the recovery request and evidence checks, the
// scopes staging receives, and countingSink's write accounting.

func captureR5Error(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

type captureR5Predicate struct {
	name string
	got  bool
	want bool
}

func captureR5Predicates(t *testing.T, tests []captureR5Predicate) {
	t.Helper()
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("%s: got %v, want %v", test.name, test.got, test.want)
		}
	}
}

func TestCaptureR5NewArguments(t *testing.T) {
	h := newCaptureHarness(t)
	const message = "capture runtime requires matching registries, records, broker, staging, and byte limit"
	tests := []struct {
		name     string
		adapters *adapter.Registry
		records  Records
		broker   *capturestream.Broker
		staging  Staging
		maximum  int64
		valid    bool
	}{
		{"valid at the lower byte limit", h.registry, h.records, h.broker, h.staging, 1, true},
		{"valid at the largest exact JSON integer", h.registry, h.records, h.broker, h.staging, 9007199254740991, true},
		{"no adapters", nil, h.records, h.broker, h.staging, 10, false},
		{"adapter registry for another protocol", &adapter.Registry{}, h.records, h.broker, h.staging, 10, false},
		{"no records", h.registry, nil, h.broker, h.staging, 10, false},
		{"no broker", h.registry, h.records, nil, h.staging, 10, false},
		{"no staging", h.registry, h.records, h.broker, nil, 10, false},
		{"zero byte limit", h.registry, h.records, h.broker, h.staging, 0, false},
		{"byte limit above the exact JSON integers", h.registry, h.records, h.broker, h.staging, 9007199254740992, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := New(test.adapters, h.schemas, test.records, test.broker, test.staging, test.maximum)
			if test.valid {
				if err != nil || runtime == nil || runtime.maximum != test.maximum {
					t.Fatalf("New = %v, %v", runtime, err)
				}
				return
			}
			captureR5Error(t, err, message)
			if runtime != nil {
				t.Fatal("New returned a runtime with an error")
			}
		})
	}
	t.Run("no schemas", func(t *testing.T) {
		_, err := New(h.registry, nil, h.records, h.broker, h.staging, 10)
		captureR5Error(t, err, message)
	})
}

// captureR5Recovery returns fixed sealed evidence, records the scope it was
// asked for and whether the caller closed the stream, and can cancel the
// caller's context while it opens the evidence.
type captureR5Recovery struct {
	staging
	receipt capturestream.Receipt
	output  json.RawMessage
	noData  bool
	cancel  context.CancelFunc
	scope   *capturestream.Scope
	data    *captureR5Data
}

type captureR5Data struct {
	io.Reader
	closed bool
}

func (d *captureR5Data) Close() error {
	d.closed = true
	return errors.New("close failed")
}

func (s captureR5Recovery) OpenVerifiedCapture(_ context.Context, scope capturestream.Scope) (io.ReadCloser, capturestream.Receipt, json.RawMessage, error) {
	*s.scope = scope
	if s.cancel != nil {
		s.cancel()
	}
	if s.noData {
		return nil, s.receipt, s.output, nil
	}
	return s.data, s.receipt, s.output, nil
}

func captureR5RecoveryHarness(t *testing.T) (*captureHarness, *captureR5Recovery) {
	h := recoverableHarness(t)
	fixed := h.staging.(fixedRecovery)
	recovery := &captureR5Recovery{staging: fixed.staging, receipt: fixed.receipt, output: fixed.output,
		scope: &capturestream.Scope{}, data: &captureR5Data{Reader: bytes.NewReader([]byte("abc"))}}
	h.staging = recovery
	return h, recovery
}

func TestCaptureR5RecoverRequestValidation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	h := recoverableHarness(t)
	var missing *Runtime
	runtime := h.runtime()
	tests := []struct {
		name        string
		runtime     *Runtime
		ctx         context.Context
		operationID string
		stepID      string
	}{
		{"nil runtime", missing, context.Background(), h.scope.OperationID, h.scope.StepID},
		{"nil context", runtime, nil, h.scope.OperationID, h.scope.StepID},
		{"canceled context", runtime, canceled, h.scope.OperationID, h.scope.StepID},
		{"empty operation ID", runtime, context.Background(), "", h.scope.StepID},
		{"empty step ID", runtime, context.Background(), h.scope.OperationID, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runtime.RecoverSealedCapture(test.ctx, test.operationID, test.stepID)
			captureR5Error(t, err, "invalid capture recovery request")
		})
	}
}

func TestCaptureR5RecoverDurableState(t *testing.T) {
	const notAtStep = "capture operation is not at the exact recoverable step"
	const notUnresolved = "capture step is not unresolved"
	tests := []struct {
		name   string
		stepID string
		change func(*captureHarness)
		want   string
	}{
		{"operation record for another ID", "", func(h *captureHarness) { h.records.op.Record.OperationID = "other" }, notAtStep},
		{"operation is not a backup", "", func(h *captureHarness) { h.records.op.Record.OperationType = "server.restore" }, notAtStep},
		{"operation is still running", "", func(h *captureHarness) { h.records.op.Record.Status = protocolstate.OperationRunning }, notAtStep},
		{"backup has no initial lifecycle state", "", func(h *captureHarness) { h.records.op.Record.BackupInitialLifecycleState = "" }, notAtStep},
		{"operation has no current step", "", func(h *captureHarness) { h.records.op.Record.CurrentStepID = nil }, notAtStep},
		{"operation is at another step", "", func(h *captureHarness) {
			other := "other"
			h.records.op.Record.CurrentStepID = &other
		}, notAtStep},
		{"operation for another protocol", "", func(h *captureHarness) { h.records.op.Record.ProtocolVersion = "0.1.0" }, notAtStep},
		{"current step is not recorded", "missing", func(h *captureHarness) {
			missing := "missing"
			h.records.op.Record.CurrentStepID = &missing
		}, notUnresolved},
		{"step is still running", "", func(h *captureHarness) { h.step().Status = protocolstate.StepRunning }, notUnresolved},
		{"step was never dispatched", "", func(h *captureHarness) { h.step().Attempt = 0 }, notUnresolved},
		{"step is not a capture", "", func(h *captureHarness) { h.step().CapabilityName = "game.backup.validate" }, notUnresolved},
		{"step input fails its schema", "", func(h *captureHarness) { h.step().Input = json.RawMessage(`{}`) }, "recorded capture capability or input is incompatible"},
		{"operation targets another server", "", func(h *captureHarness) { h.records.op.Record.Target.ServerID = "other" }, "recorded capture target differs from its operation"},
		{"operation targets another environment", "", func(h *captureHarness) { h.records.op.Record.Target.EnvironmentID = "production" }, "recorded capture target differs from its operation"},
		{"lock is unavailable", "", func(h *captureHarness) { h.records.lockErr = errors.New("missing") }, "capture recovery requires the exact durable server lock"},
		{"lock for another scope", "", func(h *captureHarness) { h.records.lock.Scope = "other" }, "capture recovery requires the exact durable server lock"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, recovery := captureR5RecoveryHarness(t)
			test.change(h)
			stepID := h.scope.StepID
			if test.stepID != "" {
				stepID = test.stepID
			}
			_, err := h.runtime().RecoverSealedCapture(context.Background(), h.scope.OperationID, stepID)
			captureR5Error(t, err, test.want)
			if *recovery.scope != (capturestream.Scope{}) {
				t.Fatal("rejected recovery opened sealed evidence")
			}
		})
	}
}

func TestCaptureR5RecoverEvidence(t *testing.T) {
	const invalid = "sealed capture response is missing or invalid"
	tests := []struct {
		name   string
		change func(*captureHarness, *captureR5Recovery, context.CancelFunc)
		want   string
		closed bool
	}{
		{"no stream and no error", func(_ *captureHarness, r *captureR5Recovery, _ context.CancelFunc) { r.noData = true },
			"sealed capture evidence is unavailable or invalid", false},
		{"context canceled while opening", func(_ *captureHarness, r *captureR5Recovery, cancel context.CancelFunc) { r.cancel = cancel }, invalid, true},
		{"empty sealed response", func(_ *captureHarness, r *captureR5Recovery, _ context.CancelFunc) { r.output = nil }, invalid, true},
		{"sealed response fails its schema", func(_ *captureHarness, r *captureR5Recovery, _ context.CancelFunc) {
			r.output = json.RawMessage(`{"outcome":"captured"}`)
		}, invalid, true},
		{"receipt digest differs", func(_ *captureHarness, r *captureR5Recovery, _ context.CancelFunc) { r.receipt.SHA256 = "00" },
			"sealed capture response does not match measured stream evidence", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, recovery := captureR5RecoveryHarness(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			test.change(h, recovery, cancel)
			_, err := h.runtime().RecoverSealedCapture(ctx, h.scope.OperationID, h.scope.StepID)
			captureR5Error(t, err, test.want)
			if recovery.data.closed != test.closed {
				t.Fatalf("stream closed = %v, want %v", recovery.data.closed, test.closed)
			}
		})
	}
}

func TestCaptureR5RecoverReturnsTheSealedCapture(t *testing.T) {
	h, recovery := captureR5RecoveryHarness(t)
	recovered, err := h.runtime().RecoverSealedCapture(context.Background(), h.scope.OperationID, h.scope.StepID)
	if err != nil {
		t.Fatal(err)
	}
	if *recovery.scope != h.scope {
		t.Fatalf("recovery opened scope %+v, want %+v", *recovery.scope, h.scope)
	}
	if recovered.Data != recovery.data || recovered.Receipt != recovery.receipt || !bytes.Equal(recovered.Output, recovery.output) || recovery.data.closed {
		t.Fatalf("recovered %+v, want the open sealed evidence", recovered)
	}
	recovered.Output[0] = ' '
	if recovery.output[0] == ' ' {
		t.Fatal("recovered output shares memory with staging")
	}
}

// captureR5ScopeStaging records the scope Invoke opens a stage for.
type captureR5ScopeStaging struct {
	stage *stage
	scope capturestream.Scope
}

func (s *captureR5ScopeStaging) Open(_ context.Context, scope capturestream.Scope) (Stage, error) {
	s.scope = scope
	return s.stage, nil
}

func TestCaptureR5InvokeOpensTheDurableScope(t *testing.T) {
	h := newCaptureHarness(t)
	recording := &captureR5ScopeStaging{stage: h.sink}
	h.staging = recording
	if _, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request); err != nil {
		t.Fatal(err)
	}
	if recording.scope != h.scope {
		t.Fatalf("Invoke opened scope %+v, want %+v", recording.scope, h.scope)
	}
}

// captureR5Op returns a copy of the harness operation after change.
func captureR5Op(h *captureHarness, change func(*store.OperationRecord)) store.OperationRecord {
	op := h.records.op.Record
	op.Steps = append([]store.OperationStep(nil), op.Steps...)
	change(&op)
	return op
}

func captureR5OtherStep(op *store.OperationRecord) {
	other := "other"
	op.CurrentStepID = &other
}

func TestCaptureR5RecoveryPredicates(t *testing.T) {
	h := recoverableHarness(t)
	r := h.runtime()
	at := func(change func(*store.OperationRecord)) bool {
		return r.atRecoverableCaptureStep(captureR5Op(h, change), h.scope.OperationID, h.scope.StepID)
	}
	step := func(change func(*store.OperationStep)) bool {
		s := *h.step()
		change(&s)
		return unresolvedCaptureStep(&s)
	}
	targets := func(change func(*store.OperationRecord)) bool {
		return captureTargetsOperation(h.input, captureR5Op(h, change))
	}
	captureR5Predicates(t, []captureR5Predicate{
		{"recoverable step", at(func(*store.OperationRecord) {}), true},
		{"another operation ID", at(func(op *store.OperationRecord) { op.OperationID = "other" }), false},
		{"another operation type", at(func(op *store.OperationRecord) { op.OperationType = "server.restore" }), false},
		{"running operation", at(func(op *store.OperationRecord) { op.Status = protocolstate.OperationRunning }), false},
		{"no initial lifecycle state", at(func(op *store.OperationRecord) { op.BackupInitialLifecycleState = "" }), false},
		{"no current step", at(func(op *store.OperationRecord) { op.CurrentStepID = nil }), false},
		{"another current step", at(captureR5OtherStep), false},
		{"another protocol", at(func(op *store.OperationRecord) { op.ProtocolVersion = "0.1.0" }), false},
		{"unresolved step", step(func(*store.OperationStep) {}), true},
		{"nil step", unresolvedCaptureStep(nil), false},
		{"running step", step(func(s *store.OperationStep) { s.Status = protocolstate.StepRunning }), false},
		{"undispatched step", step(func(s *store.OperationStep) { s.Attempt = 0 }), false},
		{"other capability", step(func(s *store.OperationStep) { s.CapabilityName = "game.backup.validate" }), false},
		{"recorded output", step(func(s *store.OperationStep) { s.Output = json.RawMessage(`{}`) }), false},
		{"targets the operation", targets(func(*store.OperationRecord) {}), true},
		{"another server", targets(func(op *store.OperationRecord) { op.Target.ServerID = "other" }), false},
		{"another environment", targets(func(op *store.OperationRecord) { op.Target.EnvironmentID = "other" }), false},
		{"another instance", targets(func(op *store.OperationRecord) { op.Target.InstanceRef = json.RawMessage(`{}`) }), false},
		{"reordered instance JSON", targets(func(op *store.OperationRecord) {
			op.Target.InstanceRef = json.RawMessage(`{"resource_kind":"instance","external_id":"instance-demo-01","environment_id":"staging","adapter_id":"hosting.vultr"}`)
		}), true},
	})
}

// captureR5Result returns a captured result and receipt that match.
func captureR5Result(h *captureHarness) (result, capturestream.Receipt) {
	output := result{capture: h.input, Outcome: "captured", StreamCompleted: true, Size: 3}
	output.Checksum.Algorithm = "sha256"
	output.Checksum.Digest = "digest"
	return output, capturestream.Receipt{Scope: h.scope, SizeBytes: 3, SHA256: "digest"}
}

func TestCaptureR5EvidencePredicates(t *testing.T) {
	h := newCaptureHarness(t)
	sealed := func(change func(*result, *capturestream.Receipt)) bool {
		output, receipt := captureR5Result(h)
		change(&output, &receipt)
		return resultMatchesReceipt(output, receipt)
	}
	measured := func(change func(*result, *capturestream.Receipt)) bool {
		output, receipt := captureR5Result(h)
		change(&output, &receipt)
		return streamEvidenceMatches(receipt, h.scope, output)
	}
	captureR5Predicates(t, []captureR5Predicate{
		{"sealed result matches", sealed(func(*result, *capturestream.Receipt) {}), true},
		{"sealed result not captured", sealed(func(o *result, _ *capturestream.Receipt) { o.Outcome = "rejected" }), false},
		{"sealed stream incomplete", sealed(func(o *result, _ *capturestream.Receipt) { o.StreamCompleted = false }), false},
		{"sealed size differs", sealed(func(o *result, _ *capturestream.Receipt) { o.Size = 4 }), false},
		{"sealed algorithm differs", sealed(func(o *result, _ *capturestream.Receipt) { o.Checksum.Algorithm = "md5" }), false},
		{"sealed digest differs", sealed(func(_ *result, r *capturestream.Receipt) { r.SHA256 = "other" }), false},
		{"sealed result ignores the receipt scope", sealed(func(_ *result, r *capturestream.Receipt) { r.Scope.CaptureID = "other" }), true},
		{"measured stream matches", measured(func(*result, *capturestream.Receipt) {}), true},
		{"measured stream ignores the reported outcome", measured(func(o *result, _ *capturestream.Receipt) { o.Outcome = "rejected" }), true},
		{"measured scope differs", measured(func(_ *result, r *capturestream.Receipt) { r.Scope.CaptureID = "other" }), false},
		{"measured stream incomplete", measured(func(o *result, _ *capturestream.Receipt) { o.StreamCompleted = false }), false},
		{"measured size differs", measured(func(_ *result, r *capturestream.Receipt) { r.SizeBytes = 4 }), false},
		{"measured algorithm differs", measured(func(o *result, _ *capturestream.Receipt) { o.Checksum.Algorithm = "md5" }), false},
		{"measured digest differs", measured(func(o *result, _ *capturestream.Receipt) { o.Checksum.Digest = "other" }), false},
	})
}

func TestCaptureR5DispatchPredicates(t *testing.T) {
	h := newCaptureHarness(t)
	c := h.request.Params.RequestContext
	version := h.request.Params.ProtocolVersion
	running := func(change func(*store.OperationRecord)) bool {
		return atRunningBackupStep(captureR5Op(h, change), c, version)
	}
	step := func(change func(*store.OperationStep)) bool {
		s := *h.step()
		change(&s)
		return runningCaptureStepMatches(&s, h.descriptor.AdapterID, h.request.Params.Input)
	}
	deadline := c.Deadline
	before, after := deadline.Add(-time.Second), deadline.Add(time.Second)
	captureR5Predicates(t, []captureR5Predicate{
		{"running backup step", running(func(*store.OperationRecord) {}), true},
		{"another operation type", running(func(op *store.OperationRecord) { op.OperationType = "server.restore" }), false},
		{"reconciling operation", running(func(op *store.OperationRecord) { op.Status = protocolstate.OperationReconciling }), false},
		{"no initial lifecycle state", running(func(op *store.OperationRecord) { op.BackupInitialLifecycleState = "" }), false},
		{"no current step", running(func(op *store.OperationRecord) { op.CurrentStepID = nil }), false},
		{"another current step", running(captureR5OtherStep), false},
		{"another server", running(func(op *store.OperationRecord) { op.Target.ServerID = "other" }), false},
		{"another environment", running(func(op *store.OperationRecord) { op.Target.EnvironmentID = "other" }), false},
		{"another protocol", running(func(op *store.OperationRecord) { op.ProtocolVersion = "0.1.0" }), false},
		{"running capture step", step(func(*store.OperationStep) {}), true},
		{"nil step", runningCaptureStepMatches(nil, h.descriptor.AdapterID, h.request.Params.Input), false},
		{"step outcome unknown", step(func(s *store.OperationStep) { s.Status = protocolstate.StepOutcomeUnknown }), false},
		{"undispatched step", step(func(s *store.OperationStep) { s.Attempt = 0 }), false},
		{"step for another adapter", step(func(s *store.OperationStep) { s.AdapterID = "game.other" }), false},
		{"step for another capability", step(func(s *store.OperationStep) { s.CapabilityName = "game.backup.validate" }), false},
		{"step with another input", step(func(s *store.OperationStep) { s.Input = json.RawMessage(`{}`) }), false},
		{"no operation deadline", withinOperationDeadline(store.OperationRecord{}, c), true},
		{"operation deadline equal", withinOperationDeadline(store.OperationRecord{Deadline: &deadline}, c), true},
		{"operation deadline later", withinOperationDeadline(store.OperationRecord{Deadline: &after}, c), true},
		{"operation deadline earlier", withinOperationDeadline(store.OperationRecord{Deadline: &before}, c), false},
		{"targets the request", captureTargetsRequest(h.input, c, h.records.op.Record), true},
		{"request for another server", captureTargetsRequest(h.input, adapter.RequestContext{ServerID: "other", EnvironmentID: c.EnvironmentID}, h.records.op.Record), false},
		{"request for another environment", captureTargetsRequest(h.input, adapter.RequestContext{ServerID: c.ServerID, EnvironmentID: "other"}, h.records.op.Record), false},
		{"operation for another instance", captureTargetsRequest(h.input, c, captureR5Op(h, func(op *store.OperationRecord) { op.Target.InstanceRef = json.RawMessage(`{}`) })), false},
	})
}

// captureR5Destination returns a request context with one valid external
// destination after change; attach controls whether a writer is attached.
func captureR5Destination(change func(*capturestream.Reference, *time.Time), attach bool) adapter.RequestContext {
	reference := capturestream.Reference{TransferID: "t", Direction: "adapter_to_controller", MaximumBytes: 10, ExpiresAt: time.Now().Add(time.Minute)}
	deadline := time.Now().Add(2 * time.Minute)
	change(&reference, &deadline)
	c := adapter.RequestContext{Deadline: deadline, StreamTransfers: []capturestream.Reference{reference}}
	if attach {
		c.SetStreamDestinations(map[string]io.WriteCloser{reference.TransferID: &closeBuffer{}})
	}
	return c
}

func TestCaptureR5ValidCaptureDestination(t *testing.T) {
	r := newCaptureHarness(t).runtime()
	valid := func(change func(*capturestream.Reference, *time.Time)) bool {
		return r.validCaptureDestination(captureR5Destination(change, true))
	}
	claimed := captureR5Destination(func(*capturestream.Reference, *time.Time) {}, true)
	if _, err := claimed.CreateStream("t"); err != nil {
		t.Fatal(err)
	}
	captureR5Predicates(t, []captureR5Predicate{
		{"valid destination", valid(func(*capturestream.Reference, *time.Time) {}), true},
		{"lower byte limit", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.MaximumBytes = 1 }), true},
		{"empty transfer ID", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.TransferID = "" }), false},
		{"wrong direction", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.Direction = "controller_to_adapter" }), false},
		{"zero byte limit", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.MaximumBytes = 0 }), false},
		{"byte limit above the runtime's", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.MaximumBytes = 11 }), false},
		{"already expired", valid(func(ref *capturestream.Reference, _ *time.Time) { ref.ExpiresAt = time.Now().Add(-time.Second) }), false},
		{"expires at the deadline", valid(func(ref *capturestream.Reference, deadline *time.Time) { *deadline = ref.ExpiresAt }), true},
		{"expires after the deadline", valid(func(ref *capturestream.Reference, deadline *time.Time) { *deadline = ref.ExpiresAt.Add(-time.Second) }), false},
		{"expires within five minutes", valid(func(ref *capturestream.Reference, deadline *time.Time) {
			*deadline = time.Now().Add(10 * time.Minute)
			ref.ExpiresAt = time.Now().Add(5*time.Minute - 5*time.Second)
		}), true},
		{"expires after five minutes", valid(func(ref *capturestream.Reference, deadline *time.Time) {
			*deadline = time.Now().Add(10 * time.Minute)
			ref.ExpiresAt = time.Now().Add(5*time.Minute + 5*time.Second)
		}), false},
		{"no attached writer", r.validCaptureDestination(captureR5Destination(func(*capturestream.Reference, *time.Time) {}, false)), false},
		{"writer already claimed", r.validCaptureDestination(claimed), false},
	})
}

func TestCaptureR5SameCapture(t *testing.T) {
	h := newCaptureHarness(t)
	other := h.input
	other.CaptureID = "other"
	reordered := h.input
	reordered.Target.InstanceRef = json.RawMessage(`{"external_id":"instance-demo-01","resource_kind":"instance","environment_id":"staging","adapter_id":"hosting.vultr"}`)
	broken := h.input
	broken.Target.InstanceRef = json.RawMessage(`{`)
	captureR5Predicates(t, []captureR5Predicate{
		{"same capture", sameCapture(h.input, h.input), true},
		{"same capture with reordered JSON", sameCapture(h.input, reordered), true},
		{"another capture ID", sameCapture(h.input, other), false},
		{"requested capture cannot be encoded", sameCapture(broken, h.input), false},
		{"reported capture cannot be encoded", sameCapture(h.input, broken), false},
		{"neither capture can be encoded", sameCapture(broken, broken), false},
	})
}

// captureR5Writer reports n bytes written (len(p) when n is negative) and err.
type captureR5Writer struct {
	n     int
	err   error
	calls int
}

func (w *captureR5Writer) Write(p []byte) (int, error) {
	w.calls++
	if w.n < 0 {
		return len(p), w.err
	}
	return w.n, w.err
}

func TestCaptureR5CountingSinkWrite(t *testing.T) {
	sinkErr, mirrorErr := errors.New("sink failed"), errors.New("mirror failed")
	tests := []struct {
		name        string
		sink        captureR5Writer
		mirror      *captureR5Writer
		wantN       int
		wantErr     error
		wantSize    int64
		mirrorCalls int
	}{
		{"complete write without a mirror", captureR5Writer{n: -1}, nil, 3, nil, 3, 0},
		{"complete write with a mirror", captureR5Writer{n: -1}, &captureR5Writer{n: -1}, 3, nil, 3, 1},
		{"sink error skips the mirror", captureR5Writer{n: 1, err: sinkErr}, &captureR5Writer{n: -1}, 1, sinkErr, 1, 0},
		{"short sink write skips the mirror", captureR5Writer{n: 2}, &captureR5Writer{n: -1}, 2, nil, 2, 0},
		{"mirror error counts nothing", captureR5Writer{n: -1}, &captureR5Writer{n: 1, err: mirrorErr}, 1, mirrorErr, 0, 1},
		{"short mirror write counts nothing", captureR5Writer{n: -1}, &captureR5Writer{n: 2}, 2, io.ErrShortWrite, 0, 1},
		{"sink claims more than it was given", captureR5Writer{n: 5}, &captureR5Writer{n: -1}, 5, nil, 0, 0},
		{"sink writes nothing", captureR5Writer{n: 0, err: sinkErr}, nil, 0, sinkErr, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := test.sink
			w := &countingSink{sink: &sink}
			if test.mirror != nil {
				w.mirror = test.mirror
			}
			n, err := w.Write([]byte("abc"))
			if n != test.wantN || err != test.wantErr || w.size.Load() != test.wantSize {
				t.Fatalf("Write = %d, %v (size %d); want %d, %v (size %d)", n, err, w.size.Load(), test.wantN, test.wantErr, test.wantSize)
			}
			if test.mirror != nil && test.mirror.calls != test.mirrorCalls {
				t.Fatalf("mirror calls = %d, want %d", test.mirror.calls, test.mirrorCalls)
			}
		})
	}
}
