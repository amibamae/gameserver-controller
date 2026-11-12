package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// All published create cases must retain their exact dispatch and retry decisions.
func TestUnitCreateConformanceVectors(t *testing.T) {
	pkg, err := loadPackage("../..")
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}
	fixture, err := parseJSONFile(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "execution-unit-create-exact-definition-no-autostart.json"))
	if err != nil {
		t.Fatalf("load create fixture: %v", err)
	}
	actual, err := evaluateUnitCreate(pkg, asObject(fixture))
	if err != nil {
		t.Fatalf("evaluate create fixture: %v", err)
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatalf("marshal create result: %v", err)
	}
	normalized, err := parseJSON(encoded, "create result")
	if err != nil {
		t.Fatalf("normalize create result: %v", err)
	}
	if err := compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$"); err != nil {
		t.Fatalf("create conformance: %v", err)
	}
}
