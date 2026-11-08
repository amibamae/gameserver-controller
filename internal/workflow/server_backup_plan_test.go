package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestServerBackupPlanSelectsExactTerminalInspectionOrResumeReadiness(t *testing.T) {
	for _, test := range []struct {
		name    string
		resume  bool
		present []string
		omitted []string
	}{
		{name: "no resume", resume: false, present: []string{backupVerifyStoppedStep}, omitted: []string{backupStartRuntimeStep, backupReadinessStep}},
		{name: "resume", resume: true, present: []string{backupStartRuntimeStep, backupVerifyRuntimeStep, backupReadinessStep}, omitted: []string{backupVerifyStoppedStep}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workflow, operation := newBackupPlanFixture(t, test.resume)
			steps, err := workflow.BuildPlan(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			positions := make(map[string]int, len(steps))
			for i, step := range steps {
				positions[step.StepID] = i
			}
			for _, stepID := range test.present {
				if _, exists := positions[stepID]; !exists {
					t.Errorf("plan omitted required terminal evidence step %q: %#v", stepID, steps)
				}
			}
			for _, stepID := range test.omitted {
				if _, exists := positions[stepID]; exists {
					t.Errorf("plan included incompatible terminal evidence step %q: %#v", stepID, steps)
				}
			}
			if !test.resume && positions[backupVerifyStoppedStep] != len(steps)-1 {
				t.Fatalf("no-resume final exact inspection is not the last step: %#v", steps)
			}
			if test.resume && !(positions[backupStartRuntimeStep] < positions[backupVerifyRuntimeStep] &&
				positions[backupVerifyRuntimeStep] < positions[backupReadinessStep]) {
				t.Fatalf("resume plan does not require start, exact-unit inspect, and readiness in order: %#v", steps)
			}
		})
	}
}

func TestDegradedBackupResumesOnlyOnExplicitRequestAndRequiresFreshReadiness(t *testing.T) {
	for _, test := range []struct {
		name       string
		parameters string
		wantResume bool
	}{
		{name: "explicit resume", parameters: `{"backup_profile":"profile-1","resume_after_backup":true}`, wantResume: true},
		{name: "no explicit resume", parameters: `{"backup_profile":"profile-1"}`, wantResume: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			workflow, operation := newBackupPlanFixture(t, false)
			repository := workflow.servers.(testBackupServerRepository)
			repository.record.Record.LifecycleState = protocolstate.ServerDegraded
			workflow.servers = repository
			operation.RequestSnapshot.Parameters = json.RawMessage(test.parameters)
			steps, err := workflow.BuildPlan(context.Background(), operation)
			if err != nil {
				t.Fatal(err)
			}
			present := map[string]bool{}
			for _, step := range steps {
				present[step.StepID] = true
			}
			if !present[backupSaveGameStep] || !present[backupStopRuntimeStep] {
				t.Fatalf("degraded running server was not saved and stopped before backup: %#v", steps)
			}
			if present[backupStartRuntimeStep] != test.wantResume || present[backupReadinessStep] != test.wantResume || present[backupVerifyStoppedStep] == test.wantResume {
				t.Fatalf("degraded resume plan does not match explicit choice: %#v", steps)
			}
		})
	}
}

func newBackupPlanFixture(t *testing.T, resume bool) (*ServerBackupWorkflow, store.OperationRecord) {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	server := backupLifecycleServer("ready")
	server.DisplayName = "Pilot"
	server.StorageProfile = "storage-profile"
	server.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)
	unit := json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"unit-1"}`)
	checks := []ReadinessRequirement{{CheckID: "paper.started", CheckVersion: "1"}}
	profile := ServerBackupProfile{
		ProfileID: "profile-1", GameReleaseID: "paper-release", GameName: "Paper", GameVersion: "1.21",
		ConsistencyMode:  "offline",
		DataSets:         []ServerBackupDataSet{{DataSetID: "world", RelativePaths: []string{"world"}, Required: true, SupportedConsistencyModes: []string{"offline"}, MaximumBytes: 64}},
		ValidationChecks: []ReadinessRequirement{{CheckID: "paper.backup.archive", CheckVersion: "1"}},
		StorageAdapterID: "storage.fake", StorageProfileID: server.StorageProfile, StorageContainer: "backups",
		UnitRef: unit, ReadinessCheckSetID: "paper:paper-release:checks-v1", RequiredReadinessChecks: checks,
	}
	dataDeclaration := map[string]any{"persistent_data_sets": []any{map[string]any{
		"game_release_id": profile.GameReleaseID, "data_set_ids": []string{"world"}, "supported_consistency_modes": []string{"offline"},
	}}}
	readinessDeclaration := map[string]any{"readiness_check_sets": []any{map[string]any{
		"game_release_id": profile.GameReleaseID, "environment_ids": []string{server.EnvironmentID},
		"readiness_check_set_id": profile.ReadinessCheckSetID,
		"required_checks":        []any{map[string]any{"check_id": "paper.started", "check_version": "1"}},
	}}}
	descriptors := map[string]adapter.Descriptor{
		"hosting.fake": backupDescriptor(schemas.ProtocolVersion(), adapter.CategoryHosting, []adapter.Capability{
			backupCapability(schemas.ProtocolVersion(), "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false, nil),
		}),
		"execution.fake": backupDescriptor(schemas.ProtocolVersion(), adapter.CategoryExecution, []adapter.Capability{
			backupCapability(schemas.ProtocolVersion(), "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false, nil),
			backupCapability(schemas.ProtocolVersion(), "execution.unit.graceful_stop", "execution:systemd-unit-graceful-stop-input", "execution:systemd-unit-graceful-stop-output", true, nil),
			backupCapability(schemas.ProtocolVersion(), "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true, nil),
		}),
		"game.fake": backupDescriptor(schemas.ProtocolVersion(), adapter.CategoryGame, []adapter.Capability{
			backupCapability(schemas.ProtocolVersion(), "game.data.describe", "game:data-describe-input", "game:data-describe-output", false, dataDeclaration),
			backupCapability(schemas.ProtocolVersion(), "game.save.request", "game:save-request-input", "game:save-request-output", true, nil),
			backupCapability(schemas.ProtocolVersion(), "game.backup.prepare", "game:backup-prepare-input", "game:backup-prepare-output", true, nil),
			backupCapability(schemas.ProtocolVersion(), "game.backup.capture", "game:backup-capture-input", "game:backup-capture-output", true, nil),
			backupCapability(schemas.ProtocolVersion(), "game.backup.validate", "game:backup-validate-input", "game:backup-validate-output", false, nil),
			backupCapability(schemas.ProtocolVersion(), "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false, readinessDeclaration),
		}),
		"storage.fake": backupDescriptor(schemas.ProtocolVersion(), adapter.CategoryStorage, []adapter.Capability{
			backupCapability(schemas.ProtocolVersion(), "storage.object.put", "storage:object-put-input", "storage:object-put-output", true, nil),
			backupCapability(schemas.ProtocolVersion(), "storage.object.get", "storage:object-get-input", "storage:object-get-output", false, nil),
		}),
	}
	serverRecord := store.StoredLogicalServer{Record: server}
	resolver := staticServerBackupProfileResolver{profile: profile}
	workflow, err := NewServerBackupWorkflow(
		testBackupServerRepository{record: serverRecord}, backupDescriptorRuntime{version: schemas.ProtocolVersion(), descriptors: descriptors},
		schemas, resolver, noopServerBackupTransferBroker{}, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := json.Marshal(map[string]any{"backup_profile": profile.ProfileID, "resume_after_backup": resume})
	if err != nil {
		t.Fatal(err)
	}
	operation := store.OperationRecord{
		OperationID: fmt.Sprintf("backup-plan-%t", resume), OperationType: "server.backup",
		ProtocolVersion: schemas.ProtocolVersion(), Target: store.OperationTarget{
			ServerID: server.ServerID, EnvironmentID: server.EnvironmentID, InstanceRef: server.InstanceRef,
		}, CreatedAt: time.Date(2026, time.November, 7, 9, 0, 0, 0, time.UTC),
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.backup", Parameters: parameters},
	}
	return workflow, operation
}

func backupDescriptor(version string, category adapter.Category, capabilities []adapter.Capability) adapter.Descriptor {
	return adapter.Descriptor{
		AdapterID: map[adapter.Category]string{
			adapter.CategoryHosting: "hosting.fake", adapter.CategoryExecution: "execution.fake",
			adapter.CategoryGame: "game.fake", adapter.CategoryStorage: "storage.fake",
		}[category],
		Category: category, DisplayName: "backup plan test adapter", ImplementationVersion: "test",
		ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{}, Capabilities: capabilities,
	}
}

func backupCapability(version, name, inputSchema, outputSchema string, mutating bool, constraints map[string]any) adapter.Capability {
	return adapter.Capability{
		Name: name, Version: "1", Mutating: mutating,
		InputSchemaURI:      "urn:gameserver-controller:" + version + ":" + inputSchema,
		OutputSchemaURI:     "urn:gameserver-controller:" + version + ":" + outputSchema,
		RequiredPermissions: []string{}, Constraints: constraints,
	}
}

type backupDescriptorRuntime struct {
	version     string
	descriptors map[string]adapter.Descriptor
}

func (runtime backupDescriptorRuntime) ProtocolVersion() string { return runtime.version }

func (runtime backupDescriptorRuntime) Descriptor(adapterID string) (adapter.Descriptor, error) {
	descriptor, ok := runtime.descriptors[adapterID]
	if !ok {
		return adapter.Descriptor{}, adapter.ErrAdapterNotFound
	}
	return descriptor, nil
}

func (backupDescriptorRuntime) Invoke(context.Context, string, adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return adapter.InvokeResponse{}, fmt.Errorf("plan fixture does not dispatch adapter calls")
}

type noopServerBackupTransferBroker struct{}

func (noopServerBackupTransferBroker) IssueStepStreamTransfers(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation, string) ([]adapter.StreamTransfer, error) {
	return nil, nil
}

func (noopServerBackupTransferBroker) VerifyStepStreams(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation) error {
	return nil
}
