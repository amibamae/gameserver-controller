package main

import (
	"path/filepath"
	"testing"
)

// TestConformanceFixturesPass routes every published fixture through evaluate
// and checks its expected results, so a routing or evaluator regression fails
// go test as well as the protocol-check command.
func TestConformanceFixturesPass(t *testing.T) {
	pkg, err := loadPackage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := pkg.runConformance(); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateRejectsUnregisteredFixture(t *testing.T) {
	_, err := evaluate(nil, map[string]any{"fixture_id": "unregistered.fixture"})
	if err == nil || err.Error() != `no evaluator registered for fixture "unregistered.fixture"` {
		t.Fatalf("evaluate error = %v, want the unregistered-fixture error", err)
	}
}
