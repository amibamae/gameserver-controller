package main

import (
	"path/filepath"
	"testing"
)

// TestEveryVectorHasExactlyOneEvaluator keeps the registry and the published
// vectors in step: no vector without an evaluator, and no evaluator left for
// a vector that was removed.
func TestEveryVectorHasExactlyOneEvaluator(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err != nil {
			t.Fatal(err)
		}
		id := stringValue(asObject(fixture), "fixture_id")
		if previous, duplicate := seen[id]; duplicate {
			t.Errorf("fixture_id %q is used by %s and %s", id, previous, filepath.Base(path))
		}
		seen[id] = filepath.Base(path)
		if _, ok := fixtureEvaluators[id]; !ok {
			t.Errorf("%s: no evaluator registered for %q", filepath.Base(path), id)
		}
	}
	for id := range fixtureEvaluators {
		if _, ok := seen[id]; !ok {
			t.Errorf("evaluator registered for %q, but no vector has that fixture_id", id)
		}
	}
}

func TestUnknownFixtureIsRejected(t *testing.T) {
	if _, err := evaluate(nil, map[string]any{"fixture_id": "no.such.fixture"}); err == nil || err.Error() != `no evaluator registered for fixture "no.such.fixture"` {
		t.Fatalf("unknown fixture error = %v", err)
	}
}
