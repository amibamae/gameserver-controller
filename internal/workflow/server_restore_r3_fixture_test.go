package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// This fixture plans a server.restore against in-memory collaborators so the
// characterization tests can call BuildPlan, CheckPreconditions and Reconcile
// directly and pin their exact results.

const (
	restoreR3Instance      = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`
	restoreR3OtherInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-2"}`
	restoreR3Unit          = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper"}`
	restoreR3OtherUnit     = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper-2"}`
	restoreR3ObjectRef     = `{"adapter_id":"storage.fake","storage_profile_id":"storage-1","environment_id":"staging","container":"backups","key":"server-1/world.tar","version_id":"v1"}`
	restoreR3Execution     = `{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`
	restoreR3Release       = `{"game_adapter_id":"game.paper","game_name":"Paper","release_id":"paper-1","game_version":"1.21.4","targets":[{"os_family":"linux","architecture":"x86_64"}],"artifacts":[{"artifact_id":"server-jar","artifact_uri":"https://example.invalid/paper.jar","checksum":{"algorithm":"sha256","digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],"runtime_requirements":[{"runtime_id":"java","version_requirement":"21"}],"configuration_schema_uri":"https://example.invalid/config.schema.json","configuration_schema_checksum":{"algorithm":"sha256","digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"data_format":{"format_id":"paper-world","version":"1"},"required_execution_capabilities":["systemd"]}`
	restoreR3Replace       = `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1"}`
	restoreR3ReplaceStart  = `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","start_after_restore":true}`
	restoreR3BackupData    = "x"
)

// restoreR3PlanOrder is the full planned order when start_after_restore is set.
var restoreR3PlanOrder = []string{restoreInspectInstanceStep, restoreCompatibilityStep, restoreInspectRuntimeStep, restoreApplyStep,
	restoreInspectStep, restoreStartStep, restoreVerifyRuntimeStep, restoreReadinessStep}

type restoreR3Repository struct {
	store.LifecycleOperationStore
	record store.StoredLogicalServer
}

func (repository *restoreR3Repository) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	if repository.record.Record.EnvironmentID != environmentID || repository.record.Record.ServerID != serverID {
		return store.StoredLogicalServer{}, store.ErrNotFound
	}
	return repository.record, nil
}

type restoreR3Profiles struct {
	profile ServerRestoreProfile
	err     error
}

func (profiles *restoreR3Profiles) ResolveServerRestore(context.Context, store.LogicalServer) (ServerRestoreProfile, error) {
	return profiles.profile, profiles.err
}

type restoreR3Backups struct {
	backup VerifiedProvisionBackup
	err    error
}

func (backups *restoreR3Backups) ResolveServerRestoreBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error) {
	return VerifiedProvisionBackup{Result: append(json.RawMessage(nil), backups.backup.Result...), IntegrityVerified: backups.backup.IntegrityVerified}, backups.err
}

func (backups *restoreR3Backups) OpenServerRestoreBackupObject(context.Context, store.LogicalServer, string, json.RawMessage) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(restoreR3BackupData)), nil
}

type restoreR3Authorizer struct {
	err   error
	calls int
}

func (authorizer *restoreR3Authorizer) AuthorizeServerRestore(context.Context, string, string, store.OperationRecord, store.LogicalServer, string) error {
	authorizer.calls++
	return authorizer.err
}

// restoreR3Adapters serves fixed descriptors and answers Invoke with reply.
type restoreR3Adapters struct {
	version     string
	descriptors map[string]adapter.Descriptor
	reply       func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
	calls       []adapter.InvokeRequest
}

func (adapters *restoreR3Adapters) ProtocolVersion() string { return adapters.version }

func (adapters *restoreR3Adapters) Descriptor(adapterID string) (adapter.Descriptor, error) {
	descriptor, ok := adapters.descriptors[adapterID]
	if !ok {
		return adapter.Descriptor{}, adapter.ErrAdapterNotFound
	}
	return descriptor, nil
}

func (adapters *restoreR3Adapters) Invoke(_ context.Context, _ string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	adapters.calls = append(adapters.calls, request)
	if adapters.reply == nil {
		return adapter.InvokeResponse{}, io.ErrUnexpectedEOF
	}
	return adapters.reply(request)
}

func restoreR3Capability(version, name, input, output string, mutating bool, constraints map[string]any) adapter.Capability {
	return adapter.Capability{Name: name, Version: "1", Mutating: mutating,
		InputSchemaURI:  "urn:gameserver-controller:" + version + ":" + input,
		OutputSchemaURI: "urn:gameserver-controller:" + version + ":" + output, RequiredPermissions: []string{}, Constraints: constraints}
}

func restoreR3Descriptors(version string) map[string]adapter.Descriptor {
	readiness := map[string]any{"readiness_check_sets": []any{map[string]any{
		"game_release_id": "paper-1", "environment_ids": []string{"staging"}, "readiness_check_set_id": "paper.checks",
		"required_checks": []any{map[string]any{"check_id": "paper.ready", "check_version": "1"}},
	}}}
	return map[string]adapter.Descriptor{
		"hosting.fake": {AdapterID: "hosting.fake", Category: adapter.CategoryHosting, Capabilities: []adapter.Capability{
			restoreR3Capability(version, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false, nil),
		}},
		"execution.fake": {AdapterID: "execution.fake", Category: adapter.CategoryExecution, Capabilities: []adapter.Capability{
			restoreR3Capability(version, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false, nil),
			restoreR3Capability(version, "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true, nil),
		}},
		"game.paper": {AdapterID: "game.paper", Category: adapter.CategoryGame, Capabilities: []adapter.Capability{
			restoreR3Capability(version, "game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false, nil),
			restoreR3Capability(version, "game.restore", "game:restore-input", "game:restore-output", true, map[string]any{"stream_transfer_direction": "controller_to_adapter"}),
			restoreR3Capability(version, "game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false, nil),
			restoreR3Capability(version, "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false, readiness),
		}},
	}
}

func restoreR3Manifest() json.RawMessage {
	digest := sha256.Sum256([]byte(restoreR3BackupData))
	return json.RawMessage(`{"manifest_version":"1","backup_id":"backup-1","server_id":"server-1","environment_id":"staging","data_generation_id":"generation-source","created_at":"2026-11-08T10:00:00Z","source_operation_id":"backup-op","game":{"adapter_id":"game.paper","name":"Paper","version":"1.21.4"},"consistency":{"mode":"offline","evidence":"stopped"},"objects":[{"object_ref":` + restoreR3ObjectRef + `,"size_bytes":1,"checksum_algorithm":"sha256","checksum":"` + hex.EncodeToString(digest[:]) + `"}]}`)
}

func restoreR3BackupResult() json.RawMessage {
	return json.RawMessage(`{"outcome_summary":"Verified backup","operation_type":"server.backup","affected_resources":[],"completed_steps":[],"failed_steps":[],"warnings":[],"unresolved_conditions":[],"audit_event_ids":[],"partial":false,"operation_data":{"backup_id":"backup-1","environment_id":"staging","data_generation_id":"generation-source","manifest":` + string(restoreR3Manifest()) + `,"verified_at":"2026-11-08T10:00:00Z","final_lifecycle_state":"stopped"}}`)
}

func restoreR3Server() store.LogicalServer {
	return store.LogicalServer{ServerID: "server-1", EnvironmentID: "staging", LifecycleState: protocolstate.ServerStopped, DesiredState: "stopped",
		InstanceRef: json.RawMessage(restoreR3Instance), GameAdapterID: "game.paper", ExecutionAdapterID: "execution.fake",
		StorageProfile: "storage-1", DataGenerationID: "generation-before", DataGenerationStatus: "known"}
}

func restoreR3Profile() ServerRestoreProfile {
	return ServerRestoreProfile{ProfileID: "profile-1", GameReleaseID: "paper-1", GameAdapterID: "game.paper", GameVersion: "1.21.4",
		GameReleaseDescription: json.RawMessage(restoreR3Release), RequiredCompatibilityCheckIDs: []string{"world.compatibility"},
		RequiredRestoreCheckIDs: []string{"world.loaded"}, StorageProfileID: "storage-1", StorageAdapterID: "storage.fake",
		StorageVersioningEnabled: true, UnitRef: json.RawMessage(restoreR3Unit), ReadinessCheckSetID: "paper.checks",
		RequiredReadinessChecks: []ReadinessRequirement{{CheckID: "paper.ready", CheckVersion: "1"}}}
}

type restoreR3Fixture struct {
	workflow   *ServerRestoreWorkflow
	operation  store.OperationRecord
	servers    *restoreR3Repository
	profiles   *restoreR3Profiles
	backups    *restoreR3Backups
	adapters   *restoreR3Adapters
	authorizer *restoreR3Authorizer
}

// newRestoreR3Fixture builds an unplanned restore of a stopped server whose
// data is not known to be empty, with the given request parameters.
func newRestoreR3Fixture(t *testing.T, parameters string) *restoreR3Fixture {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	version := schemas.ProtocolVersion()
	fixture := &restoreR3Fixture{
		servers:    &restoreR3Repository{record: store.StoredLogicalServer{Record: restoreR3Server()}},
		profiles:   &restoreR3Profiles{profile: restoreR3Profile()},
		backups:    &restoreR3Backups{backup: VerifiedProvisionBackup{Result: restoreR3BackupResult(), IntegrityVerified: true}},
		adapters:   &restoreR3Adapters{version: version, descriptors: restoreR3Descriptors(version)},
		authorizer: &restoreR3Authorizer{},
	}
	fixture.workflow, err = NewServerRestoreWorkflow(fixture.servers, fixture.adapters, schemas, fixture.profiles, fixture.backups, fixture.authorizer, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fixture.operation = store.OperationRecord{OperationID: "restore-op", OperationType: "server.restore", ProtocolVersion: version,
		Status: protocolstate.OperationRunning, ActorID: "operator-1", CorrelationID: "correlation-1",
		Target:          store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.restore", Parameters: json.RawMessage(parameters)}}
	return fixture
}

// newPlannedRestoreR3Fixture plans the restore and records the plan as the
// operation's steps.
func newPlannedRestoreR3Fixture(t *testing.T, parameters string) *restoreR3Fixture {
	t.Helper()
	fixture := newRestoreR3Fixture(t, parameters)
	steps, err := fixture.workflow.BuildPlan(context.Background(), fixture.operation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.operation.Steps = steps
	return fixture
}

func (fixture *restoreR3Fixture) step(stepID string) *store.OperationStep {
	for i := range fixture.operation.Steps {
		if fixture.operation.Steps[i].StepID == stepID {
			return &fixture.operation.Steps[i]
		}
	}
	panic("no step " + stepID)
}

func (fixture *restoreR3Fixture) server() *store.LogicalServer { return &fixture.servers.record.Record }

func restoreR3Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// restoreR3ErrText returns the error text, or "" for no error.
func restoreR3ErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func restoreR3JSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// restoreR3Succeed records a successful attempt with the given output.
func restoreR3Succeed(fixture *restoreR3Fixture, stepID string, output json.RawMessage) {
	started := time.Now().UTC()
	step := fixture.step(stepID)
	step.Status = protocolstate.StepSucceeded
	step.Attempt = 1
	step.StartedAt = &started
	step.Output = output
}

// restoreR3GameOutput is a valid game.restore result for the planned target.
func restoreR3GameOutput(t *testing.T, fixture *restoreR3Fixture) map[string]any {
	t.Helper()
	now := restoreR3Now()
	return map[string]any{"backup_id": "backup-1", "environment_id": "staging", "source_data_generation_id": "generation-source",
		"target": stepField(fixture.step(restoreApplyStep).Input, "target"), "target_data_generation_id": "restore-restore-op",
		"restore_execution_ref": json.RawMessage(restoreR3Execution), "outcome": "restored", "observed_at": now,
		"validation_checks": []any{map[string]any{"check_id": "world.loaded", "result": "pass", "observed_at": now}}}
}

func restoreR3ValidOutput(t *testing.T, fixture *restoreR3Fixture, stepID string) json.RawMessage {
	t.Helper()
	now := restoreR3Now()
	switch stepID {
	case restoreInspectInstanceStep:
		return restoreR3JSON(t, map[string]any{"instance_ref": json.RawMessage(restoreR3Instance), "state": "stopped", "observed_at": now})
	case restoreCompatibilityStep:
		return restoreR3JSON(t, map[string]any{"backup_id": "backup-1", "environment_id": "staging", "data_generation_id": "generation-source",
			"source_game_adapter_id": "game.paper", "source_game_version": "1.21.4", "target_game_adapter_id": "game.paper",
			"target_release_id": "paper-1", "target_game_version": "1.21.4", "compatibility_status": "compatible", "observed_at": now,
			"check_results": []any{map[string]any{"check_id": "world.compatibility", "check_version": "1", "result": "compatible", "observed_at": now}}})
	case restoreInspectRuntimeStep:
		return restoreR3JSON(t, map[string]any{"unit_ref": json.RawMessage(restoreR3Unit), "state": "stopped", "observed_at": now})
	case restoreApplyStep, restoreInspectStep:
		return restoreR3JSON(t, restoreR3GameOutput(t, fixture))
	case restoreStartStep:
		return restoreR3JSON(t, map[string]any{"unit_ref": json.RawMessage(restoreR3Unit), "outcome": "completed", "state": "running", "observed_at": now})
	case restoreVerifyRuntimeStep:
		return restoreR3JSON(t, map[string]any{"unit_ref": json.RawMessage(restoreR3Unit), "state": "running", "observed_at": now})
	}
	t.Fatalf("no valid output for %s", stepID)
	return nil
}

// restoreR3CompleteThrough records valid outputs for every planned step
// before stepID. Once the restore call has succeeded the lifecycle override
// moves the server to restoring, as the projector would.
func restoreR3CompleteThrough(t *testing.T, fixture *restoreR3Fixture, stepID string) {
	t.Helper()
	for _, step := range fixture.operation.Steps {
		if step.StepID == stepID {
			return
		}
		restoreR3Succeed(fixture, step.StepID, restoreR3ValidOutput(t, fixture, step.StepID))
		if step.StepID == restoreApplyStep {
			fixture.server().LifecycleState = protocolstate.ServerRestoring
		}
	}
	t.Fatalf("no planned step %s", stepID)
}
