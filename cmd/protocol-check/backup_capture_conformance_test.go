package main

import (
	"path/filepath"
	"testing"
)

// backupCaptureFixtureCase loads the backup-capture conformance fixture and
// returns it, its input, and a fresh copy of the named evaluation.
func backupCaptureFixtureCase(t *testing.T, caseID string) (*protocolPackage, map[string]any, map[string]any, map[string]any) {
	t.Helper()
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := parseJSONFile(filepath.Join(root, "protocol", "conformance", "test-vectors", "game-backup-capture-binding-and-stream-integrity.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := asObject(raw)
	input := asObject(fixture["input"])
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		if evaluation := asObject(rawEvaluation); stringValue(evaluation, "case_id") == caseID {
			return pkg, fixture, input, evaluation
		}
	}
	t.Fatalf("backup-capture fixture has no case %q", caseID)
	return nil, nil, nil, nil
}

// TestBackupCaptureChecksNotCoveredByFixture covers conditions that no case in
// the conformance fixture isolates. Each subtest starts from the valid offline
// capture, changes one field, and expects one flag to turn false.
func TestBackupCaptureChecksNotCoveredByFixture(t *testing.T) {
	differentPreparation := func(field string) func(map[string]any, map[string]any) {
		return func(input, evaluation map[string]any) {
			value := stringValue(asObject(input["base_preparation_result"]), field)
			evaluation["preparation_result_overrides"] = map[string]any{field: value + "-other"}
		}
	}
	tests := []struct {
		name   string
		mutate func(input, evaluation map[string]any)
		flag   string
	}{
		{"preparation for another release", differentPreparation("release_id"), "preparation_bound"},
		{"preparation for another backup", differentPreparation("backup_id"), "preparation_bound"},
		{"preparation for another data generation", differentPreparation("data_generation_id"), "preparation_bound"},
		{"preparation in another consistency mode", func(_, evaluation map[string]any) {
			evaluation["preparation_result_overrides"] = map[string]any{"consistency_mode": "game_native"}
		}, "preparation_bound"},
		{"invocation context for another server", func(input, evaluation map[string]any) {
			value := stringValue(asObject(input["request_context"]), "server_id")
			evaluation["request_context_overrides"] = map[string]any{"server_id": value + "-other"}
		}, "request_target_bound"},
		{"runtime resumed after an offline capture", func(_, evaluation map[string]any) {
			evaluation["runtime_resumed"] = true
		}, "destruction_current_generation_proven"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg, fixture, input, evaluation := backupCaptureFixtureCase(t, "valid-offline-capture")
			if result := evaluateBackupCaptureCase(pkg, fixture, input, evaluation); result[test.flag] != true {
				t.Fatalf("baseline %s = %v, want true", test.flag, result[test.flag])
			}
			test.mutate(input, evaluation)
			if result := evaluateBackupCaptureCase(pkg, fixture, input, evaluation); result[test.flag] != false {
				t.Fatalf("%s = %v, want false", test.flag, result[test.flag])
			}
		})
	}
}

func TestBackupCaptureRejectsEvaluationWithoutCaseID(t *testing.T) {
	pkg, fixture, input, evaluation := backupCaptureFixtureCase(t, "valid-offline-capture")
	delete(evaluation, "case_id")
	input["evaluations"] = []any{evaluation}
	if _, err := evaluateBackupCapture(pkg, fixture); err == nil {
		t.Fatal("evaluateBackupCapture accepted an evaluation without case_id")
	}
}
