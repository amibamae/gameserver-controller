package captureinvoke

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin every operand of the capture runtime's
// durable-record, evidence, dispatch and destination predicates.

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
