package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

func TestBootStatusWithoutReservations(t *testing.T) {
	var output bytes.Buffer
	err := showBootStatus("pilot-test", "staging", filepath.Join(t.TempDir(), "pilot.db"), time.Unix(1, 0), &output)
	if err != nil {
		t.Fatal(err)
	}
	const want = "No controller-owned VM is currently recorded for pilot-test@staging.\nHistorical boot estimate unavailable (0 completed boot sample(s); 3 required).\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestBootStatusRejectsInvalidInputsBeforeOpeningState(t *testing.T) {
	for _, test := range []struct {
		serverID, environmentID string
		now                     time.Time
	}{
		{"Invalid", "staging", time.Unix(1, 0)},
		{"pilot-test", "development", time.Unix(1, 0)},
		{"pilot-test", "staging", time.Time{}},
	} {
		var output bytes.Buffer
		err := showBootStatus(test.serverID, test.environmentID, "", test.now, &output)
		if err == nil || err.Error() != "a valid server ID, environment, and current time are required" {
			t.Errorf("error = %v for %+v", err, test)
		}
		if output.Len() != 0 {
			t.Errorf("unexpected output %q", output.String())
		}
	}
}
