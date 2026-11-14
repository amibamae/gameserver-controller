package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupResolveStepInputTrustedEvidence(t *testing.T) {
	for _, tc := range []struct {
		step         string
		wantResolved string
		wantError    string
	}{
		{backupDataStepID("capture", "world"), `"preparation_id"`, "capture input requires an exact completed backup preparation"},
		{backupDataStepID("upload", "world"), `"expected_checksum"`, "storage upload input requires a complete bounded capture"},
		{backupDataStepID("readback", "world"), `"object_ref"`, "storage readback input requires a completed capture"},
		{backupValidateStep, `"checksum_algorithm"`, "backup validation requires a completed, evidence-bearing preparation"},
	} {
		t.Run(tc.step, func(t *testing.T) {
			f := newBackupPreconditionFixture(t, true)
			w := backupRefactorPreconditionWorkflow(f, protocolstate.ServerBackingUp)
			step := f.operation.Steps[backupStepIndex(f.operation, tc.step)]
			operation := backupRefactorOperationThrough(t, f, tc.step)
			checkBackupResolvedInput(t, w, operation, step, tc.wantResolved)
			got, err := w.ResolveStepInput(context.Background(), f.operation, step)
			if got != nil || err == nil || err.Error() != tc.wantError {
				t.Fatalf("without evidence got %s, %v; want %q", got, err, tc.wantError)
			}
		})
	}
}

func checkBackupResolvedInput(t *testing.T, w *ServerBackupWorkflow, operation store.OperationRecord, step store.OperationStep, expected string) {
	t.Helper()
	got, err := w.ResolveStepInput(context.Background(), operation, step)
	if err != nil || !strings.Contains(string(got), expected) {
		t.Fatalf("resolved %s, %v; expected field %q", got, err, expected)
	}
}
