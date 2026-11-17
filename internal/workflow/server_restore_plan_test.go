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

func TestServerRestorePlanDefaultsToStoppedAndAddsStartOnlyOnRequest(t *testing.T) {
	f := newRestorePlanTestFixture(t)
	operation, plan := f.defaultPlan(t)
	f.verifyBackupStream(t, operation, plan)
	f.explicitStartPlan(t)
	f.runStoppedRestore(t)
}

type restorePlanTestFixture struct {
	schemas             *schemaregistry.Registry
	registry            *adapter.Registry
	hosting             *fakeadapter.Scripted
	execution           *fakeadapter.Scripted
	gameScript          *fakeadapter.Scripted
	operationStore      *store.BoltStore
	workflow            *ServerRestoreWorkflow
	profile             ServerRestoreProfile
	authorizer          *countingServerRestoreAuthorizer
	version             string
	backupBytes         string
	observedAt          string
	compatibilityOutput json.RawMessage
}

func newRestorePlanTestFixture(t *testing.T) *restorePlanTestFixture {
	t.Helper()

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
	return &restorePlanTestFixture{
		schemas:        schemas,
		registry:       registry,
		hosting:        hosting,
		execution:      execution,
		gameScript:     gameScript,
		operationStore: operationStore,
		workflow:       workflow,
		profile:        profile,
		authorizer:     authorizer,
		version:        version,
		backupBytes:    backupBytes,
	}
}

func (f *restorePlanTestFixture) newOperation(operationID, parameters string) store.OperationRecord {
	return store.OperationRecord{OperationID: operationID, OperationType: "server.restore", ProtocolVersion: f.version,
		Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1",
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.restore", Parameters: json.RawMessage(parameters)}}
}

func (f *restorePlanTestFixture) defaultPlan(t *testing.T) (store.OperationRecord, []store.OperationStep) {
	t.Helper()
	replaceParameters := `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1"}`
	defaultOperation := f.newOperation("restore-plan", replaceParameters)
	plan, err := f.workflow.BuildPlan(context.Background(), defaultOperation)
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
	if err := f.workflow.CheckPreconditions(context.Background(), f.newOperation("restore-precondition", replaceParameters), plan[0]); err != nil {
		t.Fatalf("read-only provider inspection precondition failed: %v", err)
	}
	if f.authorizer.calls != 0 {
		t.Fatalf("read-only restore planning or inspection checked authorization %d times", f.authorizer.calls)
	}
	return defaultOperation, plan
}

func (f *restorePlanTestFixture) verifyBackupStream(t *testing.T, defaultOperation store.OperationRecord, plan []store.OperationStep) {
	t.Helper()
	f.observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	compatibilityOutput, err := json.Marshal(map[string]any{
		"backup_id": "backup-1", "environment_id": "staging", "data_generation_id": "generation-source",
		"source_game_adapter_id": "game.paper", "source_game_version": "1.21.4", "target_game_adapter_id": "game.paper",
		"target_release_id": "paper-1", "target_game_version": "1.21.4", "compatibility_status": "compatible", "observed_at": f.observedAt,
		"check_results": []map[string]string{{"check_id": "world.compatibility", "check_version": "1", "result": "compatible", "observed_at": f.observedAt}},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.compatibilityOutput = compatibilityOutput
	compatibilityStarted := time.Now().UTC()
	plan[1].Status = protocolstate.StepSucceeded
	plan[1].StartedAt = &compatibilityStarted
	plan[1].Output = compatibilityOutput
	defaultOperation.Steps = plan
	transfers, err := f.workflow.StreamTransfersForStep(context.Background(), defaultOperation, plan[3], time.Now().Add(time.Minute))
	if err != nil || len(transfers) != 1 {
		t.Fatalf("verified restore stream count=%d err=%v", len(transfers), err)
	}
	streamed, err := io.ReadAll(transfers[0].Source)
	if err != nil || string(streamed) != f.backupBytes {
		t.Fatalf("restore stream bytes=%q err=%v", streamed, err)
	}
	if err := transfers[0].Verify(); err != nil {
		t.Fatalf("verified restore stream did not match the manifest: %v", err)
	}
	_ = transfers[0].Source.Close()
}

func (f *restorePlanTestFixture) explicitStartPlan(t *testing.T) {
	t.Helper()
	plan, err := f.workflow.BuildPlan(context.Background(), f.newOperation("restore-explicit-start", `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","start_after_restore":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 8 || plan[5].StepID != restoreStartStep || plan[6].StepID != restoreVerifyRuntimeStep || plan[7].StepID != restoreReadinessStep {
		t.Fatalf("explicit-start restore plan has unexpected steps: %#v", plan)
	}
	if f.authorizer.calls != 0 {
		t.Fatalf("plan construction consumed authorization %d times", f.authorizer.calls)
	}

}

func (f *restorePlanTestFixture) runStoppedRestore(t *testing.T) {
	t.Helper()
	const replaceParameters = `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1"}`
	e2e := f.newOperation("restore-e2e", replaceParameters)
	e2e.IdempotencyKey = "restore-e2e-key"
	e2e.RequestFingerprint = "sha256:" + strings.Repeat("e", 64)
	logicalServer, err := f.operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	e2e.Target.InstanceRef = append(json.RawMessage(nil), logicalServer.Record.InstanceRef...)
	e2e.Steps, err = f.workflow.BuildPlan(context.Background(), e2e)
	if err != nil {
		t.Fatal(err)
	}
	accepted, _, err := f.operationStore.AcceptOperation(e2e, testProof(88), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.restore", f.workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(f.operationStore, f.registry, f.schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	f.scriptRestore(t, e2e)
	completed, err := engine.Run(context.Background(), accepted.Record.OperationID)
	if err != nil {
		f.reportRestoreRunFailure(t, accepted.Record.OperationID, err)
	}
	f.assertStoppedRestore(t, completed)
}

func (f *restorePlanTestFixture) scriptRestore(t *testing.T, e2e store.OperationRecord) {
	t.Helper()
	providerOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":"active","observed_at":%q}`, startTestInstance, f.observedAt))
	if err := f.hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, f.observedAt))
	if err := f.execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.gameScript.SetScript("game.compatibility.check", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: f.compatibilityOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	restoreRef := json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-e2e"}`)
	makeRestoreOutput := func() json.RawMessage {
		stamp := time.Now().UTC().Add(1 * time.Minute).Format(time.RFC3339Nano)
		return json.RawMessage(fmt.Sprintf(`{"backup_id":"backup-1","environment_id":"staging","source_data_generation_id":"generation-source","target":%s,"target_data_generation_id":"restore-restore-e2e","restore_execution_ref":%s,"outcome":"restored","observed_at":%q,"validation_checks":[{"check_id":"world.loaded","result":"pass","observed_at":%q}]}`,
			stepField(e2e.Steps[3].Input, "target"), restoreRef, stamp, stamp))
	}
	if err := f.gameScript.SetScript("game.restore", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: makeRestoreOutput(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.gameScript.SetScript("game.restore.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: makeRestoreOutput(), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func (f *restorePlanTestFixture) reportRestoreRunFailure(t *testing.T, operationID string, err error) {
	t.Helper()

	latest, getOperationErr := f.operationStore.GetOperation(operationID)
	server, getServerErr := f.operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	stepStates := make([]string, 0, len(latest.Record.Steps))
	for _, step := range latest.Record.Steps {
		stepStates = append(stepStates, step.StepID+"="+string(step.Status))
	}
	projected, transition, projectionErr := f.workflow.ProjectLifecycle(latest.Record, server)
	t.Fatalf("engine run: %v; operation=%s steps=%v; server=%s data_generation=%s/%s; projected=%s generation=%s/%s transition=%t projection_error=%v; load_errors=%v/%v", err, latest.Record.Status, stepStates,
		server.Record.LifecycleState, server.Record.DataGenerationID, server.Record.DataGenerationStatus, projected.LifecycleState, projected.DataGenerationID, projected.DataGenerationStatus, transition, projectionErr, getOperationErr, getServerErr)
}

func (f *restorePlanTestFixture) reportIncompleteRestore(t *testing.T, completed store.StoredOperation) {
	t.Helper()

	stepStates := make([]string, 0, len(completed.Record.Steps))
	for _, step := range completed.Record.Steps {
		stepStates = append(stepStates, step.StepID+"="+string(step.Status))
	}
	var preconditionErr error
	var currentServer store.StoredLogicalServer
	if current, getErr := f.operationStore.GetLogicalServer(startTestEnvID, startTestServerID); getErr == nil {
		currentServer = current
	}
	compatStep := operationStep(completed.Record, restoreCompatibilityStep)
	if len(completed.Record.Steps) > 3 {
		preconditionErr = f.workflow.CheckPreconditions(context.Background(), completed.Record, completed.Record.Steps[3])
	}
	t.Fatalf("restore operation status=%s step_states=%v lifecycle=%s compatibility_ok=%t compatibility_fresh=%t failed_step_precondition=%v error=%s", completed.Record.Status, stepStates, currentServer.Record.LifecycleState,
		restoreCompatible(completed.Record, f.schemas, f.profile, "backup-1", startTestEnvID), observationIsFreshForStep(stepFieldString(compatStep.Output, "observed_at"), compatStep), preconditionErr, completed.Record.Error)
}

func (f *restorePlanTestFixture) assertStoppedRestore(t *testing.T, completed store.StoredOperation) {
	t.Helper()
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 5 {
		f.reportIncompleteRestore(t, completed)
	}
	for _, step := range completed.Record.Steps {
		if step.Status != protocolstate.StepSucceeded || step.Attempt != 1 {
			t.Fatalf("restore step %q status/attempt=%s/%d", step.StepID, step.Status, step.Attempt)
		}
	}
	f.assertStoppedServerProjection(t)
}

func (f *restorePlanTestFixture) assertStoppedServerProjection(t *testing.T) {
	t.Helper()
	finalServer, err := f.operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	stopped := finalServer.Record.LifecycleState == protocolstate.ServerStopped && finalServer.Record.DesiredState == "stopped"
	knownGeneration := finalServer.Record.DataGenerationID == "restore-restore-e2e" && finalServer.Record.DataGenerationStatus == "known"
	if err != nil || !stopped || !knownGeneration {
		t.Fatalf("restored server projection=%#v err=%v", finalServer.Record, err)
	}
}
