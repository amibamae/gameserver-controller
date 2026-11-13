package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixtures creates a package root whose test-vector directory holds the
// given fixture files.
func writeFixtures(t *testing.T, files map[string]string) *protocolPackage {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "protocol", "conformance", "test-vectors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &protocolPackage{root: root}
}

// TestRunConformanceReportsEveryFailingFixture pins how failures are reported:
// one line per failing file, in file-name order, with each stage's error.
func TestRunConformanceReportsEveryFailingFixture(t *testing.T) {
	saved := evaluators
	t.Cleanup(func() { evaluators = saved })
	evaluators = map[string]evaluator{}
	registerEvaluator(withoutPackage(func(map[string]any) (map[string]any, error) {
		return map[string]any{"accepted": true, "extra": "ignored"}, nil
	}), "test.accepts")

	pkg := writeFixtures(t, map[string]string{
		"a-passes.json":       `{"fixture_id":"test.accepts","expected":{"accepted":true}}`,
		"b-mismatch.json":     `{"fixture_id":"test.accepts","expected":{"accepted":false}}`,
		"c-missing-key.json":  `{"fixture_id":"test.accepts","expected":{"reason":"x"}}`,
		"d-unregistered.json": `{"fixture_id":"test.unknown","expected":{}}`,
		"e-invalid.json":      `{"fixture_id":`,
	})
	err := pkg.runConformance()
	if err == nil {
		t.Fatal("runConformance passed with failing fixtures")
	}
	want := "conformance failed:\n" +
		" - b-mismatch.json: $.accepted: expected false, got true\n" +
		" - c-missing-key.json: $.reason: missing expected value\n" +
		` - d-unregistered.json: no evaluator registered for fixture "test.unknown"` + "\n" +
		" - e-invalid.json: "
	if got := err.Error(); len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("runConformance error =\n%s\nwant a report starting with\n%s", got, want)
	}
}

func TestRunConformancePassesWhenEveryFixtureMatches(t *testing.T) {
	saved := evaluators
	t.Cleanup(func() { evaluators = saved })
	evaluators = map[string]evaluator{}
	registerEvaluator(withoutPackage(func(map[string]any) (map[string]any, error) {
		return map[string]any{"count": 2}, nil
	}), "test.counts")
	// The result is compared after a JSON round trip, so an int result
	// matches the fixture's JSON number.
	pkg := writeFixtures(t, map[string]string{"counts.json": `{"fixture_id":"test.counts","expected":{"count":2}}`})
	if err := pkg.runConformance(); err != nil {
		t.Fatalf("runConformance: %v", err)
	}
}
