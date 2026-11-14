package main

import (
	"path/filepath"
	"testing"
)

func TestCapacityEmptyReport(t *testing.T) {
	output, err := runPilotWithState(t, filepath.Join(t.TempDir(), "pilot.db"), "capacity")
	if err != nil {
		t.Fatal(err)
	}
	const want = "Managed VM capacity: 0/2 total.\nproduction: 0/1 slot(s)\nshared_nonproduction: 0/1 slot(s)\nNo managed VM reservations are recorded.\n"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}
