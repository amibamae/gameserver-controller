package main

import (
	"strings"
	"testing"
)

// Exercising every published vector protects the full dispatcher and each
// evaluator's expected result while the case groups are reorganized.
func TestConformanceDispatchPreservesAllVectors(t *testing.T) {
	pkg, err := loadPackage("../..")
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}
	if err := pkg.runConformance(); err != nil {
		t.Fatalf("published conformance vectors: %v", err)
	}
}

func TestConformanceDispatchRejectsUnknownFixtures(t *testing.T) {
	for _, id := range []string{"unregistered", "hosting.not-registered", "game.not-registered", "velocity-unknown"} {
		_, err := evaluate(nil, map[string]any{"fixture_id": id})
		if err == nil || !strings.Contains(err.Error(), "no evaluator registered for fixture") {
			t.Errorf("fixture %q: expected unregistered evaluator error, got %v", id, err)
		}
	}
}
