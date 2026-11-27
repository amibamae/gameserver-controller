package workflow

import (
	"context"
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
	schemas := workflowTestSchemas(t)
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
	schemas := workflowTestSchemas(t)
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
	schemas := workflowTestSchemas(t)
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
	schemas := workflowTestSchemas(t)
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
	schemas := workflowTestSchemas(t)
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
