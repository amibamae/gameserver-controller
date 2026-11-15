package workflow

import (
	"encoding/json"
	"testing"
)

// These tests call hasValidRestoreResult directly on a synthetic operation
// result and pin the pass/fail outcome for every field check in the exact
// order the production code applies them.
//
// The valid-case fixture must satisfy the full server-restore-result schema
// (including the operation-result base fields) before it can be used to
// prove a true return. The invalid cases only need to break one specific
// check and are allowed to fail at an earlier step.

// restoreR3RestoreResultBase builds a schema-valid server.restore terminal
// result for the fixture's exact operation, server and request. It is valid
// for start_after_restore=false, where the final lifecycle state is stopped
// and no readiness assertion is required.
//
// For start_after_restore=true the caller must use a planned fixture, patch
// the final_lifecycle_state to "ready" and inject a readiness_assertion via
// restoreR3Patch.
func restoreR3RestoreResultBase(t *testing.T, fixture *restoreR3Fixture) json.RawMessage {
	t.Helper()
	now := restoreR3Now()
	backupID := string(fixture.backups.backup.Result)
	// Restore the backup_id from the resolved backup's operation data.
	var backupOp struct {
		OperationData struct {
			BackupID string `json:"backup_id"`
		} `json:"operation_data"`
	}
	if err := json.Unmarshal(fixture.backups.backup.Result, &backupOp); err != nil {
		t.Fatal(err)
	}
	backupID = backupOp.OperationData.BackupID

	result := map[string]any{
		"outcome_summary": "The selected verified backup was restored and validated on the exact managed instance.",
		"operation_type":  "server.restore",
		"affected_resources": []any{
			json.RawMessage(fixture.servers.record.Record.InstanceRef),
		},
		"completed_steps":       []any{},
		"failed_steps":          []any{},
		"warnings":              []string{},
		"unresolved_conditions": []any{},
		"audit_event_ids":       []string{},
		"partial":               false,
		"operation_data": map[string]any{
			"backup_id":             backupID,
			"target_instance_ref":   json.RawMessage(fixture.servers.record.Record.InstanceRef),
			"data_generation_id":    "restore-" + fixture.operation.OperationID,
			"restore_verified":      true,
			"final_lifecycle_state": "stopped",
		},
	}
	_ = now
	return restoreR3JSON(t, result)
}

func TestHasValidRestoreResult(t *testing.T) {
	schemaURI := "urn:gameserver-controller:0.21.0-draft:operations:server-restore-result"

	t.Run("valid without start after restore", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		fixture.operation.Result = result
		if !hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to return true for a schema-valid, fully bound result")
		}
		// sanity: the fixture's result must pass the schema itself
		if err := fixture.workflow.schemas.ValidateJSON(schemaURI, result); err != nil {
			t.Fatalf("fixture itself is not schema-valid: %v", err)
		}
	})

	t.Run("valid with start after restore", func(t *testing.T) {
		fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
		restoreR3CompleteAll(t, fixture)

		// Build a schema-valid readiness assertion using the workflow's own
		// builder against the fully-completed operation.
		assertion, err := BuildReadinessAssertion(fixture.workflow.schemas, fixture.operation, *fixture.server(), restoreReadinessStep, fixture.workflow.assertionTTL)
		if err != nil {
			t.Fatalf("could not build readiness assertion: %v", err)
		}

		result := restoreR3RestoreResultBase(t, fixture)
		// Patch final_lifecycle_state to "ready" and inject the assertion.
		patched := restoreR3Patch(t, result, "ready", "operation_data", "final_lifecycle_state")
		patched = restoreR3Patch(t, patched, json.RawMessage(assertion), "operation_data", "readiness_assertion")

		if err := fixture.workflow.schemas.ValidateJSON(schemaURI, patched); err != nil {
			t.Fatalf("patched result is not schema-valid: %v", err)
		}
		fixture.operation.Result = patched
		if !hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to return true for a start-after-restore result with a valid readiness assertion")
		}
	})

	t.Run("wrong operation type", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, "server.backup", "operation_type")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result whose operation_type is not server.restore")
		}
	})

	t.Run("wrong backup id", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, "backup-other", "operation_data", "backup_id")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result whose backup_id does not match the request")
		}
	})

	t.Run("wrong instance", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, json.RawMessage(restoreR3OtherInstance), "operation_data", "target_instance_ref")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result whose target instance differs from the managed instance")
		}
	})

	t.Run("wrong generation", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, "restore-wrong", "operation_data", "data_generation_id")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result whose data_generation_id does not match the operation")
		}
	})

	t.Run("not verified", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, false, "operation_data", "restore_verified")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result that is not restore_verified")
		}
	})

	t.Run("start after restore requires ready", func(t *testing.T) {
		fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
		restoreR3CompleteAll(t, fixture)

		// Build a valid readiness assertion so the schema check passes; the
		// test is about the final_lifecycle_state check, not the assertion content.
		assertion, err := BuildReadinessAssertion(fixture.workflow.schemas, fixture.operation, *fixture.server(), restoreReadinessStep, fixture.workflow.assertionTTL)
		if err != nil {
			t.Fatalf("could not build readiness assertion: %v", err)
		}

		// Keep final_lifecycle_state at "stopped" (not "ready") but inject a
		// valid assertion so the test isolates the lifecycle-state check.
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, json.RawMessage(assertion), "operation_data", "readiness_assertion")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a start-after-restore result whose final state is not ready")
		}
	})

	t.Run("start after restore requires readiness assertion", func(t *testing.T) {
		fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
		restoreR3CompleteAll(t, fixture)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, "ready", "operation_data", "final_lifecycle_state")
		// No readiness_assertion injected.
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a start-after-restore result that lacks a readiness assertion")
		}
	})

	t.Run("no start after restore must be stopped", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		result := restoreR3RestoreResultBase(t, fixture)
		result = restoreR3Patch(t, result, "ready", "operation_data", "final_lifecycle_state")
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a non-start-after-restore result whose final state is not stopped")
		}
	})
}

// TestHasValidRestoreResultMalformed pins the JSON-malformed and bad-request
// early failures.
func TestHasValidRestoreResultMalformed(t *testing.T) {
	t.Run("malformed json", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		fixture.operation.Result = json.RawMessage(`{"operation_type":`)
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a malformed result JSON")
		}
	})

	t.Run("malformed request snapshot", func(t *testing.T) {
		fixture := newRestoreR3Fixture(t, restoreR3Replace)
		fixture.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":`)
		result := restoreR3RestoreResultBase(t, fixture)
		fixture.operation.Result = result
		if hasValidRestoreResult(fixture.operation, fixture.servers.record.Record, fixture.workflow.schemas) {
			t.Fatal("expected hasValidRestoreResult to reject a result when the request snapshot cannot be parsed")
		}
	})
}
