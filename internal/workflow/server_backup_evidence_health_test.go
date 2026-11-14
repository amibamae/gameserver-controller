package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupCaptureEvidenceErrorPrecedence(t *testing.T) {
	base := backupCaptureObservation{
		Target: json.RawMessage(`{"server_id":"server"}`), ReleaseID: "release",
		BackupID: "backup", DataGenerationID: "generation", PreparationID: "prepare",
		DataSetID: "world", CaptureID: "capture", ConsistencyMode: "offline",
		Outcome: "captured", StreamCompleted: true, SizeBytes: 10,
		ObservedAt: "2026-01-01T00:00:00Z",
	}
	base.Checksum.Algorithm, base.Checksum.Digest = "sha256", strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		mutate func(*backupCaptureObservation)
		want   string
	}{
		{"valid capture", func(*backupCaptureObservation) {}, ""},
		{"wrong generation before invalid checksum", func(o *backupCaptureObservation) {
			o.DataGenerationID, o.Checksum.Digest = "other", strings.Repeat("g", 64)
		}, "capture output does not bind the exact prepared data set"},
		{"nonhex checksum before invalid timestamp", func(o *backupCaptureObservation) {
			o.Checksum.Digest, o.ObservedAt = strings.Repeat("g", 64), "unknown"
		}, "capture output checksum is not hexadecimal SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := base
			tc.mutate(&output)
			op := store.OperationRecord{Steps: []store.OperationStep{{
				StepID: backupDataStepID("capture", "world"), Status: protocolstate.StepSucceeded,
				Input: mustJSON(base), Output: mustJSON(output),
			}}}
			got, err := backupCaptureOutput(op, "world")
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Fatalf("capture error = %v, want %q", err, tc.want)
				}
				return
			}
			if err != nil || got.SizeBytes != base.SizeBytes || got.CaptureID != base.CaptureID {
				t.Fatalf("capture output = %+v, %v", got, err)
			}
		})
	}
}
