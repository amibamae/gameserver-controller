package main

import (
	"path/filepath"
	"testing"
)

func TestExecutionHealthDiagnosticProbes(t *testing.T) {
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(root, "protocol", "conformance", "test-vectors", "execution-unit-health-diagnostic-redaction-and-binding.json")
	for _, tc := range []struct {
		name   string
		probes []any
		want   bool
	}{
		{name: "empty probe list"},
		{name: "empty probe", probes: []any{""}},
		{name: "non-string probe", probes: []any{42}},
		{name: "probe spans diagnostic lines", probes: []any{"first\nsecond"}},
		{name: "all probes absent", probes: []any{"third", "fourth"}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := parseJSONFile(fixturePath)
			if err != nil {
				t.Fatal(err)
			}
			fixture := asObject(raw)
			input := asObject(fixture["input"])
			evaluation := asObject(arrayValue(input, "evaluations")[0])
			asObject(evaluation["response"])["safe_diagnostics"] = []any{"first", "second"}
			evaluation["redaction_probes"] = tc.probes
			input["evaluations"] = []any{evaluation}
			got, err := evaluateExecutionHealthDiagnostics(pkg, fixture)
			if err != nil {
				t.Fatal(err)
			}
			result := asObject(arrayValue(got, "evaluations")[0])
			if !boolValue(result, "schemas_valid") || !boolValue(result, "unit_bound") {
				t.Fatalf("expected valid, bound diagnostics: %#v", result)
			}
			for _, field := range []string{"redaction_probes_absent", "response_accepted", "healthy_confirmed"} {
				if result[field] != tc.want {
					t.Errorf("%s = %v, want %t", field, result[field], tc.want)
				}
			}
		})
	}
}
