package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The published fixture covers successful, rejected, ambiguous, and reconciled
// restarts. Keep its exact assertions as a regression oracle for refactoring.
func TestUnitRestartConformanceVectors(t *testing.T) {
	pkg, err := loadPackage("../..")
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}
	fixture, err := parseJSONFile(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "execution-unit-restart-clean-stop-distinct-runtime.json"))
	if err != nil {
		t.Fatalf("load restart fixture: %v", err)
	}
	actual, err := evaluateUnitRestart(pkg, asObject(fixture))
	if err != nil {
		t.Fatalf("evaluate restart fixture: %v", err)
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("marshal restart result: %v", err)
	}
	normalized, err := parseJSON(encoded, "restart result")
	if err != nil {
		t.Fatalf("normalize restart result: %v", err)
	}
	if err := compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$"); err != nil {
		t.Fatalf("restart conformance: %v", err)
	}
}
