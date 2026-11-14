package workflow

import (
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupResultBindsManifestAndReadiness(t *testing.T) {
	server := backupLifecycleServer(protocolstate.ServerReady)
	operation := store.OperationRecord{
		OperationID: "backup-1",
		Target:      store.OperationTarget{ServerID: server.ServerID, EnvironmentID: server.EnvironmentID},
	}
	schemas := acceptAllTestSchemas{version: "0.21.0-draft"}
	tests := []struct {
		name    string
		section string
		field   string
		value   any
		want    bool
	}{
		{name: "exact evidence", want: true},
		{name: "other operation type", field: "operation_type", value: "server.restore"},
		{name: "other backup", section: "data", field: "backup_id", value: "other"},
		{name: "other result environment", section: "data", field: "environment_id", value: "testing"},
		{name: "wrong final state", section: "data", field: "final_lifecycle_state", value: "stopped"},
		{name: "non-object manifest", section: "data", field: "manifest", value: []any{}},
		{name: "wrong version", section: "manifest", field: "manifest_version", value: "1"},
		{name: "wrong manifest backup", section: "manifest", field: "backup_id", value: "other"},
		{name: "wrong server", section: "manifest", field: "server_id", value: "other"},
		{name: "wrong environment", section: "manifest", field: "environment_id", value: "testing"},
		{name: "wrong generation", section: "manifest", field: "data_generation_id", value: "generation-other"},
		{name: "wrong source operation", section: "manifest", field: "source_operation_id", value: "other"},
		{name: "wrong source instance", section: "manifest", field: "source_instance_ref", value: map[string]any{"external_id": "other"}},
		{name: "readiness for another server", section: "readiness", field: "server_id", value: "other"},
		{name: "readiness for another environment", section: "readiness", field: "environment_id", value: "testing"},
		{name: "readiness for another instance", section: "readiness", field: "instance_ref", value: map[string]any{"external_id": "other"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal(backupReadyResult(operation, server), &payload); err != nil {
				t.Fatal(err)
			}
			data := payload["operation_data"].(map[string]any)
			sections := map[string]map[string]any{
				"": payload, "data": data, "manifest": data["manifest"].(map[string]any),
				"readiness": data["readiness_assertion"].(map[string]any),
			}
			if tt.field != "" {
				sections[tt.section][tt.field] = tt.value
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := hasValidBackupResult(raw, operation, server, schemas, protocolstate.ServerReady); got != tt.want {
				t.Fatalf("hasValidBackupResult = %v, want %v", got, tt.want)
			}
		})
	}
}
