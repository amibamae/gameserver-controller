package main

import (
	"path/filepath"
	"testing"
	"time"
)

// restartFixtureCase loads the restart conformance fixture and returns a fresh
// copy of the named case.
func restartFixtureCase(t *testing.T, caseID string) (*protocolPackage, map[string]any, map[string]any) {
	t.Helper()
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := parseJSONFile(filepath.Join(root, "protocol", "conformance", "test-vectors", "execution-unit-restart-clean-stop-distinct-runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := asObject(raw)
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		if c := asObject(rawCase); stringValue(c, "case_id") == caseID {
			return pkg, fixture, c
		}
	}
	t.Fatalf("restart fixture has no case %q", caseID)
	return nil, nil, nil
}

func shiftedTimestamp(t *testing.T, value string, offset time.Duration) string {
	t.Helper()
	parsed, err := parseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Add(offset).UTC().Format(time.RFC3339Nano)
}

func TestUnitRestartSaveStepEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, core, saveStep map[string]any, save map[string]any)
		flag   string
	}{
		{
			name: "save step completed after dispatch",
			mutate: func(t *testing.T, core, saveStep, _ map[string]any) {
				saveStep["completed_at"] = shiftedTimestamp(t, stringValue(core, "dispatch_at"), time.Second)
			},
			flag: "save_confirmation_bound",
		},
		{
			name: "save step completed before the save was observed",
			mutate: func(t *testing.T, _, saveStep, save map[string]any) {
				saveStep["completed_at"] = shiftedTimestamp(t, stringValue(save, "observed_at"), -time.Second)
			},
			flag: "save_confirmation_bound",
		},
		{
			name: "save step requested for another reason",
			mutate: func(_ *testing.T, _, saveStep, _ map[string]any) {
				asObject(saveStep["input"])["reason"] = "backup"
			},
			flag: "save_step_bound",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg, fixture, c := restartFixtureCase(t, "completed-distinct-runtime")
			if result := evaluateUnitRestartCase(pkg, fixture, c); result[test.flag] != true || result["restart_confirmed"] != true {
				t.Fatalf("baseline case: %s = %v, restart_confirmed = %v; want both true", test.flag, result[test.flag], result["restart_confirmed"])
			}
			core := asObject(c["core_preconditions"])
			test.mutate(t, core, asObject(core["completed_save_step"]), asObject(asObject(c["request"])["save_confirmation"]))
			result := evaluateUnitRestartCase(pkg, fixture, c)
			if result[test.flag] != false || result["dispatch_allowed"] != false || result["restart_confirmed"] != false {
				t.Fatalf("%s = %v, dispatch_allowed = %v, restart_confirmed = %v; want all false",
					test.flag, result[test.flag], result["dispatch_allowed"], result["restart_confirmed"])
			}
		})
	}
}

func TestUnitRestartReconciliationRequiresCleanStop(t *testing.T) {
	pkg, fixture, c := restartFixtureCase(t, "reconcile-completed-new-runtime")
	if result := evaluateUnitRestartCase(pkg, fixture, c); result["reconciliation_confirmed"] != true {
		t.Fatalf("baseline reconciliation_confirmed = %v, want true", result["reconciliation_confirmed"])
	}
	asObject(c["resolved_operation"])["clean_stop_confirmed"] = false
	result := evaluateUnitRestartCase(pkg, fixture, c)
	if result["reconciliation_confirmed"] != false || result["restart_confirmed"] != false || result["must_inspect_before_retry"] != true {
		t.Fatalf("reconciliation_confirmed = %v, restart_confirmed = %v, must_inspect_before_retry = %v; want false, false, true",
			result["reconciliation_confirmed"], result["restart_confirmed"], result["must_inspect_before_retry"])
	}
}
