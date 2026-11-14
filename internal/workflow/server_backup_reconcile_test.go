package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupReconciliationRetryBoundaries(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		stepID string
		want   string
	}{
		{"cancelled inspection needs operator", cancelled, backupInspectRuntimeStep, "needs_operator"},
		{"readiness can retry", context.Background(), backupReadinessStep, "safe_to_retry"},
		{"planned readback can retry", context.Background(), backupDataStepID("readback", "world"), "safe_to_retry"},
		{"unplanned readback needs operator", context.Background(), "readback-other", "needs_operator"},
		{"save needs operator", context.Background(), backupSaveGameStep, "needs_operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation := store.OperationRecord{Steps: []store.OperationStep{
				{StepID: tc.stepID},
				{StepID: backupPrepareStep, Input: json.RawMessage(`{"data_set_ids":["world"]}`)},
			}}
			got, err := (&ServerBackupWorkflow{}).Reconcile(tc.ctx, operation, store.OperationStep{StepID: tc.stepID})
			if err != nil || string(got.State) != tc.want {
				t.Fatalf("Reconcile = %+v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestBackupObjectEvidenceBindsPlannedDestination(t *testing.T) {
	target := json.RawMessage(`{"adapter_id":"storage","storage_profile_id":"backups","environment_id":"test","container":"worlds","key":"planned"}`)
	for _, tc := range []struct {
		name, ref string
		want      bool
	}{
		{"exact destination with result metadata", `{"adapter_id":"storage","storage_profile_id":"backups","environment_id":"test","container":"worlds","key":"planned","version":"1"}`, true},
		{"other object", `{"adapter_id":"storage","storage_profile_id":"backups","environment_id":"test","container":"worlds","key":"other"}`, false},
		{"missing destination fields", `{"key":"planned"}`, false},
		{"not an object", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backupObjectRefMatchesTarget(json.RawMessage(tc.ref), target); got != tc.want {
				t.Fatalf("backupObjectRefMatchesTarget = %v, want %v", got, tc.want)
			}
		})
	}
}
