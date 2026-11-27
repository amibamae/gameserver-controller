package main

import (
	"encoding/json"
	"testing"
)

// pcR6CaptureCase returns the fixture, its input and the valid offline
// capture as a merged case, ready for one field to be changed.
func pcR6CaptureCase(t *testing.T) (map[string]any, map[string]any, backupCaptureCase) {
	t.Helper()
	_, fixture, input, evaluation := backupCaptureFixtureCase(t, "valid-offline-capture")
	return fixture, input, newBackupCaptureCase(input, evaluation)
}

func TestBackupCaptureStringFieldsMatch(t *testing.T) {
	left := map[string]any{"a": "x", "b": "y", "n": json.Number("1")}
	tests := []struct {
		name   string
		right  map[string]any
		fields []string
		want   bool
	}{
		{"no fields", map[string]any{}, nil, true},
		{"all fields equal", map[string]any{"a": "x", "b": "y"}, []string{"a", "b"}, true},
		{"second field differs", map[string]any{"a": "x", "b": "z"}, []string{"a", "b"}, false},
		{"field missing on one side", map[string]any{"a": "x"}, []string{"a", "b"}, false},
		{"non-string reads as empty and matches a missing field", map[string]any{}, []string{"n"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcR6CheckBool(t, "match", backupCaptureStringFieldsMatch(left, test.right, test.fields...), test.want)
		})
	}
}

func TestBackupCaptureSettlementNeedsReconciliation(t *testing.T) {
	settled := backupCaptureSettlement{outputValid: true, captureAccepted: true, bindingExact: true, dispatchPermitted: true}
	tests := []struct {
		name   string
		mutate func(*backupCaptureSettlement)
		want   bool
	}{
		{"invalid output", func(s *backupCaptureSettlement) { s.outputValid = false; s.outcome = "captured" }, true},
		{"unknown outcome", func(s *backupCaptureSettlement) { s.outcome = "outcome_unknown" }, true},
		{"accepted capture", func(s *backupCaptureSettlement) { s.outcome = "captured" }, false},
		{"captured but not accepted", func(s *backupCaptureSettlement) { s.outcome = "captured"; s.captureAccepted = false }, true},
		{"clean rejection", func(s *backupCaptureSettlement) { s.outcome = "rejected" }, false},
		{"unbound rejection", func(s *backupCaptureSettlement) { s.outcome = "rejected"; s.bindingExact = false }, true},
		{"rejection never permitted", func(s *backupCaptureSettlement) { s.outcome = "rejected"; s.dispatchPermitted = false }, true},
		{"rejection that streamed bytes", func(s *backupCaptureSettlement) { s.outcome = "rejected"; s.streamedBytes = 1 }, true},
		{"other outcome", func(s *backupCaptureSettlement) { s.outcome = "pending" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := settled
			test.mutate(&s)
			pcR6CheckBool(t, "needsReconciliation", s.needsReconciliation(), test.want)
		})
	}
}

func TestBackupCaptureStreamCompleted(t *testing.T) {
	tests := []struct {
		name       string
		output     map[string]any
		evaluation map[string]any
		want       bool
	}{
		{"completed with default transport", map[string]any{"stream_completed": true}, map[string]any{}, true},
		{"completion not reported", map[string]any{}, map[string]any{}, false},
		{"completion reported as a string", map[string]any{"stream_completed": "true"}, map[string]any{}, false},
		{"stream incomplete", map[string]any{"stream_completed": false}, map[string]any{}, false},
		{"transport incomplete", map[string]any{"stream_completed": true}, map[string]any{"stream_transport_complete": false}, false},
		{"transport complete", map[string]any{"stream_completed": true}, map[string]any{"stream_transport_complete": true}, true},
		{"non-bool transport flag is ignored", map[string]any{"stream_completed": true}, map[string]any{"stream_transport_complete": "no"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcR6CheckBool(t, "completed", backupCaptureStreamCompleted(test.output, test.evaluation), test.want)
		})
	}
}

func TestBackupCaptureStreamWithinBounds(t *testing.T) {
	tests := []struct {
		name     string
		maximum  any
		reported any
		streamed int64
		want     bool
	}{
		{"within the maximum", json.Number("3"), json.Number("2"), 2, true},
		{"exactly the maximum", json.Number("3"), json.Number("3"), 3, true},
		{"maximum not an integer", "3", json.Number("3"), 3, false},
		{"reported size not an integer", json.Number("3"), "3", 3, false},
		{"negative maximum", json.Number("-1"), json.Number("0"), 0, false},
		{"reported size differs from streamed size", json.Number("3"), json.Number("2"), 3, false},
		{"streamed more than the maximum", json.Number("2"), json.Number("3"), 3, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transfer := map[string]any{"maximum_bytes": test.maximum}
			output := map[string]any{"size_bytes": test.reported}
			pcR6CheckBool(t, "withinBounds", backupCaptureStreamWithinBounds(transfer, output, test.streamed), test.want)
		})
	}
}

func TestBackupCaptureChecksumValid(t *testing.T) {
	const abcDigest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	tests := []struct {
		name     string
		checksum any
		want     bool
	}{
		{"sha256 of the stream", map[string]any{"algorithm": "sha256", "digest": abcDigest}, true},
		{"another algorithm", map[string]any{"algorithm": "sha512", "digest": abcDigest}, false},
		{"another digest", map[string]any{"algorithm": "sha256", "digest": "00"}, false},
		{"no checksum", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := map[string]any{"checksum": test.checksum}
			pcR6CheckBool(t, "checksumValid", backupCaptureChecksumValid(output, []byte("abc")), test.want)
		})
	}
}

func TestBackupCaptureScopeSizeMatches(t *testing.T) {
	tests := []struct {
		name           string
		scope, granted any
		want           bool
	}{
		{"same size", json.Number("3"), json.Number("3"), true},
		{"different size", json.Number("4"), json.Number("3"), false},
		{"transfer size not an integer", json.Number("3"), "3", false},
		{"scope size not an integer", "3", json.Number("3"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := map[string]any{"maximum_bytes": test.scope}
			transfer := map[string]any{"maximum_bytes": test.granted}
			pcR6CheckBool(t, "sizeMatches", backupCaptureScopeSizeMatches(scope, transfer), test.want)
		})
	}
}

// TestBackupCaptureScopeChecks starts from the valid offline capture,
// changes one field, and expects each scope predicate to turn false exactly
// when that field belongs to it.
func TestBackupCaptureScopeChecks(t *testing.T) {
	tests := []struct {
		name                                string
		mutate                              func(fixture, input map[string]any, bc backupCaptureCase)
		grantsTransfer, namesStep, namesCap bool
	}{
		{"valid capture", func(map[string]any, map[string]any, backupCaptureCase) {}, true, true, true},
		{"scope size differs", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["maximum_bytes"] = json.Number("4") }, false, true, true},
		{"controller-to-adapter transfer", func(_, _ map[string]any, bc backupCaptureCase) {
			bc.transfer["direction"] = "controller_to_adapter"
			bc.scope["direction"] = "controller_to_adapter"
		}, false, true, true},
		{"scope for another transfer", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["transfer_id"] = "other" }, false, true, true},
		{"scope expires at another time", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["expires_at"] = "2026-11-07T10:04:00Z" }, false, true, true},
		{"scope for another operation", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["operation_id"] = "other" }, true, false, true},
		{"scope for another step", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["step_id"] = "other" }, true, false, true},
		{"scope for another capability", func(fixture, _ map[string]any, _ backupCaptureCase) { fixture["capability"] = "game.backup.prepare" }, true, false, true},
		{"scope for another adapter", func(_, input map[string]any, _ backupCaptureCase) { input["adapter_id"] = "game.other" }, true, false, true},
		{"scope for another target", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["target"] = map[string]any{} }, true, true, false},
		{"scope for another data set", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["data_set_id"] = "plugins" }, true, true, false},
		{"scope for another capture", func(_, _ map[string]any, bc backupCaptureCase) { bc.scope["capture_id"] = "other" }, true, true, false},
		{"invocation context for another environment", func(_, _ map[string]any, bc backupCaptureCase) {
			bc.requestContext["environment_id"] = "production"
		}, true, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, input, bc := pcR6CaptureCase(t)
			test.mutate(fixture, input, bc)
			pcR6CheckBool(t, "grantsTransfer", backupCaptureScopeGrantsTransfer(bc.scope, bc.transfer), test.grantsTransfer)
			pcR6CheckBool(t, "namesStep", backupCaptureScopeNamesStep(fixture, input, bc.scope, bc.requestContext), test.namesStep)
			pcR6CheckBool(t, "namesCapture", backupCaptureScopeNamesCapture(bc.scope, bc.request, bc.requestContext), test.namesCap)
			all := test.grantsTransfer && test.namesStep && test.namesCap
			pcR6CheckBool(t, "scopeMatches", backupCaptureScopeMatches(fixture, input, bc), all)
			pcR6CheckBool(t, "transferAuthorized", backupCaptureTransferAuthorized(fixture, input, bc), all)
		})
	}
}

func TestBackupCaptureScopeMatchesRejectsConsumedScope(t *testing.T) {
	fixture, input, bc := pcR6CaptureCase(t)
	bc.scope["consumed"] = true
	pcR6CheckBool(t, "scopeMatches", backupCaptureScopeMatches(fixture, input, bc), false)
	pcR6CheckBool(t, "transferAuthorized", backupCaptureTransferAuthorized(fixture, input, bc), false)
}

func TestBackupCaptureTransferAuthorizedRequiresTimelyTransfer(t *testing.T) {
	fixture, input, bc := pcR6CaptureCase(t)
	input["now"] = "2026-11-07T10:05:00Z"
	pcR6CheckBool(t, "scopeMatches", backupCaptureScopeMatches(fixture, input, bc), true)
	pcR6CheckBool(t, "transferAuthorized", backupCaptureTransferAuthorized(fixture, input, bc), false)
}

func TestBackupCaptureBindingAndPreparationFields(t *testing.T) {
	for _, field := range []string{"release_id", "backup_id", "data_generation_id", "preparation_id", "consistency_mode"} {
		t.Run(field, func(t *testing.T) {
			_, _, bc := pcR6CaptureCase(t)
			pcR6CheckBool(t, "bindingExact baseline", backupCaptureBindingExact(bc.request, bc.output), true)
			pcR6CheckBool(t, "preparationBound baseline", backupCapturePreparationBound(bc.request, bc.preparation), true)
			bc.output[field] = "other"
			bc.preparation[field] = "other"
			pcR6CheckBool(t, "bindingExact", backupCaptureBindingExact(bc.request, bc.output), false)
			pcR6CheckBool(t, "preparationBound", backupCapturePreparationBound(bc.request, bc.preparation), false)
		})
	}
	for _, field := range []string{"data_set_id", "capture_id"} {
		t.Run(field, func(t *testing.T) {
			_, _, bc := pcR6CaptureCase(t)
			bc.output[field] = "other"
			pcR6CheckBool(t, "bindingExact", backupCaptureBindingExact(bc.request, bc.output), false)
		})
	}
	t.Run("target", func(t *testing.T) {
		_, _, bc := pcR6CaptureCase(t)
		bc.output["target"] = map[string]any{}
		pcR6CheckBool(t, "bindingExact", backupCaptureBindingExact(bc.request, bc.output), false)
	})
	t.Run("data set not prepared", func(t *testing.T) {
		_, _, bc := pcR6CaptureCase(t)
		bc.preparation["data_set_ids"] = []any{"plugins"}
		pcR6CheckBool(t, "preparationBound", backupCapturePreparationBound(bc.request, bc.preparation), false)
	})
}
