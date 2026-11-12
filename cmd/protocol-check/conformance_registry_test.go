package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEvaluatorsCoverExactlyThePublishedFixtures fails when a published fixture
// has no evaluator, or an evaluator is registered for a fixture ID that no
// published fixture uses (a typo or a stale registration).
func TestEvaluatorsCoverExactlyThePublishedFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, path := range paths {
		value, err := parseJSONFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fixtureID := stringValue(asObject(value), "fixture_id")
		if fixtureID == "" {
			t.Fatalf("%s has no fixture_id", filepath.Base(path))
		}
		published[fixtureID] = true
	}
	var unregistered, unpublished []string
	for fixtureID := range published {
		if evaluators[fixtureID] == nil {
			unregistered = append(unregistered, fixtureID)
		}
	}
	for fixtureID := range evaluators {
		if !published[fixtureID] {
			unpublished = append(unpublished, fixtureID)
		}
	}
	sort.Strings(unregistered)
	sort.Strings(unpublished)
	if len(unregistered) > 0 {
		t.Errorf("published fixtures without an evaluator (add a registerEvaluator call): %s", strings.Join(unregistered, ", "))
	}
	if len(unpublished) > 0 {
		t.Errorf("evaluators registered for unpublished fixture IDs: %s", strings.Join(unpublished, ", "))
	}
}

func TestRegisterEvaluatorRejectsEmptyAndRepeatedIDs(t *testing.T) {
	saved := evaluators
	t.Cleanup(func() { evaluators = saved })
	noop := func(*protocolPackage, map[string]any) (map[string]any, error) { return nil, nil }
	tests := []struct {
		name     string
		register func()
		want     string
	}{
		{"no fixture IDs", func() { registerEvaluator(noop) }, "at least one fixture ID"},
		{"nil evaluator", func() { registerEvaluator(nil, "a.fixture") }, "at least one fixture ID"},
		{"empty fixture ID", func() { registerEvaluator(noop, "") }, "empty fixture ID"},
		{"ID repeated in one call", func() { registerEvaluator(noop, "a.fixture", "a.fixture") }, `"a.fixture" is already registered`},
		{"ID registered twice", func() {
			registerEvaluator(noop, "a.fixture")
			registerEvaluator(noop, "a.fixture")
		}, `"a.fixture" is already registered`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluators = map[string]evaluator{}
			defer func() {
				recovered := recover()
				message, _ := recovered.(string)
				if !strings.Contains(message, test.want) {
					t.Fatalf("registerEvaluator panic = %v, want one containing %q", recovered, test.want)
				}
			}()
			test.register()
		})
	}
}

func TestEvaluateRoutesThroughRegistry(t *testing.T) {
	saved := evaluators
	t.Cleanup(func() { evaluators = saved })
	evaluators = map[string]evaluator{}
	var received map[string]any
	registerEvaluator(withoutPackage(func(fixture map[string]any) (map[string]any, error) {
		received = fixture
		return map[string]any{"routed": true}, nil
	}), "a.fixture", "b.fixture")

	fixture := map[string]any{"fixture_id": "b.fixture"}
	result, err := evaluate(nil, fixture)
	if err != nil || result["routed"] != true {
		t.Fatalf("evaluate = %v, %v; want the registered evaluator's result", result, err)
	}
	if received["fixture_id"] != "b.fixture" {
		t.Fatalf("withoutPackage passed %v, want the fixture", received)
	}
	if _, err := evaluate(nil, map[string]any{"fixture_id": "c.fixture"}); err == nil || err.Error() != `no evaluator registered for fixture "c.fixture"` {
		t.Fatalf("unregistered fixture error = %v", err)
	}
}
