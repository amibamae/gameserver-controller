package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGlobConformanceVectorFiles returns all fixture files.
func TestGlobConformanceVectorFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "protocol", "conformance", "test-vectors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, dir, "a-first.json", `{"fixture_id":"x"}`)
	writeTestFile(t, dir, "z-last.json", `{"fixture_id":"y"}`)

	pkg := &protocolPackage{root: root}
	files, err := pkg.globConformanceVectorFiles()
	if err != nil {
		t.Fatal(err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	if files[0] != filepath.Join(dir, "a-first.json") {
		t.Errorf("first file = %q, want %q", files[0], filepath.Join(dir, "a-first.json"))
	}
	if files[1] != filepath.Join(dir, "z-last.json") {
		t.Errorf("second file = %q, want %q", files[1], filepath.Join(dir, "z-last.json"))
	}
}

// TestFilenameSorter sorts paths by basename.
func TestFilenameSorter(t *testing.T) {
	sorter := newFilenameSorter()
	paths := []string{
		filepath.Join("/", "dir", "z-second.json"),
		filepath.Join("/", "dir", "a-first.json"),
		filepath.Join("/", "dir", "m-middle.json"),
	}
	sorter.Sort(paths)

	expected := []string{
		filepath.Join("/", "dir", "a-first.json"),
		filepath.Join("/", "dir", "m-middle.json"),
		filepath.Join("/", "dir", "z-second.json"),
	}

	if !equalStringSlices(paths, expected) {
		t.Errorf("sorted paths = %v, want %v", paths, expected)
	}
}

// equalStringSlices compares two slices of strings.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeTestFile creates a test fixture file.
func writeTestFile(t *testing.T, dir string, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestParseJSONFile handles both valid and invalid JSON.
func TestParseJSONFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "protocol", "conformance", "test-vectors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Valid fixture
	validFixture := `{"fixture_id":"test.parse","expected":{"count":1}}`
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), []byte(validFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg := &protocolPackage{root: root}
	fixture, err := pkg.parseJSONFile(filepath.Join(dir, "valid.json"))
	if err != nil {
		t.Fatalf("parse valid fixture: %v", err)
	}

	if fixture["fixture_id"] != "test.parse" {
		t.Errorf("parsed fixture_id = %v, want test.parse", fixture["fixture_id"])
	}

	// Invalid JSON should fail
	if err := os.WriteFile(filepath.Join(dir, "invalid.json"), []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = pkg.parseJSONFile(filepath.Join(dir, "invalid.json"))
	if err == nil {
		t.Error("expected error parsing invalid JSON")
	}
}

// TestParseJSON handles various input formats.
func TestParseJSON(t *testing.T) {
	// Valid empty object
	obj, err := parseJSON([]byte("{}"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(obj) != 0 {
		t.Errorf("empty object length = %d, want 0", len(obj))
	}

	// Valid with content
	obj2, err := parseJSON([]byte(`{"key":"value"}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if val, ok := obj2["key"].(string); !ok || val != "value" {
		t.Errorf("parsed key = %v, want value", obj2["key"])
	}

	// Invalid JSON
	_, err = parseJSON([]byte(`{invalid}`), "test")
	if err == nil {
		t.Error("expected error on invalid JSON")
	}
}

// TestRunEvaluator looks up evaluator by fixture ID.
func TestRunEvaluator(t *testing.T) {
	saved := evaluators
	evaluators = map[string]evaluator{}
	defer func() { evaluators = saved }()

	// Register a test evaluator
	expectedResult := map[string]any{"status": "ok"}
	registerEvaluator(withoutPackage(func(_ map[string]any) (map[string]any, error) {
		return expectedResult, nil
	}), "test.run-evaluator")

	pkg := &protocolPackage{root: ".", version: "v1", entries: nil, schemaDocs: nil, rawSchemas: nil}
	fixture := map[string]any{"fixture_id": "test.run-evaluator"}

	result, err := pkg.runEvaluator(asObject(fixture))
	if err != nil {
		t.Fatalf("runEvaluator error = %v, want nil", err)
	}

	if !equalMaps(result, expectedResult) {
		t.Errorf("result = %v, want %v", result, expectedResult)
	}

	// Unregistered fixture should fail
	fixture["fixture_id"] = "unregistered"
	result2, err := pkg.runEvaluator(asObject(fixture))
	if err == nil || result2 != nil {
		t.Error("expected error for unregistered fixture")
	}
}

// equalMaps compares two maps for equality.
func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		bVal, ok := b[k]
		if !ok {
			return false
		}
		if reflect.DeepEqual(v, bVal) == false {
			return false
		}
	}
	return true
}

// TestCompareResult compares result and expected values at various levels.
func TestCompareResult(t *testing.T) {
	// Match: identical nested objects
	match, _ := compareActualWithExpected(
		map[string]any{"key": "value", "num": 1},
		map[string]any{"key": "value", "num": 1},
		"$")
	if match != "" {
		t.Errorf("identical objects: error = %q, want empty", match)
	}

	// Mismatch: top-level value differs
	mismatch, _ := compareActualWithExpected(
		map[string]any{"key": "wrong"},
		map[string]any{"key": "value"},
		"$")
	if mismatch != `$.key: expected map[key:value], got map[key:wrong]` {
		t.Errorf("mismatch error = %q", mismatch)
	}

	// Mismatch: nested missing key
	nestedMissing, _ := compareActualWithExpected(
		map[string]any{},
		map[string]any{"nested": "exists"},
		"$")
	if nestedMissing != `$.nested: missing expected value` {
		t.Errorf("nested missing error = %q, want \"$.nested: missing expected value\"", nestedMissing)
	}

	// Primitive match (integers via JSON Number roundtrip)
	primitiveMatch, _ := compareActualWithExpected(
		map[string]any{"count": 5},
		map[string]any{"count": 5},
		"$")
	if primitiveMatch != "" {
		t.Errorf("primitive match error = %q, want empty", primitiveMatch)
	}

	// Deep equality for null case
	nullCase, _ := compareActualWithExpected(
		nil,
		nil,
		"$")
	if nullCase != "" {
		t.Errorf("null comparison: error = %q, want empty", nullCase)
	}
}

// TestParseJSONFileHandlesReadError verifies the read path returns a clear error.
func TestParseJSONFileHandlesReadError(t *testing.T) {
	pkg := &protocolPackage{root: "/nonexistent"}
	_, err := pkg.parseJSONFile(filepath.Join(pkg.root, "file.json"))
	if err == nil || !contains(err.Error(), "nonexistent") {
		t.Errorf("parseJSONFile with nonexistent path error = %v, want read error", err)
	}
}

// TestEvaluateFixtureEndToEnd runs the full fixture evaluation pipeline.
func TestEvaluateFixtureEndToEnd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "protocol", "conformance", "test-vectors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a passing fixture
	passFixture := `{"fixture_id":"test.evaluate-pass","expected":{"status":"ok"}}`
	if err := os.WriteFile(filepath.Join(dir, "pass.json"), []byte(passFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	pkg := &protocolPackage{root: root}
	failed, err := pkg.evaluateFixture(filepath.Join(dir, "pass.json"))
	if failed || err != nil {
		t.Errorf("evaluateFixture on passing fixture: failed=%v, error=%v", failed, err)
	}

	// Create a failing fixture (mismatched expected value)
	failFixture := `{"fixture_id":"test.evaluate-fail","expected":{"status":"fail"}}`
	if err := os.WriteFile(filepath.Join(dir, "fail.json"), []byte(failFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	failed2, _ := pkg.evaluateFixture(filepath.Join(dir, "fail.json"))
	if !failed2 {
		t.Error("expected evaluateFixture to return failed=true for mismatch")
	}
}
