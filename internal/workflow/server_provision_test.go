package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	provisionTestServer = "provision-test"
	provisionTestEnv    = "staging"
	provisionTestUnit   = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper-provision-test"}`
)

type staticProvisionProfileResolver struct{ profile ServerProvisionProfile }

func (r *staticProvisionProfileResolver) ResolveServerProvision(context.Context, store.LogicalServer, json.RawMessage) (ServerProvisionProfile, error) {
	return r.profile, nil
}

type staticProvisionBackupResolver struct {
	result json.RawMessage
	data   string
	object json.RawMessage
}

func (r *staticProvisionBackupResolver) ResolveServerProvisionBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error) {
	return VerifiedProvisionBackup{Result: append(json.RawMessage(nil), r.result...), IntegrityVerified: true}, nil
}

func (r *staticProvisionBackupResolver) OpenServerProvisionBackupObject(_ context.Context, _ store.LogicalServer, _ string, object json.RawMessage) (io.ReadCloser, error) {
	if !equalJSON(object, r.object) {
		return nil, errors.New("unexpected object reference")
	}
	return io.NopCloser(strings.NewReader(r.data)), nil
}

type provisionPlanFixture struct {
	store     *store.BoltStore
	schemas   *schemaregistry.Registry
	registry  *adapter.Registry
	workflow  *ServerProvisionWorkflow
	operation store.OperationRecord
	profile   ServerProvisionProfile
	request   json.RawMessage
	hosting   *fakeadapter.Scripted
	execution *fakeadapter.Scripted
	game      *fakeadapter.Scripted
}

func newProvisionPlanFixture(t *testing.T) provisionPlanFixture {
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
	checks := []ReadinessRequirement{{CheckID: "paper.startup-complete", CheckVersion: "1"}, {CheckID: "paper.world-loaded", CheckVersion: "2"}}
	checkDeclarations := []any{}
	for _, check := range checks {
		checkDeclarations = append(checkDeclarations, map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion})
	}
	cap := func(name, input, output string, mutating bool) adapter.Capability {
		return adapter.Capability{Name: name, Version: "1", Mutating: mutating, InputSchemaURI: "urn:gameserver-controller:" + version + ":" + input, OutputSchemaURI: "urn:gameserver-controller:" + version + ":" + output, RequiredPermissions: []string{}}
	}
	hosting := fakeadapter.New(adapter.Descriptor{AdapterID: "hosting.fake", Category: adapter.CategoryHosting, DisplayName: "fake hosting", ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{}, Capabilities: []adapter.Capability{
		cap("hosting.instance.create", "hosting:instance-create-input", "hosting:instance-create-output", true),
		cap("hosting.instance.wait", "hosting:instance-wait-input", "hosting:instance-wait-output", false),
		cap("hosting.instance.find_by_resource_key", "hosting:instance-find-by-resource-key-input", "hosting:instance-find-by-resource-key-output", false),
		cap("hosting.bootstrap.deliver", "hosting:bootstrap-deliver-input", "hosting:bootstrap-execution-output", true),
		cap("hosting.bootstrap.inspect", "hosting:bootstrap-inspect-input", "hosting:bootstrap-execution-output", false),
	}})
	execution := fakeadapter.New(adapter.Descriptor{AdapterID: "execution.fake", Category: adapter.CategoryExecution, DisplayName: "fake execution", ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{}, Capabilities: []adapter.Capability{
		cap("execution.unit.create", "execution:systemd-unit-create-input", "execution:systemd-unit-create-output", true),
		cap("execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true),
		cap("execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false),
	}})
	game := fakeadapter.New(adapter.Descriptor{AdapterID: "game.paper", Category: adapter.CategoryGame, DisplayName: "fake Paper", ImplementationVersion: "test-1", ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{}, Capabilities: []adapter.Capability{
		cap("game.install", "game:install-input", "game:install-output", true),
		cap("game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false),
		{Name: "game.restore", Version: "1", Mutating: true, InputSchemaURI: "urn:gameserver-controller:" + version + ":game:restore-input", OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:restore-output", RequiredPermissions: []string{}, Constraints: map[string]any{"stream_transfer_direction": "controller_to_adapter"}},
		cap("game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false),
		{Name: "game.readiness.check", Version: "1", Mutating: false, InputSchemaURI: "urn:gameserver-controller:" + version + ":game:readiness-check-input", OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:readiness-check-output", RequiredPermissions: []string{}, Constraints: map[string]any{"readiness_check_sets": []any{map[string]any{"game_release_id": "paper-test-1", "environment_ids": []string{provisionTestEnv}, "readiness_check_set_id": "paper.test:checks-v1", "required_checks": checkDeclarations}}}},
	}, StreamTransportProfiles: []string{"local-unix-stream-v1"}})
	for _, fake := range []*fakeadapter.Scripted{hosting, execution, game} {
		if err := registry.Register(fake); err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	define := store.OperationRecord{OperationID: "provision-test-define", OperationType: "server.define", ProtocolVersion: version, Target: store.OperationTarget{ServerID: provisionTestServer, EnvironmentID: provisionTestEnv}, ActorID: "operator-1", IdempotencyKey: "define-key", RequestFingerprint: "sha256:" + strings.Repeat("1", 64), RequestSnapshot: store.RequestSnapshot{OperationType: "server.define", Parameters: json.RawMessage(`{"server_id":"provision-test","environment_id":"staging","display_name":"Provision Test","hosting_profile":"staging-host","storage_profile":"staging-storage","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.fake","profile":"paper-test"}}`)}}
	if _, _, err := s.AcceptOperation(define, testProof(44), []string{store.LogicalServerLockScope(provisionTestEnv, provisionTestServer)}); err != nil {
		t.Fatal(err)
	}
	defined, err := s.GetOperation(define.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	defined.Record.Status = protocolstate.OperationRunning
	defined, err = s.UpdateOperation(defined.Revision, defined.Record)
	if err != nil {
		t.Fatal(err)
	}
	defined.Record.Status = protocolstate.OperationSucceeded
	defined.Record.Result = json.RawMessage(`{}`)
	if _, err := s.UpdateOperation(defined.Revision, defined.Record); err != nil {
		t.Fatal(err)
	}
	profile := ServerProvisionProfile{Provider: "vultr", CapacityGroup: "shared_nonproduction", CapacityLimit: 2, CapacityGroupLimit: 1, RegionID: "sea", PlanID: "vc2-1c-1gb", OS: json.RawMessage(`{"provider_os_id":"debian-13","distribution":"debian","version":"13","architecture":"x86_64"}`), Bootstrap: json.RawMessage(`{"source_uri":"https://example.invalid/bootstrap.sh","revision":"bootstrap-1","digest":"sha256:` + strings.Repeat("a", 64) + `","inputs":{}}`), GameReleaseID: "paper-test-1", ArtifactURI: "https://example.invalid/paper.jar", ArtifactChecksum: json.RawMessage(`{"algorithm":"sha256","digest":"` + strings.Repeat("b", 64) + `"}`), UnitRef: json.RawMessage(provisionTestUnit), ReadinessCheckSetID: "paper.test:checks-v1", RequiredChecks: checks}
	request := json.RawMessage(`{"server_id":"provision-test","environment_id":"staging","hosting_adapter_id":"hosting.fake","region_id":"sea","plan_id":"vc2-1c-1gb","os":{"provider_os_id":"debian-13","distribution":"debian","version":"13","architecture":"x86_64"},"bootstrap":{"source_uri":"https://example.invalid/bootstrap.sh","revision":"bootstrap-1","digest":"sha256:` + strings.Repeat("a", 64) + `","inputs":{}},"game":{"adapter_id":"game.paper","release_ref":"paper-test-1"},"execution":{"adapter_id":"execution.fake","profile":"paper-test"}}`)
	resolver := &staticProvisionProfileResolver{profile: profile}
	wf, err := NewServerProvisionWorkflow(s, s, registry, schemas, resolver, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	op := store.OperationRecord{OperationID: "provision-test-operation", OperationType: "server.provision", ProtocolVersion: version, Target: store.OperationTarget{ServerID: provisionTestServer, EnvironmentID: provisionTestEnv}, ActorID: "operator-1", IdempotencyKey: "provision-key", RequestFingerprint: "sha256:" + strings.Repeat("2", 64), RequestSnapshot: store.RequestSnapshot{OperationType: "server.provision", Parameters: request}}
	return provisionPlanFixture{store: s, schemas: schemas, registry: registry, workflow: wf, operation: op, profile: profile, request: request, hosting: hosting, execution: execution, game: game}
}

func TestServerProvisionPlanPinsOrderedStepsAndExactBindings(t *testing.T) {
	f := newProvisionPlanFixture(t)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 9 {
		t.Fatalf("step count=%d, want 9", len(steps))
	}
	want := []string{provisionCreateStep, provisionWaitStep, provisionBootstrapStep, provisionBootstrapInspect, provisionInstallStep, provisionCreateUnitStep, provisionStartUnitStep, provisionInspectUnitStep, provisionReadinessStep}
	for i, step := range steps {
		if step.StepID != want[i] || step.CapabilityVersion != "1" {
			t.Fatalf("step %d = %#v", i, step)
		}
	}
	if err := inputbinding.ValidatePlan(toBindingSteps(steps)); err != nil {
		t.Fatalf("trusted input bindings are invalid: %v", err)
	}
	if !strings.HasPrefix(provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint), "gsc-") || len(provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)) != 68 {
		t.Fatal("resource key is not stable protocol format")
	}
	if provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint) == provisionResourceKey("other", f.operation.RequestFingerprint) {
		t.Fatal("different operations share a provider resource key")
	}
	if steps[3].InputBindings[1].SourceStepID != provisionBootstrapStep || steps[3].InputBindings[1].SourcePointer != "/bootstrap_execution_ref" {
		t.Fatal("bootstrap inspection is not bound to the exact delivery execution")
	}
	if steps[7].InputBindings != nil {
		t.Fatal("unit inspection should use the profile-pinned exact unit reference")
	}
}

func TestServerProvisionCreateDispatchReservesAndClaimsCapacityOnce(t *testing.T) {
	f := newProvisionPlanFixture(t)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.workflow.BeforeAdapterDispatch(context.Background(), f.operation, steps[0]); err != nil {
		t.Fatal(err)
	}
	reservation, err := f.store.GetCapacityReservation(f.operation.OperationID)
	if err != nil || reservation.State != store.CapacityCreateOutcomeUnknown || reservation.ResourceKey != provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint) {
		t.Fatalf("capacity after create claim = %#v, %v", reservation, err)
	}
	if err := f.workflow.BeforeAdapterDispatch(context.Background(), f.operation, steps[0]); !errors.Is(err, store.ErrCapacityTransition) {
		t.Fatalf("duplicate create claim error=%v, want transition rejection", err)
	}
}

func TestServerProvisionSecretReferencesAreBootstrapScopedAndConsistent(t *testing.T) {
	f := newProvisionPlanFixture(t)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	ref := store.SecretReference{SecretRef: "systemd-credential://wasabi-backup", Purpose: "bootstrap backup credentials"}
	request, err := json.Marshal(map[string]any{
		"server_id": provisionTestServer, "environment_id": provisionTestEnv,
		"hosting_adapter_id": "hosting.fake", "secret_refs": []store.SecretReference{ref},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.operation.RequestSnapshot.Parameters = request
	refs, err := f.workflow.SecretReferencesForStep(context.Background(), f.operation, steps[2])
	if err != nil || len(refs) != 1 || refs[0].SecretRef != ref.SecretRef {
		t.Fatalf("parameter secret references=%#v, err=%v", refs, err)
	}
	refs, err = f.workflow.SecretReferencesForStep(context.Background(), f.operation, steps[0])
	if err != nil || len(refs) != 0 {
		t.Fatalf("unrelated step secret references=%#v, err=%v", refs, err)
	}
	f.operation.RequestSnapshot.SecretRefs = []store.SecretReference{{SecretRef: "systemd-credential://other", Purpose: ref.Purpose}}
	if _, err := f.workflow.SecretReferencesForStep(context.Background(), f.operation, steps[2]); err == nil {
		t.Fatal("conflicting parameter and operation snapshot secret references were accepted")
	}
}

func TestServerProvisionRestoreRequiresTrustedBackupResolver(t *testing.T) {
	f := newProvisionPlanFixture(t)
	var request map[string]any
	if err := json.Unmarshal(f.request, &request); err != nil {
		t.Fatal(err)
	}
	request["restore_backup_id"] = "backup-123"
	f.operation.RequestSnapshot.Parameters, _ = json.Marshal(request)
	if _, err := f.workflow.BuildPlan(context.Background(), f.operation); err == nil || !strings.Contains(err.Error(), "trusted backup resolver") {
		t.Fatalf("restore request error=%v", err)
	}
}

func provisionRestoreSetup(t *testing.T, f *provisionPlanFixture) (*staticProvisionBackupResolver, []store.OperationStep) {
	t.Helper()
	const objectData = "worlddata"
	digest := sha256.Sum256([]byte(objectData))
	objectRef, _ := json.Marshal(map[string]any{"adapter_id": "storage.fake", "storage_profile_id": "staging-storage",
		"environment_id": provisionTestEnv, "container": "game-backups", "key": "backups/backup-123/world.tar", "version_id": "object-version-1"})
	manifest, _ := json.Marshal(map[string]any{"manifest_version": "1", "backup_id": "backup-123", "server_id": provisionTestServer,
		"environment_id": provisionTestEnv, "data_generation_id": "gen-source", "created_at": time.Now().UTC().Format(time.RFC3339Nano),
		"source_operation_id": "backup-operation", "game": map[string]any{"adapter_id": "game.paper", "name": "Paper", "version": "1.21.4"},
		"consistency": map[string]any{"mode": "offline", "evidence": "runtime stopped"},
		"objects":     []any{map[string]any{"object_ref": json.RawMessage(objectRef), "size_bytes": len(objectData), "checksum_algorithm": "sha256", "checksum": hex.EncodeToString(digest[:])}}})
	result, _ := json.Marshal(map[string]any{"outcome_summary": "Backup verified", "operation_type": "server.backup", "affected_resources": []any{},
		"completed_steps": []any{}, "failed_steps": []any{}, "warnings": []any{}, "unresolved_conditions": []any{}, "audit_event_ids": []any{}, "partial": false,
		"operation_data": map[string]any{"backup_id": "backup-123", "environment_id": provisionTestEnv, "data_generation_id": "gen-source",
			"manifest": json.RawMessage(manifest), "verified_at": time.Now().UTC().Format(time.RFC3339Nano), "final_lifecycle_state": "stopped"}})
	release, _ := json.Marshal(map[string]any{"game_adapter_id": "game.paper", "game_name": "Paper", "release_id": "paper-test-1", "game_version": "1.21.4",
		"targets":              []any{map[string]any{"os_family": "linux", "architecture": "x86_64"}},
		"artifacts":            []any{map[string]any{"artifact_id": "server-jar", "artifact_uri": "https://example.invalid/paper.jar", "checksum": map[string]any{"algorithm": "sha256", "digest": strings.Repeat("a", 64)}}},
		"runtime_requirements": []any{map[string]any{"runtime_id": "java", "version_requirement": "21"}}, "configuration_schema_uri": "https://example.invalid/config.schema.json",
		"configuration_schema_checksum": map[string]any{"algorithm": "sha256", "digest": strings.Repeat("b", 64)},
		"data_format":                   map[string]any{"format_id": "paper-world", "version": "1"}, "required_execution_capabilities": []string{"systemd"}})
	f.profile.BackupStorageProfileID = "staging-storage"
	f.profile.BackupStorageAdapterID = "storage.fake"
	f.profile.GameReleaseDescription = release
	f.profile.RequiredCompatibilityCheckIDs = []string{"world-format.check"}
	f.profile.RequiredRestoreCheckIDs = []string{"world-load.check"}
	backupResolver := &staticProvisionBackupResolver{result: result, data: objectData, object: objectRef}
	var err error
	f.workflow, err = NewServerProvisionWorkflow(f.store, f.store, f.registry, f.schemas, &staticProvisionProfileResolver{profile: f.profile}, time.Minute, backupResolver)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(f.request, &request); err != nil {
		t.Fatal(err)
	}
	request["restore_backup_id"] = "backup-123"
	f.operation.RequestSnapshot.Parameters, _ = json.Marshal(request)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	return backupResolver, steps
}

func TestServerProvisionRestorePlanPreflightsCompatibilityAndBindsNewGeneration(t *testing.T) {
	f := newProvisionPlanFixture(t)
	_, steps := provisionRestoreSetup(t, &f)
	want := []string{provisionCompatibilityStep, provisionCreateStep, provisionWaitStep, provisionBootstrapStep,
		provisionBootstrapInspect, provisionInstallStep, provisionRestoreStep, provisionRestoreInspectStep,
		provisionCreateUnitStep, provisionStartUnitStep, provisionInspectUnitStep, provisionReadinessStep}
	if len(steps) != len(want) {
		t.Fatalf("step count=%d, want %d", len(steps), len(want))
	}
	for i := range want {
		if steps[i].StepID != want[i] {
			t.Fatalf("step %d=%q want %q", i, steps[i].StepID, want[i])
		}
	}
	if err := inputbinding.ValidatePlan(toBindingSteps(steps)); err != nil {
		t.Fatalf("restore bindings invalid: %v", err)
	}
	var restoreInput struct {
		TargetGeneration string   `json:"target_data_generation_id"`
		RequiredChecks   []string `json:"required_check_ids"`
		ObjectStreams    []struct {
			ObjectRef json.RawMessage `json:"object_ref"`
		} `json:"object_streams"`
	}
	if err := json.Unmarshal(steps[6].Input, &restoreInput); err != nil {
		t.Fatal(err)
	}
	if restoreInput.TargetGeneration != provisionDataGeneration(f.operation.OperationID) || restoreInput.TargetGeneration == "gen-source" ||
		len(restoreInput.RequiredChecks) != 1 || restoreInput.RequiredChecks[0] != "world-load.check" || len(restoreInput.ObjectStreams) != 1 {
		t.Fatalf("restore input not bound to fresh generation and exact object: %#v", restoreInput)
	}
}

func TestServerProvisionRestoreStreamsVerifyExactManifestBytes(t *testing.T) {
	f := newProvisionPlanFixture(t)
	_, steps := provisionRestoreSetup(t, &f)
	prepareProvisionRestoreDispatch(t, &f, steps)
	deadline := time.Now().Add(time.Minute)
	transfers, err := f.workflow.StreamTransfersForStep(context.Background(), f.operation, steps[6], deadline)
	if err != nil || len(transfers) != 1 {
		t.Fatalf("transfers=%d err=%v", len(transfers), err)
	}
	if transfers[0].Reference.Direction != "controller_to_adapter" || transfers[0].Reference.MaximumBytes != int64(len("worlddata")) || transfers[0].Reference.ExpiresAt.After(deadline) {
		t.Fatalf("unsafe restore transfer reference: %#v", transfers[0].Reference)
	}
	data, err := io.ReadAll(transfers[0].Source)
	if err != nil || string(data) != "worlddata" || transfers[0].Verify() != nil {
		t.Fatalf("stream bytes=%q read=%v verify=%v", data, err, transfers[0].Verify())
	}
	_ = transfers[0].Source.Close()
}

func TestServerProvisionRestoreRejectsCorruptSourceStream(t *testing.T) {
	f := newProvisionPlanFixture(t)
	resolver, steps := provisionRestoreSetup(t, &f)
	prepareProvisionRestoreDispatch(t, &f, steps)
	resolver.data = "corrupt"
	transfers, err := f.workflow.StreamTransfersForStep(context.Background(), f.operation, steps[6], time.Now().Add(time.Minute))
	if err != nil || len(transfers) != 1 {
		t.Fatalf("transfers=%d err=%v", len(transfers), err)
	}
	_, readErr := io.ReadAll(transfers[0].Source)
	if readErr != nil || transfers[0].Verify() == nil {
		t.Fatal("corrupt object bytes passed size and SHA-256 verification")
	}
	_ = transfers[0].Source.Close()
}

func prepareProvisionRestoreDispatch(t *testing.T, f *provisionPlanFixture, steps []store.OperationStep) {
	t.Helper()
	f.operation.Steps = steps
	f.operation.Steps[0].Status = protocolstate.StepSucceeded
	f.operation.Steps[0].Output = provisionCompatibleOutput(t, steps[0], time.Now().UTC())
	ref := json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-test"}`)
	f.operation.Steps[1].Status = protocolstate.StepSucceeded
	f.operation.Steps[1].Output, _ = json.Marshal(map[string]any{"instance_ref": ref, "state": "creating", "observed_at": time.Now().UTC().Format(time.RFC3339Nano)})
	bindingSteps := toBindingSteps(f.operation.Steps)
	bindingSteps[0].Status = string(protocolstate.StepSucceeded)
	bindingSteps[1].Status = string(protocolstate.StepSucceeded)
	resolved, err := inputbinding.Resolve(bindingSteps, 6)
	if err != nil {
		t.Fatal(err)
	}
	f.operation.Steps[6].ResolvedInput = resolved
	f.operation.Steps[6].Status = protocolstate.StepRunning
}

func provisionCompatibleOutput(t *testing.T, step store.OperationStep, observed time.Time) json.RawMessage {
	t.Helper()
	var input struct {
		Manifest json.RawMessage `json:"source_manifest"`
		Release  json.RawMessage `json:"target_release"`
	}
	if err := json.Unmarshal(step.Input, &input); err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		BackupID         string `json:"backup_id"`
		EnvironmentID    string `json:"environment_id"`
		DataGenerationID string `json:"data_generation_id"`
		Game             struct {
			AdapterID string `json:"adapter_id"`
			Version   string `json:"version"`
		} `json:"game"`
	}
	var release struct {
		AdapterID string `json:"game_adapter_id"`
		ReleaseID string `json:"release_id"`
		Version   string `json:"game_version"`
	}
	if json.Unmarshal(input.Manifest, &manifest) != nil || json.Unmarshal(input.Release, &release) != nil {
		t.Fatal("decode compatibility fixtures")
	}
	result, err := json.Marshal(map[string]any{"backup_id": manifest.BackupID, "environment_id": manifest.EnvironmentID,
		"data_generation_id": manifest.DataGenerationID, "source_game_adapter_id": manifest.Game.AdapterID,
		"source_game_version": manifest.Game.Version, "target_game_adapter_id": release.AdapterID,
		"target_release_id": release.ReleaseID, "target_game_version": release.Version, "compatibility_status": "compatible",
		"observed_at": observed.Format(time.RFC3339Nano), "check_results": []any{map[string]any{"check_id": "world-format.check", "check_version": "1", "result": "compatible", "observed_at": observed.Format(time.RFC3339Nano)}}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestServerProvisionRestoreRejectsCrossProfileBackupBeforeCreate(t *testing.T) {
	f := newProvisionPlanFixture(t)
	resolver, _ := provisionRestoreSetup(t, &f)
	var result map[string]any
	if err := json.Unmarshal(resolver.result, &result); err != nil {
		t.Fatal(err)
	}
	manifest := result["operation_data"].(map[string]any)["manifest"].(map[string]any)
	ref := manifest["objects"].([]any)[0].(map[string]any)["object_ref"].(map[string]any)
	ref["storage_profile_id"] = "different-profile"
	resolver.result, _ = json.Marshal(result)
	if _, err := f.workflow.BuildPlan(context.Background(), f.operation); err == nil {
		t.Fatal("backup from another storage profile was accepted")
	}
	if len(f.hosting.Calls()) != 0 {
		t.Fatal("provider was called while validating an invalid backup")
	}
}

func TestServerProvisionRestoreReconcilesOnlyExactExecution(t *testing.T) {
	f := newProvisionPlanFixture(t)
	_, steps := provisionRestoreSetup(t, &f)
	prepareProvisionRestoreDispatch(t, &f, steps)
	step := f.operation.Steps[6]
	step.Status = protocolstate.StepOutcomeUnknown
	step.Output = json.RawMessage(`{"backup_id":"backup-123","environment_id":"staging","source_data_generation_id":"gen-source","target":{"server_id":"provision-test","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-test"}},"target_data_generation_id":"` + provisionDataGeneration(f.operation.OperationID) + `","restore_execution_ref":{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"},"outcome":"outcome_unknown","observed_at":"2026-11-08T12:00:00Z"}`)
	step.ExternalOperationRef = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	output := json.RawMessage(`{"backup_id":"backup-123","environment_id":"staging","source_data_generation_id":"gen-source","target":{"server_id":"provision-test","environment_id":"staging","instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-test"}},"target_data_generation_id":"` + provisionDataGeneration(f.operation.OperationID) + `","restore_execution_ref":{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"},"outcome":"restored","observed_at":"2026-11-08T12:01:00Z","validation_checks":[{"check_id":"world-load.check","result":"pass","observed_at":"2026-11-08T12:01:00Z"}]}`)
	if err := f.game.SetScript("game.restore.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	resolution, err := f.workflow.reconcileRestore(context.Background(), f.operation, step)
	if err != nil || resolution.State != ResolutionCompleted {
		t.Fatalf("restore reconciliation=%#v err=%v", resolution, err)
	}

	var wrong map[string]any
	if err := json.Unmarshal(output, &wrong); err != nil {
		t.Fatal(err)
	}
	wrong["backup_id"] = "another-backup"
	wrongOutput, _ := json.Marshal(wrong)
	if err := f.game.SetScript("game.restore.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: wrongOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	resolution, err = f.workflow.reconcileRestore(context.Background(), f.operation, step)
	if err != nil || resolution.State != ResolutionNeedsOperator {
		t.Fatalf("mismatched restore reconciliation=%#v err=%v", resolution, err)
	}
}

func TestServerProvisionCreateReconciliationRequiresOneExactMatch(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(map[int]string{0: "none", 1: "one", 2: "duplicates"}[count], func(t *testing.T) {
			f := newProvisionPlanFixture(t)
			steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
			if err != nil {
				t.Fatal(err)
			}
			step := steps[0]
			step.Attempt = 1
			step.Status = protocolstate.StepOutcomeUnknown
			key := provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)
			matches := make([]any, 0, count)
			for i := 0; i < count; i++ {
				matches = append(matches, map[string]any{"instance_ref": map[string]any{
					"adapter_id": "hosting.fake", "environment_id": provisionTestEnv,
					"resource_kind": "instance", "external_id": map[int]string{0: "vm-a", 1: "vm-b"}[i],
				}, "state": "creating"})
			}
			output, _ := json.Marshal(map[string]any{"resource_key": key, "inventory_complete": true,
				"matches": matches, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)})
			if err := f.hosting.SetScript("hosting.instance.find_by_resource_key", fakeadapter.Reply{
				Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			resolution, err := f.workflow.reconcileCreate(context.Background(), f.operation, step)
			if err != nil {
				t.Fatal(err)
			}
			if count == 1 {
				if resolution.State != ResolutionCompleted {
					t.Fatalf("unique match resolution=%#v", resolution)
				}
				var recovered provisionCreateOutput
				if err := json.Unmarshal(resolution.Output, &recovered); err != nil || !strings.Contains(string(recovered.InstanceRef), "vm-a") {
					t.Fatalf("unique match recovery=%#v, err=%v", recovered, err)
				}
			} else if resolution.State != ResolutionNeedsOperator {
				t.Fatalf("%d matches resolution=%#v, want operator review", count, resolution)
			}
		})
	}
}

func TestServerProvisionRunsDurablePlanAndProjectsReady(t *testing.T) {
	f := newProvisionPlanFixture(t)
	var request map[string]any
	if err := json.Unmarshal(f.operation.RequestSnapshot.Parameters, &request); err != nil {
		t.Fatal(err)
	}
	request["secret_refs"] = []any{map[string]any{"secret_ref": "systemd-credential://wasabi-backup", "purpose": "bootstrap backup credentials", "permission_scope": []string{"bootstrap.deliver"}, "version_ref": "version-1"}}
	f.operation.RequestSnapshot.Parameters, _ = json.Marshal(request)
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	f.operation.Steps = steps
	accepted, _, err := f.store.AcceptOperation(f.operation, testProof(45), []string{store.LogicalServerLockScope(provisionTestEnv, provisionTestServer)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.provision", f.workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(f.store, f.registry, f.schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ReserveCapacity(store.CapacityReservation{ProviderAdapterID: "hosting.fake", OperationID: f.operation.OperationID,
		ServerID: provisionTestServer, EnvironmentID: provisionTestEnv, CapacityGroup: f.profile.CapacityGroup, Provider: f.profile.Provider,
		ResourceKey: provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)}, f.profile.CapacityLimit, f.profile.CapacityGroupLimit); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	instance := json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-provision-test"}`)
	execution := json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"bootstrap-execution","external_id":"bootstrap-provision-test"}`)
	unit := json.RawMessage(provisionTestUnit)
	checksum := f.profile.ArtifactChecksum
	bootstrapOutput, err := json.Marshal(map[string]any{"instance_ref": instance, "bootstrap_execution_ref": execution, "revision": "bootstrap-1", "digest": "sha256:" + strings.Repeat("a", 64), "state": "succeeded", "observed_at": observedAt, "started_at": observedAt, "completed_at": observedAt, "exit_code": 0, "validation_evidence": []any{map[string]any{"check_id": "bootstrap-complete", "result": "pass", "observed_at": observedAt}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.hosting.SetScript("hosting.instance.create", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{"instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-provision-test"},"state":"creating","observed_at":"` + observedAt + `"}`), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.hosting.SetScript("hosting.instance.wait", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{"instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-provision-test"},"operation_status":"succeeded","instance_state":"active","observed_at":"` + observedAt + `"}`), ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.hosting.SetScript("hosting.bootstrap.deliver", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: bootstrapOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := f.hosting.SetScript("hosting.bootstrap.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: bootstrapOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	target, _ := json.Marshal(map[string]any{"server_id": provisionTestServer, "environment_id": provisionTestEnv, "instance_ref": instance})
	install, _ := json.Marshal(map[string]any{"target": json.RawMessage(target), "release_id": "paper-test-1", "outcome": "installed", "artifact_verification": "verified", "installation_started": true, "verified_artifact_ids": []string{"paper-server"}, "observed_at": observedAt})
	if err := f.game.SetScript("game.install", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: install, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitCreate, _ := json.Marshal(map[string]any{"unit_ref": unit, "target": json.RawMessage(target), "release_id": "paper-test-1", "definition_checksum": checksum, "outcome": "created", "state": "stopped", "started_by_request": false, "persistent_data_preserved": true, "observed_at": observedAt})
	if err := f.execution.SetScript("execution.unit.create", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitCreate, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitStart, _ := json.Marshal(map[string]any{"unit_ref": unit, "outcome": "completed", "state": "running", "observed_at": observedAt})
	if err := f.execution.SetScript("execution.unit.start", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitStart, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitInspect, _ := json.Marshal(map[string]any{"unit_ref": unit, "state": "running", "observed_at": observedAt})
	if err := f.execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitInspect, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	var readinessChecks []any
	for _, check := range f.profile.RequiredChecks {
		readinessChecks = append(readinessChecks, map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion, "result": "pass", "observed_at": observedAt})
	}
	readiness, _ := json.Marshal(map[string]any{"server_id": provisionTestServer, "environment_id": provisionTestEnv, "instance_ref": instance, "game_release_id": "paper-test-1", "observed_at": observedAt, "check_results": readinessChecks})
	if err := f.game.SetScript("game.readiness.check", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: readiness, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	completed, err := engine.Run(context.Background(), accepted.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 9 {
		if _, resultErr := f.workflow.BuildSuccessResult(context.Background(), completed.Record); resultErr != nil {
			t.Logf("success result error: %v", resultErr)
		}
		var states []string
		for _, step := range completed.Record.Steps {
			states = append(states, step.StepID+":"+string(step.Status))
		}
		t.Fatalf("operation status/steps=%s/%d, error=%s, steps=%v", completed.Record.Status, len(completed.Record.Steps), completed.Record.Error, states)
	}
	server, err := f.store.GetLogicalServer(provisionTestEnv, provisionTestServer)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerReady || server.Record.DesiredState != "running" || !equalJSON(server.Record.InstanceRef, instance) || server.Record.DataGenerationStatus != "known" {
		t.Fatalf("server state=%#v, err=%v", server.Record, err)
	}
	reservation, err := f.store.GetCapacityReservation(f.operation.OperationID)
	if err != nil || reservation.State != store.CapacityActive || reservation.ReadyAt.IsZero() || reservation.ProviderInstanceID != "vm-provision-test" {
		t.Fatalf("capacity=%#v, err=%v", reservation, err)
	}
	if err := f.schemas.ValidateJSON("urn:gameserver-controller:"+f.schemas.ProtocolVersion()+":operations:server-provision-result", completed.Record.Result); err != nil {
		t.Fatalf("typed provision result invalid: %v", err)
	}
	for _, call := range f.hosting.Calls() {
		refs := call.Params.RequestContext.SecretRefs
		if call.Params.Capability == "hosting.bootstrap.deliver" {
			if len(refs) != 1 || refs[0].SecretRef != "systemd-credential://wasabi-backup" || refs[0].Purpose != "bootstrap backup credentials" {
				t.Fatalf("bootstrap delivery secret refs=%#v", refs)
			}
		} else if len(refs) != 0 {
			t.Fatalf("secret references leaked to unrelated capability %q: %#v", call.Params.Capability, refs)
		}
	}
}

func toBindingSteps(steps []store.OperationStep) []inputbinding.Step {
	out := make([]inputbinding.Step, len(steps))
	for i, step := range steps {
		out[i] = inputbinding.Step{StepID: step.StepID, Status: string(protocolstate.StepPending), Input: step.Input, Output: step.Output, Binding: step.InputBindings}
	}
	return out
}
