package workflow

import (
	"context"
	"strings"
	"testing"
)

func TestServerDestroyRejectsIncompleteFinalRecoveryPoint(t *testing.T) {
	cases := []struct {
		name   string
		change func(*VerifiedServerBackup)
	}{
		{"backup not off the VM", func(b *VerifiedServerBackup) { b.OffInstanceVerified = false }},
		{"capture not consistent", func(b *VerifiedServerBackup) { b.ConsistencyVerified = false }},
		{"writes after recovery point", func(b *VerifiedServerBackup) { b.FinalWriteBoundaryVerified = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newServerDestroyFixture(t)
			tc.change(&fixture.backups.backup)
			_, err := fixture.workflow.BuildPlan(context.Background(), fixture.operation.Record)
			if err == nil || !strings.Contains(err.Error(), "off-instance consistent backup covering the final write boundary") {
				t.Fatalf("destroy plan = %v, want recovery-point rejection", err)
			}
			if err := fixture.workflow.CheckPreconditions(context.Background(), fixture.operation.Record,
				fixture.operation.Record.Steps[0]); err == nil || !strings.Contains(err.Error(), "off-instance consistent backup covering the final write boundary") {
				t.Fatalf("pre-dispatch check = %v, want recovery-point rejection", err)
			}
			// A backup may become unavailable after the first read-only checks.
			// Rejection must be repeated immediately before provider destruction.
			late := destroyOperationAfter(fixture, 2)
			if err := fixture.workflow.CheckPreconditions(context.Background(), late,
				late.Steps[2]); err == nil || !strings.Contains(err.Error(), "off-instance consistent backup covering the final write boundary") {
				t.Fatalf("provider destroy precondition = %v, want backup rejection", err)
			}
		})
	}
}
