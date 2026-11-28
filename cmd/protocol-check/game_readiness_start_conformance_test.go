package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// readinessStartFixture loads the start-bound readiness fixture.
func readinessStartFixture(t *testing.T) (*protocolPackage, map[string]any) {
	t.Helper()
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := parseJSONFile(filepath.Join(root, "protocol", "conformance", "test-vectors", "game-readiness-start-bound-evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	return pkg, asObject(raw)
}

// TestReadinessStartBoundRejectsInvalidCases pins the evaluator errors for
// a case whose evidence does not match its schema or lacks a start time.
func TestReadinessStartBoundRejectsInvalidCases(t *testing.T) {
	cases := map[string]struct {
		change func(map[string]any)
		want   string
	}{
		"inspection schema": {func(c map[string]any) { asObject(c["unit_inspection"])["state"] = "bogus" },
			`case "fresh-same-runtime" unit inspection does not match schema`},
		"readiness schema": {func(c map[string]any) { asObject(c["readiness_output"])["runtime_id"] = "" },
			`case "fresh-same-runtime" readiness output does not match schema`},
		"dispatch time": {func(c map[string]any) { c["readiness_dispatched_at"] = "not-a-time" },
			`case "fresh-same-runtime" has no valid readiness_dispatched_at`},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			pkg, fixture := readinessStartFixture(t)
			first := asObject(arrayValue(fixture, "evaluations")[0])
			test.change(first)
			fixture["evaluations"] = []any{first}
			_, err := evaluateGameReadinessStartBound(pkg, fixture)
			if err == nil || !strings.HasPrefix(err.Error(), test.want) {
				t.Fatalf("error = %v; want prefix %q", err, test.want)
			}
		})
	}
}
