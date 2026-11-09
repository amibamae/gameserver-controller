package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type staticServerRestoreProfileResolver struct{ profile ServerRestoreProfile }

func (r staticServerRestoreProfileResolver) ResolveServerRestore(context.Context, store.LogicalServer) (ServerRestoreProfile, error) {
	return r.profile, nil
}

type staticServerRestoreBackupResolver struct {
	result json.RawMessage
	object json.RawMessage
	data   string
}

func (r staticServerRestoreBackupResolver) ResolveServerRestoreBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error) {
	return VerifiedProvisionBackup{Result: append(json.RawMessage(nil), r.result...), IntegrityVerified: true}, nil
}

func (r staticServerRestoreBackupResolver) OpenServerRestoreBackupObject(_ context.Context, _ store.LogicalServer, _ string, object json.RawMessage) (io.ReadCloser, error) {
	if !equalJSON(object, r.object) {
		return nil, fmt.Errorf("unexpected backup object reference")
	}
	return io.NopCloser(strings.NewReader(r.data)), nil
}

type countingServerRestoreAuthorizer struct{ calls int }

func (a *countingServerRestoreAuthorizer) AuthorizeServerRestore(context.Context, string, string, store.OperationRecord, store.LogicalServer, string) error {
	a.calls++
	return nil
}

type restoreStreamConsumingFake struct{ scripted *fakeadapter.Scripted }

func (f *restoreStreamConsumingFake) Descriptor() adapter.Descriptor { return f.scripted.Descriptor() }

func (f *restoreStreamConsumingFake) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if request.Params.Capability == "game.restore" {
		for _, reference := range request.Params.RequestContext.StreamTransfers {
			stream, err := request.Params.RequestContext.OpenStream(reference.TransferID)
			if err != nil {
				return adapter.InvokeResponse{}, err
			}
			_, readErr := io.Copy(io.Discard, stream)
			closeErr := stream.Close()
			if readErr != nil {
				return adapter.InvokeResponse{}, readErr
			}
			if closeErr != nil {
				return adapter.InvokeResponse{}, closeErr
			}
		}
	}
	return f.scripted.Invoke(ctx, request)
}

func TestRestoreCompatibilityRequiresExactManifestAndRelease(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile := ServerRestoreProfile{GameAdapterID: "game.paper", GameReleaseID: "paper-1", GameVersion: "1.21.4", RequiredCompatibilityCheckIDs: []string{"world.check"}}
	manifest := json.RawMessage(`{"manifest_version":"1","backup_id":"backup-1","server_id":"server-1","environment_id":"staging","data_generation_id":"generation-source","created_at":"2026-11-08T10:00:00Z","source_operation_id":"backup-op","game":{"adapter_id":"game.paper","name":"Paper","version":"1.21.4"},"consistency":{"mode":"offline","evidence":"stopped"},"objects":[{"object_ref":{"adapter_id":"storage.fake","storage_profile_id":"storage-1","environment_id":"staging","container":"backups","key":"server-1/world.tar","version_id":"version-1"},"size_bytes":1,"checksum_algorithm":"sha256","checksum":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)
	release := json.RawMessage(`{"game_adapter_id":"game.paper","game_name":"Paper","release_id":"paper-1","game_version":"1.21.4","targets":[{"os_family":"linux","architecture":"x86_64"}],"artifacts":[{"artifact_id":"server-jar","artifact_uri":"https://example.invalid/paper.jar","checksum":{"algorithm":"sha256","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],"runtime_requirements":[{"runtime_id":"java","version_requirement":"21"}],"configuration_schema_uri":"https://example.invalid/config.schema.json","configuration_schema_checksum":{"algorithm":"sha256","digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"data_format":{"format_id":"paper-world","version":"1"},"required_execution_capabilities":["systemd"]}`)
	input, _ := json.Marshal(map[string]any{"source_manifest": manifest, "target_release": release, "required_check_ids": profile.RequiredCompatibilityCheckIDs})
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	output := json.RawMessage(strings.ReplaceAll(`{"backup_id":"backup-1","environment_id":"staging","data_generation_id":"generation-source","source_game_adapter_id":"game.paper","source_game_version":"1.21.4","target_game_adapter_id":"game.paper","target_release_id":"paper-1","target_game_version":"1.21.4","compatibility_status":"compatible","observed_at":"2026-11-08T10:00:00Z","check_results":[{"check_id":"world.check","check_version":"1","result":"compatible","observed_at":"2026-11-08T10:00:00Z"}]}`, "2026-11-08T10:00:00Z", stamp))
	started := time.Now().UTC()
	operation := store.OperationRecord{OperationID: "restore-op", Steps: []store.OperationStep{{StepID: restoreCompatibilityStep, Status: protocolstate.StepSucceeded, Input: input, Output: output, StartedAt: &started}}}
	if !restoreCompatible(operation, schemas, profile, "backup-1", "staging") {
		t.Fatal("matching compatibility evidence was rejected")
	}
	var changed map[string]any
	if err := json.Unmarshal(output, &changed); err != nil {
		t.Fatal(err)
	}
	changed["data_generation_id"] = "another-generation"
	operation.Steps[0].Output, _ = json.Marshal(changed)
	if restoreCompatible(operation, schemas, profile, "backup-1", "staging") {
		t.Fatal("compatibility evidence for another source generation was accepted")
	}
}

func TestRestoreApplyRequiresNewTargetGenerationAndSourceBinding(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	target := json.RawMessage(`{"server_id":"server-1","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}}`)
	execution := json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	input := json.RawMessage(`{"source_backup":{"operation_type":"server.backup","partial":false,"failed_steps":[],"unresolved_conditions":[],"operation_data":{"backup_id":"backup-1","environment_id":"staging","data_generation_id":"generation-source","manifest":{}}},"target":` + string(target) + `,"target_release_id":"paper-1","target_data_generation_id":"restore-restore-op","required_check_ids":["world.check"],"object_streams":[{"object_ref":{"adapter_id":"storage.fake","storage_profile_id":"storage-1","environment_id":"staging","container":"backups","key":"server-1/world.tar","version_id":"version-1"}}]}`)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	output := json.RawMessage(strings.ReplaceAll(`{"backup_id":"backup-1","environment_id":"staging","source_data_generation_id":"generation-source","target":`+string(target)+`,"target_data_generation_id":"restore-restore-op","restore_execution_ref":`+string(execution)+`,"outcome":"restored","observed_at":"2026-11-08T10:00:00Z","validation_checks":[{"check_id":"world.check","result":"pass","observed_at":"2026-11-08T10:00:00Z"}]}`, "2026-11-08T10:00:00Z", stamp))
	server := store.LogicalServer{ServerID: "server-1", EnvironmentID: "staging", GameAdapterID: "game.paper",
		InstanceRef: json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)}
	started := time.Now().UTC()
	operation := store.OperationRecord{OperationID: "restore-op", Steps: []store.OperationStep{{StepID: restoreApplyStep, Status: protocolstate.StepSucceeded, Input: input, Output: output, StartedAt: &started}}}
	if !restoreApplySucceeded(operation, schemas, "backup-1", server) {
		t.Fatal("exact restore evidence was rejected")
	}
	var changed map[string]any
	if err := json.Unmarshal(output, &changed); err != nil {
		t.Fatal(err)
	}
	changed["restore_execution_ref"] = json.RawMessage(`{"adapter_id":"game.other","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	operation.Steps[0].Output, _ = json.Marshal(changed)
	if restoreApplySucceeded(operation, schemas, "backup-1", server) {
		t.Fatal("restore execution reference for another game adapter was accepted")
	}
	changed["restore_execution_ref"] = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	changed["target_data_generation_id"] = "generation-source"
	operation.Steps[0].Output, _ = json.Marshal(changed)
	if restoreApplySucceeded(operation, schemas, "backup-1", server) {
		t.Fatal("restore reused the backup's source generation")
	}
}

func TestRestoreProfileRejectsCrossStorageAndDuplicateChecks(t *testing.T) {
	server := store.LogicalServer{EnvironmentID: "staging", StorageProfile: "storage-1", GameAdapterID: "game.paper", ExecutionAdapterID: "execution.fake"}
	profile := ServerRestoreProfile{ProfileID: "profile-1", GameReleaseID: "paper-1", GameAdapterID: "game.paper", GameVersion: "1.21.4",
		GameReleaseDescription: json.RawMessage(`{}`), StorageProfileID: "storage-1", StorageAdapterID: "storage.fake",
		UnitRef:             json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper"}`),
		ReadinessCheckSetID: "paper.checks", RequiredReadinessChecks: []ReadinessRequirement{{CheckID: "ready", CheckVersion: "1"}},
		RequiredCompatibilityCheckIDs: []string{"world.check"}, RequiredRestoreCheckIDs: []string{"world.loaded"}}
	if err := validateServerRestoreProfile(server, profile); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	profile.StorageProfileID = "storage-2"
	if err := validateServerRestoreProfile(server, profile); err == nil {
		t.Fatal("profile for a different persisted storage profile was accepted")
	}
	profile.StorageProfileID = "storage-1"
	profile.RequiredRestoreCheckIDs = []string{"world.loaded", "world.loaded"}
	if err := validateServerRestoreProfile(server, profile); err == nil {
		t.Fatal("duplicate restore checks were accepted")
	}
}

func TestRestoreStreamPlanBindsVerifiedBackupAndOrderedObjects(t *testing.T) {
	backup := json.RawMessage(`{"operation_type":"server.backup","partial":false,"failed_steps":[],"unresolved_conditions":[],"operation_data":{"backup_id":"backup-1","environment_id":"staging","data_generation_id":"generation-source","manifest":{}}}`)
	first := json.RawMessage(`{"adapter_id":"storage.fake","storage_profile_id":"storage-1","environment_id":"staging","container":"backups","key":"world.tar","version_id":"v1"}`)
	second := json.RawMessage(`{"adapter_id":"storage.fake","storage_profile_id":"storage-1","environment_id":"staging","container":"backups","key":"plugins.tar","version_id":"v1"}`)
	manifestRaw := json.RawMessage(`{"manifest_version":"1","backup_id":"backup-1","server_id":"server-1","environment_id":"staging","data_generation_id":"generation-source","created_at":"2026-11-08T10:00:00Z","source_operation_id":"backup-op","game":{"adapter_id":"game.paper","name":"Paper","version":"1.21.4"},"consistency":{"mode":"offline","evidence":"stopped"},"objects":[{"object_ref":` + string(first) + `,"size_bytes":1,"checksum_algorithm":"sha256","checksum":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"object_ref":` + string(second) + `,"size_bytes":1,"checksum_algorithm":"sha256","checksum":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}`)
	var manifest provisionBackupManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	target := json.RawMessage(`{"server_id":"server-1","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}}`)
	input, err := json.Marshal(map[string]any{
		"source_backup": backup, "target": target, "target_release_id": "paper-1", "target_data_generation_id": "restore-restore-op",
		"object_streams": []map[string]json.RawMessage{{"object_ref": first}, {"object_ref": second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := store.OperationStep{Input: input}
	server := store.LogicalServer{ServerID: "server-1", EnvironmentID: "staging", InstanceRef: json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)}
	profile := ServerRestoreProfile{GameReleaseID: "paper-1"}
	operation := store.OperationRecord{OperationID: "restore-op"}
	if err := validateRestoreStreamPlan(step, backup, manifest, server, profile, operation); err != nil {
		t.Fatalf("matching immutable plan rejected: %v", err)
	}
	changedBackup := json.RawMessage(strings.Replace(string(backup), "generation-source", "generation-changed", 1))
	if err := validateRestoreStreamPlan(step, changedBackup, manifest, server, profile, operation); err == nil {
		t.Fatal("a changed backup result was accepted for the immutable plan")
	}
	step.Input, _ = json.Marshal(map[string]any{
		"source_backup": backup, "target": target, "target_release_id": "paper-1", "target_data_generation_id": "restore-restore-op",
		"object_streams": []map[string]json.RawMessage{{"object_ref": second}, {"object_ref": first}},
	})
	if err := validateRestoreStreamPlan(step, backup, manifest, server, profile, operation); err == nil {
		t.Fatal("a changed object stream order was accepted")
	}
}

func TestRestoreExecutionReferenceMustMatchAdapterEnvironmentAndKind(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	valid := json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	if !validateRestoreExecutionRef(schemas, valid, "game.paper", "staging") {
		t.Fatal("matching restore execution reference was rejected")
	}
	for _, ref := range []json.RawMessage{
		json.RawMessage(`{"adapter_id":"game.other","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`),
		json.RawMessage(`{"adapter_id":"game.paper","environment_id":"production","resource_kind":"restore-execution","external_id":"restore-1"}`),
		json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"restore-1"}`),
	} {
		if validateRestoreExecutionRef(schemas, ref, "game.paper", "staging") {
			t.Fatalf("mismatched execution reference was accepted: %s", ref)
		}
	}
}

func TestRestoreReconciliationRejectsWrongExecutionReferenceBeforeAdapterCall(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	version := schemas.ProtocolVersion()
	game := fakeadapter.New(adapter.Descriptor{AdapterID: "game.paper", Category: adapter.CategoryGame,
		DisplayName: "fake Paper", ImplementationVersion: "test-1", ProtocolVersions: []string{version},
		ContractVersions: []string{"1"}, RequiredPermissions: []string{}, Capabilities: []adapter.Capability{{
			Name: "game.restore.inspect", Version: "1", Mutating: false,
			InputSchemaURI:  "urn:gameserver-controller:" + version + ":game:restore-inspect-input",
			OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:restore-output", RequiredPermissions: []string{},
		}}})
	if err := registry.Register(game); err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	workflow := &ServerRestoreWorkflow{servers: operationStore, adapters: registry, schemas: schemas}
	operation := store.OperationRecord{OperationID: "restore-op", OperationType: "server.restore", ProtocolVersion: version,
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"}}
	steps := []store.OperationStep{
		{StepID: restoreApplyStep, CapabilityName: "game.restore", AdapterID: "game.paper",
			ExternalOperationRef: json.RawMessage(`{"adapter_id":"game.other","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)},
		{StepID: restoreApplyStep, CapabilityName: "game.restore", AdapterID: "game.paper",
			ExternalOperationRef: json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`),
			Output:               json.RawMessage(`{"restore_execution_ref":{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-2"}}`)},
	}
	for _, step := range steps {
		resolution, err := workflow.Reconcile(context.Background(), operation, step)
		if err != nil {
			t.Fatal(err)
		}
		if resolution.State != ResolutionNeedsOperator {
			t.Fatalf("resolution = %s, want needs_operator", resolution.State)
		}
		if calls := game.Calls(); len(calls) != 0 {
			t.Fatalf("untrusted execution reference reached adapter inspection: %#v", calls)
		}
	}
}

func TestServerRestorePlanDefaultsToStoppedAndAddsStartOnlyOnRequest(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	version := schemas.ProtocolVersion()
	capability := func(name, input, output string, mutating bool) adapter.Capability {
		return adapter.Capability{Name: name, Version: "1", Mutating: mutating,
			InputSchemaURI:  "urn:gameserver-controller:" + version + ":" + input,
			OutputSchemaURI: "urn:gameserver-controller:" + version + ":" + output, RequiredPermissions: []string{}}
	}
	hosting := fakeadapter.New(adapter.Descriptor{AdapterID: "hosting.fake", Category: adapter.CategoryHosting, DisplayName: "fake host",
		ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{capability("hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false)}})
	execution := fakeadapter.New(adapter.Descriptor{AdapterID: "execution.fake", Category: adapter.CategoryExecution, DisplayName: "fake runtime",
		ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{
			capability("execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false),
			capability("execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true),
		}})
	readiness := []ReadinessRequirement{{CheckID: "paper.startup-complete", CheckVersion: "1"}, {CheckID: "paper.world-loaded", CheckVersion: "2"}}
	readinessDeclarations := make([]any, len(readiness))
	for i, check := range readiness {
		readinessDeclarations[i] = map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion}
	}
	gameScript := fakeadapter.New(adapter.Descriptor{AdapterID: "game.paper", Category: adapter.CategoryGame, DisplayName: "fake Paper",
		ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{
			capability("game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false),
			{Name: "game.restore", Version: "1", Mutating: true, InputSchemaURI: "urn:gameserver-controller:" + version + ":game:restore-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:restore-output", RequiredPermissions: []string{},
				Constraints: map[string]any{"stream_transfer_direction": "controller_to_adapter"}},
			capability("game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false),
			{Name: "game.readiness.check", Version: "1", Mutating: false, InputSchemaURI: "urn:gameserver-controller:" + version + ":game:readiness-check-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:readiness-check-output", RequiredPermissions: []string{},
				Constraints: map[string]any{"readiness_check_sets": []any{map[string]any{
					"game_release_id": "paper-1", "environment_ids": []string{"staging"},
					"readiness_check_set_id": "paper.checks", "required_checks": readinessDeclarations,
				}}}},
		}, StreamTransportProfiles: []string{"local-unix-stream-v1"}})
	game := &restoreStreamConsumingFake{scripted: gameScript}
	for _, fake := range []adapter.Implementation{hosting, execution, game} {
		if err := registry.Register(fake); err != nil {
			t.Fatal(err)
		}
	}
	operationStore, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	if err := seedStoppedStartTestServer(operationStore, version); err != nil {
		t.Fatal(err)
	}
	objectRef := json.RawMessage(`{"adapter_id":"storage.fake","storage_profile_id":"test-storage","environment_id":"staging","container":"backups","key":"start-test/world.tar","version_id":"v1"}`)
	backupBytes := "x"
	backupDigest := sha256.Sum256([]byte(backupBytes))
	manifest := json.RawMessage(`{"manifest_version":"1","backup_id":"backup-1","server_id":"start-test","environment_id":"staging","data_generation_id":"generation-source","created_at":"2026-11-08T10:00:00Z","source_operation_id":"backup-op","game":{"adapter_id":"game.paper","name":"Paper","version":"1.21.4"},"consistency":{"mode":"offline","evidence":"stopped"},"objects":[{"object_ref":` + string(objectRef) + `,"size_bytes":1,"checksum_algorithm":"sha256","checksum":"` + hex.EncodeToString(backupDigest[:]) + `"}]}`)
	backupResult := json.RawMessage(`{"outcome_summary":"Verified backup","operation_type":"server.backup","affected_resources":[],"completed_steps":[],"failed_steps":[],"warnings":[],"unresolved_conditions":[],"audit_event_ids":[],"partial":false,"operation_data":{"backup_id":"backup-1","environment_id":"staging","data_generation_id":"generation-source","manifest":` + string(manifest) + `,"verified_at":"2026-11-08T10:00:00Z","final_lifecycle_state":"stopped"}}`)
	release := json.RawMessage(`{"game_adapter_id":"game.paper","game_name":"Paper","release_id":"paper-1","game_version":"1.21.4","targets":[{"os_family":"linux","architecture":"x86_64"}],"artifacts":[{"artifact_id":"server-jar","artifact_uri":"https://example.invalid/paper.jar","checksum":{"algorithm":"sha256","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],"runtime_requirements":[{"runtime_id":"java","version_requirement":"21"}],"configuration_schema_uri":"https://example.invalid/config.schema.json","configuration_schema_checksum":{"algorithm":"sha256","digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"data_format":{"format_id":"paper-world","version":"1"},"required_execution_capabilities":["systemd"]}`)
	profile := ServerRestoreProfile{ProfileID: "profile-1", GameReleaseID: "paper-1", GameAdapterID: "game.paper", GameVersion: "1.21.4",
		GameReleaseDescription: release, RequiredCompatibilityCheckIDs: []string{"world.compatibility"}, RequiredRestoreCheckIDs: []string{"world.loaded"},
		StorageProfileID: "test-storage", StorageAdapterID: "storage.fake", StorageVersioningEnabled: true, TargetDataKnownEmpty: true,
		UnitRef: json.RawMessage(startTestUnit), ReadinessCheckSetID: "paper.checks", RequiredReadinessChecks: readiness}
	authorizer := &countingServerRestoreAuthorizer{}
	workflow, err := NewServerRestoreWorkflow(operationStore, registry, schemas, staticServerRestoreProfileResolver{profile},
		staticServerRestoreBackupResolver{result: backupResult, object: objectRef, data: backupBytes}, authorizer, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	newOperation := func(operationID, parameters string) store.OperationRecord {
		return store.OperationRecord{OperationID: operationID, OperationType: "server.restore", ProtocolVersion: version,
			Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1",
			RequestSnapshot: store.RequestSnapshot{OperationType: "server.restore", Parameters: json.RawMessage(parameters)}}
	}
	replaceParameters := `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1"}`
	defaultOperation := newOperation("restore-plan", replaceParameters)
	plan, err := workflow.BuildPlan(context.Background(), defaultOperation)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{restoreInspectInstanceStep, restoreCompatibilityStep, restoreInspectRuntimeStep, restoreApplyStep, restoreInspectStep}
	if len(plan) != len(want) {
		t.Fatalf("default restore plan has %d steps, want %d", len(plan), len(want))
	}
	for i, step := range plan {
		if step.StepID != want[i] {
			t.Fatalf("default restore step %d is %q, want %q", i, step.StepID, want[i])
		}
	}
	if err := workflow.CheckPreconditions(context.Background(), newOperation("restore-precondition", replaceParameters), plan[0]); err != nil {
		t.Fatalf("read-only provider inspection precondition failed: %v", err)
	}
	if authorizer.calls != 0 {
		t.Fatalf("read-only restore planning or inspection checked authorization %d times", authorizer.calls)
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	compatibilityOutput, err := json.Marshal(map[string]any{
		"backup_id": "backup-1", "environment_id": "staging", "data_generation_id": "generation-source",
		"source_game_adapter_id": "game.paper", "source_game_version": "1.21.4", "target_game_adapter_id": "game.paper",
		"target_release_id": "paper-1", "target_game_version": "1.21.4", "compatibility_status": "compatible", "observed_at": observedAt,
		"check_results": []map[string]string{{"check_id": "world.compatibility", "check_version": "1", "result": "compatible", "observed_at": observedAt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compatibilityStarted := time.Now().UTC()
	plan[1].Status = protocolstate.StepSucceeded
	plan[1].StartedAt = &compatibilityStarted
	plan[1].Output = compatibilityOutput
	defaultOperation.Steps = plan
	transfers, err := workflow.StreamTransfersForStep(context.Background(), defaultOperation, plan[3], time.Now().Add(time.Minute))
	if err != nil || len(transfers) != 1 {
		t.Fatalf("verified restore stream count=%d err=%v", len(transfers), err)
	}
	streamed, err := io.ReadAll(transfers[0].Source)
	if err != nil || string(streamed) != backupBytes {
		t.Fatalf("restore stream bytes=%q err=%v", streamed, err)
	}
	if err := transfers[0].Verify(); err != nil {
		t.Fatalf("verified restore stream did not match the manifest: %v", err)
	}
	_ = transfers[0].Source.Close()
	plan, err = workflow.BuildPlan(context.Background(), newOperation("restore-explicit-start", `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","start_after_restore":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 8 || plan[5].StepID != restoreStartStep || plan[6].StepID != restoreVerifyRuntimeStep || plan[7].StepID != restoreReadinessStep {
		t.Fatalf("explicit-start restore plan has unexpected steps: %#v", plan)
	}
	if authorizer.calls != 0 {
		t.Fatalf("plan construction consumed authorization %d times", authorizer.calls)
	}

	e2e := newOperation("restore-e2e", replaceParameters)
	e2e.IdempotencyKey = "restore-e2e-key"
	e2e.RequestFingerprint = "sha256:" + strings.Repeat("e", 64)
	logicalServer, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	e2e.Target.InstanceRef = append(json.RawMessage(nil), logicalServer.Record.InstanceRef...)
	e2e.Steps, err = workflow.BuildPlan(context.Background(), e2e)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := operationStore.AcceptOperation(e2e, testProof(88), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.restore", workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(operationStore, registry, schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	providerOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":"active","observed_at":%q}`, startTestInstance, observedAt))
	if err := hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, observedAt))
	if err := execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := gameScript.SetScript("game.compatibility.check", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: compatibilityOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	restoreRef := json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-e2e"}`)
	makeRestoreOutput := func() json.RawMessage {
		stamp := time.Now().UTC().Add(1 * time.Minute).Format(time.RFC3339Nano)
		return json.RawMessage(fmt.Sprintf(`{"backup_id":"backup-1","environment_id":"staging","source_data_generation_id":"generation-source","target":%s,"target_data_generation_id":"restore-restore-e2e","restore_execution_ref":%s,"outcome":"restored","observed_at":%q,"validation_checks":[{"check_id":"world.loaded","result":"pass","observed_at":%q}]}`,
			stepField(e2e.Steps[3].Input, "target"), restoreRef, stamp, stamp))
	}
	if err := gameScript.SetScript("game.restore", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: makeRestoreOutput(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := gameScript.SetScript("game.restore.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: makeRestoreOutput(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	completed, err := engine.Run(context.Background(), accepted.Record.OperationID)
	if err != nil {
		latest, getOperationErr := operationStore.GetOperation(accepted.Record.OperationID)
		server, getServerErr := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
		stepStates := make([]string, 0, len(latest.Record.Steps))
		for _, step := range latest.Record.Steps {
			stepStates = append(stepStates, step.StepID+"="+string(step.Status))
		}
		projected, transition, projectionErr := workflow.ProjectLifecycle(latest.Record, server)
		t.Fatalf("engine run: %v; operation=%s steps=%v; server=%s data_generation=%s/%s; projected=%s generation=%s/%s transition=%t projection_error=%v; load_errors=%v/%v", err, latest.Record.Status, stepStates,
			server.Record.LifecycleState, server.Record.DataGenerationID, server.Record.DataGenerationStatus, projected.LifecycleState, projected.DataGenerationID, projected.DataGenerationStatus, transition, projectionErr, getOperationErr, getServerErr)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 5 {
		stepStates := make([]string, 0, len(completed.Record.Steps))
		for _, step := range completed.Record.Steps {
			stepStates = append(stepStates, step.StepID+"="+string(step.Status))
		}
		var preconditionErr error
		var currentServer store.StoredLogicalServer
		if current, getErr := operationStore.GetLogicalServer(startTestEnvID, startTestServerID); getErr == nil {
			currentServer = current
		}
		compatStep := operationStep(completed.Record, restoreCompatibilityStep)
		if len(completed.Record.Steps) > 3 {
			preconditionErr = workflow.CheckPreconditions(context.Background(), completed.Record, completed.Record.Steps[3])
		}
		t.Fatalf("restore operation status=%s step_states=%v lifecycle=%s compatibility_ok=%t compatibility_fresh=%t failed_step_precondition=%v error=%s", completed.Record.Status, stepStates, currentServer.Record.LifecycleState,
			restoreCompatible(completed.Record, schemas, profile, "backup-1", startTestEnvID), observationIsFreshForStep(stepFieldString(compatStep.Output, "observed_at"), compatStep), preconditionErr, completed.Record.Error)
	}
	for _, step := range completed.Record.Steps {
		if step.Status != protocolstate.StepSucceeded || step.Attempt != 1 {
			t.Fatalf("restore step %q status/attempt=%s/%d", step.StepID, step.Status, step.Attempt)
		}
	}
	finalServer, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || finalServer.Record.LifecycleState != protocolstate.ServerStopped || finalServer.Record.DesiredState != "stopped" ||
		finalServer.Record.DataGenerationID != "restore-restore-e2e" || finalServer.Record.DataGenerationStatus != "known" {
		t.Fatalf("restored server projection=%#v err=%v", finalServer.Record, err)
	}
}

func TestRestoreRuntimeObservationMustBeCurrentAndUnknownDataRequiresApproval(t *testing.T) {
	now := time.Date(2026, time.November, 8, 12, 0, 0, 0, time.UTC)
	if !observationIsCurrent(now.Format(time.RFC3339Nano), time.Minute, now) {
		t.Fatal("current runtime observation was rejected")
	}
	if observationIsCurrent(now.Add(-time.Minute-time.Nanosecond).Format(time.RFC3339Nano), time.Minute, now) {
		t.Fatal("stale runtime observation was accepted")
	}
	if observationIsCurrent(now.Add(time.Minute).Format(time.RFC3339Nano), time.Minute, now) {
		t.Fatal("future runtime observation was accepted")
	}

	profile := ServerRestoreProfile{TargetDataKnownEmpty: true}
	server := store.LogicalServer{DataGenerationStatus: "unknown"}
	if !restoreRequiresOverwriteApproval(server, profile) {
		t.Fatal("unknown target data incorrectly relied on an earlier empty-target assertion")
	}
	server.DataGenerationStatus = "known"
	if restoreRequiresOverwriteApproval(server, profile) {
		t.Fatal("confirmed empty target unexpectedly required overwrite approval")
	}

	checks := []restoreValidationCheck{{CheckID: "world.loaded", Result: "pass", ObservedAt: now.Format(time.RFC3339Nano)}}
	if !restoreValidationChecksCurrentAt(checks, now.Format(time.RFC3339Nano), time.Minute) {
		t.Fatal("fresh restore validation check was rejected")
	}
	checks[0].ObservedAt = now.Add(-time.Minute - time.Nanosecond).Format(time.RFC3339Nano)
	if restoreValidationChecksCurrentAt(checks, now.Format(time.RFC3339Nano), time.Minute) {
		t.Fatal("stale restore validation check was accepted")
	}
}

func TestRestoreLifecycleProjectionMarksAttemptedFailureUnknown(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	workflow := &ServerRestoreWorkflow{schemas: schemas}
	server := store.StoredLogicalServer{Record: store.LogicalServer{
		ServerID: "server-1", EnvironmentID: "staging", GameAdapterID: "game.paper",
		LifecycleState: protocolstate.ServerStopped, DesiredState: "stopped",
		InstanceRef:      json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`),
		DataGenerationID: "generation-before", DataGenerationStatus: "known",
	}}
	operation := store.OperationRecord{OperationID: "restore-op", OperationType: "server.restore", ProtocolVersion: schemas.ProtocolVersion(),
		Status: protocolstate.OperationRunning, Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.restore", Parameters: json.RawMessage(`{"backup_id":"backup-1"}`)},
		Steps:           []store.OperationStep{{StepID: restoreApplyStep, Status: protocolstate.StepPending, Input: json.RawMessage(`{}`)}},
	}
	unchanged, changed, err := workflow.ProjectLifecycle(operation, server)
	if err != nil || changed || unchanged.LifecycleState != protocolstate.ServerStopped {
		t.Fatalf("pending restore projected state=%s changed=%v err=%v; want unchanged stopped", unchanged.LifecycleState, changed, err)
	}
	operation.Steps[0].Status = protocolstate.StepRunning
	operation.Steps[0].Attempt = 1
	restoring, changed, err := workflow.ProjectLifecycle(operation, server)
	if err != nil || !changed || restoring.LifecycleState != protocolstate.ServerRestoring || restoring.DesiredState != "stopped" {
		t.Fatalf("dispatched restore projected state=%s desired=%s changed=%v err=%v", restoring.LifecycleState, restoring.DesiredState, changed, err)
	}
	operation.Status = protocolstate.OperationFailed
	operation.Steps[0].Status = protocolstate.StepFailed
	failed, changed, err := workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: restoring})
	if err != nil || !changed || failed.LifecycleState != protocolstate.ServerFailed || failed.DataGenerationStatus != "unknown" || failed.DataGenerationID != "" {
		t.Fatalf("attempted restore failure projected state=%s generation=%q/%s changed=%v err=%v",
			failed.LifecycleState, failed.DataGenerationID, failed.DataGenerationStatus, changed, err)
	}
}
