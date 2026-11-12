package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// The vector covers bounded streams, release binding, reconciliation,
// watermark handling, and rejection of unsafe backup captures.
func TestBackupCaptureConformanceVectors(t *testing.T) {
	pkg, err := loadPackage("../..")
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}
	fixture, err := parseJSONFile(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "game-backup-capture-binding-and-stream-integrity.json"))
	if err != nil {
		t.Fatalf("load capture fixture: %v", err)
	}
	actual, err := evaluateBackupCapture(pkg, asObject(fixture))
	if err != nil {
		t.Fatalf("evaluate capture fixture: %v", err)
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("marshal capture result: %v", err)
	}
	normalized, err := parseJSON(encoded, "capture result")
	if err != nil {
		t.Fatalf("normalize capture result: %v", err)
	}
	if err := compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$"); err != nil {
		t.Fatalf("capture conformance: %v", err)
	}
}
