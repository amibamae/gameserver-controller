package workflow

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These tests pin which recorded evidence the restore predicates accept, one
// changed field at a time, on a restore whose every step has valid output.

// restoreR3EvidenceCase changes one recorded step and expects ok.
type restoreR3EvidenceCase struct {
	name    string
	change  func(*testing.T, *restoreR3Fixture)
	wantErr string
	ok      bool
}

func restoreR3Output(stepID string, value any, path ...string) func(*testing.T, *restoreR3Fixture) {
	return func(t *testing.T, fixture *restoreR3Fixture) {
		step := fixture.step(stepID)
		step.Output = restoreR3Patch(t, step.Output, value, path...)
	}
}

func restoreR3Input(stepID string, value any, path ...string) func(*testing.T, *restoreR3Fixture) {
	return func(t *testing.T, fixture *restoreR3Fixture) {
		step := fixture.step(stepID)
		step.Input = restoreR3Patch(t, step.Input, value, path...)
	}
}

func restoreR3Status(stepID string, status protocolstate.StepStatus) func(*testing.T, *restoreR3Fixture) {
	return func(_ *testing.T, fixture *restoreR3Fixture) { fixture.step(stepID).Status = status }
}

// restoreR3RunEvidence completes every step of a start-after-restore plan,
// applies each case and reports what predicate returns.
func restoreR3RunEvidence(t *testing.T, cases []restoreR3EvidenceCase, predicate func(*restoreR3Fixture) bool) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
			restoreR3CompleteAll(t, fixture)
			if test.change != nil {
				test.change(t, fixture)
			}
			if got := predicate(fixture); got != test.ok {
				t.Fatalf("predicate = %v; want %v", got, test.ok)
			}
		})
	}
}

func TestServerRestoreR3CompatibilityEvidence(t *testing.T) {
	check := restoreCompatibilityStep
	cases := []restoreR3EvidenceCase{
		{name: "valid", ok: true},
		{name: "not succeeded", change: restoreR3Status(check, protocolstate.StepFailed)},
		{name: "output fails schema", change: restoreR3Output(check, restoreR3Remove, "observed_at")},
		{name: "input not an object", change: func(_ *testing.T, f *restoreR3Fixture) { f.step(check).Input = json.RawMessage(`[]`) }},
		{name: "manifest for another backup", change: restoreR3Input(check, "backup-2", "source_manifest", "backup_id")},
		{name: "manifest in another environment", change: restoreR3Input(check, "production", "source_manifest", "environment_id")},
		{name: "manifest without generation", change: restoreR3Input(check, "", "source_manifest", "data_generation_id")},
		{name: "manifest without game adapter", change: restoreR3Input(check, "", "source_manifest", "game", "adapter_id")},
		{name: "manifest without game version", change: restoreR3Input(check, "", "source_manifest", "game", "version")},
		{name: "release for another adapter", change: restoreR3Input(check, "game.other", "target_release", "game_adapter_id")},
		{name: "release for another release", change: restoreR3Input(check, "paper-2", "target_release", "release_id")},
		{name: "release for another version", change: restoreR3Input(check, "1.21.5", "target_release", "game_version")},
		{name: "other required checks", change: restoreR3Input(check, []string{"world.other"}, "required_check_ids")},
		{name: "output for another backup", change: restoreR3Output(check, "backup-2", "backup_id")},
		{name: "output in another environment", change: restoreR3Output(check, "production", "environment_id")},
		{name: "output for another generation", change: restoreR3Output(check, "generation-other", "data_generation_id")},
		{name: "output from another source adapter", change: restoreR3Output(check, "game.other", "source_game_adapter_id")},
		{name: "output from another source version", change: restoreR3Output(check, "1.20", "source_game_version")},
		{name: "output to another target adapter", change: restoreR3Output(check, "game.other", "target_game_adapter_id")},
		{name: "output to another release", change: restoreR3Output(check, "paper-2", "target_release_id")},
		{name: "output to another version", change: restoreR3Output(check, "1.21.5", "target_game_version")},
		{name: "incompatible", change: restoreR3Output(check, "incompatible", "compatibility_status")},
		{name: "extra check result", change: restoreR3Output(check, []any{
			map[string]any{"check_id": "world.compatibility", "check_version": "1", "result": "compatible", "observed_at": restoreR3Now()},
			map[string]any{"check_id": "world.other", "check_version": "1", "result": "compatible", "observed_at": restoreR3Now()},
		}, "check_results")},
		{name: "check result incompatible", change: restoreR3Output(check, []any{
			map[string]any{"check_id": "world.compatibility", "check_version": "1", "result": "incompatible", "observed_at": restoreR3Now()},
		}, "check_results")},
		{name: "other check passed", change: restoreR3Output(check, []any{
			map[string]any{"check_id": "world.other", "check_version": "1", "result": "compatible", "observed_at": restoreR3Now()},
		}, "check_results")},
	}
	restoreR3RunEvidence(t, cases, func(f *restoreR3Fixture) bool {
		return restoreCompatible(f.operation, f.workflow.schemas, f.profiles.profile, "backup-1", "staging")
	})
}

func TestServerRestoreR3CompatibilityDuplicateCheck(t *testing.T) {
	fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
	restoreR3CompleteAll(t, fixture)
	profile := fixture.profiles.profile
	profile.RequiredCompatibilityCheckIDs = []string{"world.compatibility", "world.other"}
	step := fixture.step(restoreCompatibilityStep)
	step.Input = restoreR3Patch(t, step.Input, profile.RequiredCompatibilityCheckIDs, "required_check_ids")
	result := map[string]any{"check_id": "world.compatibility", "check_version": "1", "result": "compatible", "observed_at": restoreR3Now()}
	step.Output = restoreR3Patch(t, step.Output, []any{result, result}, "check_results")
	if restoreCompatible(fixture.operation, fixture.workflow.schemas, profile, "backup-1", "staging") {
		t.Fatal("a repeated compatibility check was accepted in place of a required one")
	}
	other := map[string]any{"check_id": "world.other", "check_version": "1", "result": "compatible", "observed_at": restoreR3Now()}
	step.Output = restoreR3Patch(t, step.Output, []any{result, other}, "check_results")
	if !restoreCompatible(fixture.operation, fixture.workflow.schemas, profile, "backup-1", "staging") {
		t.Fatal("two distinct compatible checks were rejected")
	}
}

func TestServerRestoreR3ApplyEvidence(t *testing.T) {
	apply := restoreApplyStep
	stale := "2026-01-01T00:00:00Z"
	cases := []restoreR3EvidenceCase{
		{name: "valid", ok: true},
		{name: "not succeeded", change: restoreR3Status(apply, protocolstate.StepOutcomeUnknown)},
		{name: "output fails schema", change: restoreR3Output(apply, restoreR3Remove, "target")},
		{name: "another backup", change: restoreR3Output(apply, "backup-2", "backup_id")},
		{name: "another environment", change: restoreR3Output(apply, "production", "environment_id")},
		{name: "rejected", change: restoreR3Output(apply, "rejected", "outcome")},
		{name: "another target generation", change: restoreR3Output(apply, "generation-other", "target_data_generation_id")},
		{name: "planned target is another server", change: restoreR3Input(apply, "server-2", "target", "server_id")},
		{name: "output target is another server", change: restoreR3Output(apply, "server-2", "target", "server_id")},
		{name: "execution of another adapter", change: restoreR3Output(apply, "game.other", "restore_execution_ref", "adapter_id")},
		{name: "observation stale", change: restoreR3Output(apply, stale, "observed_at")},
		{name: "validation check stale", change: restoreR3Output(apply, []any{map[string]any{"check_id": "world.loaded", "result": "pass", "observed_at": stale}}, "validation_checks")},
		{name: "source generation differs from backup", change: restoreR3Output(apply, "generation-other", "source_data_generation_id")},
		{name: "planned source backup not an object", change: restoreR3Input(apply, "backup-1", "source_backup")},
	}
	restoreR3RunEvidence(t, cases, func(f *restoreR3Fixture) bool {
		return restoreApplySucceeded(f.operation, f.workflow.schemas, "backup-1", *f.server())
	})
}

func TestServerRestoreR3InspectEvidence(t *testing.T) {
	inspect := restoreInspectStep
	stale := "2026-01-01T00:00:00Z"
	cases := []restoreR3EvidenceCase{
		{name: "valid", ok: true},
		{name: "not succeeded", change: restoreR3Status(inspect, protocolstate.StepFailed)},
		{name: "output fails schema", change: restoreR3Output(inspect, restoreR3Remove, "backup_id")},
		{name: "another backup", change: restoreR3Output(inspect, "backup-2", "backup_id")},
		{name: "another environment", change: restoreR3Output(inspect, "production", "environment_id")},
		{name: "rejected", change: restoreR3Output(inspect, "rejected", "outcome")},
		{name: "another target generation", change: restoreR3Output(inspect, "generation-other", "target_data_generation_id")},
		{name: "planned target is another server", change: restoreR3Input(restoreApplyStep, "server-2", "target", "server_id")},
		{name: "output target is another server", change: restoreR3Output(inspect, "server-2", "target", "server_id")},
		{name: "another execution than the restore call", change: restoreR3Output(inspect, "restore-2", "restore_execution_ref", "external_id")},
		{name: "restore call output not an object", change: func(_ *testing.T, f *restoreR3Fixture) {
			f.step(restoreApplyStep).Output = json.RawMessage(`[]`)
		}},
		{name: "source generation differs from restore call", change: restoreR3Output(inspect, "generation-other", "source_data_generation_id")},
		{name: "observation stale", change: restoreR3Output(inspect, stale, "observed_at")},
		{name: "validation check stale", change: restoreR3Output(inspect, []any{map[string]any{"check_id": "world.loaded", "result": "pass", "observed_at": stale}}, "validation_checks")},
		{name: "validation check failed", change: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3Output(inspect, "rejected", "outcome")(t, f)
			restoreR3Output(inspect, []any{map[string]any{"check_id": "world.loaded", "result": "fail", "observed_at": restoreR3Now()}}, "validation_checks")(t, f)
			restoreR3Output(inspect, "restored", "outcome")(t, f)
		}},
		{name: "required check missing", change: restoreR3Output(inspect, []any{map[string]any{"check_id": "world.other", "result": "pass", "observed_at": restoreR3Now()}}, "validation_checks")},
		{name: "required checks from resolved input", ok: true, change: func(t *testing.T, f *restoreR3Fixture) {
			step := f.step(restoreApplyStep)
			step.ResolvedInput = step.Input
			step.Input = restoreR3Patch(t, step.Input, []string{"world.other"}, "required_check_ids")
		}},
	}
	restoreR3RunEvidence(t, cases, func(f *restoreR3Fixture) bool {
		return restoreInspectionSucceeded(f.operation, f.workflow.schemas, "backup-1", *f.server())
	})
}

// restoreR3ManifestObject is a valid manifest object entry with overrides.
func restoreR3ManifestObject(t *testing.T, field string, value any, path ...string) []any {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(restoreR3Manifest(), &manifest); err != nil {
		t.Fatal(err)
	}
	object := manifest["objects"].([]any)[0]
	encoded := restoreR3JSON(t, object)
	if field != "" {
		encoded = restoreR3Patch(t, encoded, value, append([]string{field}, path...)...)
	}
	var patched any
	if err := json.Unmarshal(encoded, &patched); err != nil {
		t.Fatal(err)
	}
	return []any{patched}
}

func TestServerRestoreR3BackupValidation(t *testing.T) {
	result := func(value any, path ...string) func(*testing.T) json.RawMessage {
		return func(t *testing.T) json.RawMessage { return restoreR3Patch(t, restoreR3BackupResult(), value, path...) }
	}
	object := func(field string, value any, path ...string) func(*testing.T) json.RawMessage {
		return func(t *testing.T) json.RawMessage {
			return restoreR3Patch(t, restoreR3BackupResult(), restoreR3ManifestObject(t, field, value, path...), "operation_data", "manifest", "objects")
		}
	}
	const (
		notVerified    = "selected backup is not an integrity-verified server.backup result"
		resultUnbound  = "selected backup result does not bind the exact backup, environment, and source generation"
		manifestUnbind = "selected backup manifest does not bind the exact server, environment, backup, and source generation"
		objectInvalid  = "selected backup has an invalid or cross-environment object reference"
	)
	cases := []struct {
		name   string
		result func(*testing.T) json.RawMessage
		want   string
	}{
		{"valid", nil, ""},
		{"fails schema", func(*testing.T) json.RawMessage { return json.RawMessage(`{}`) }, notVerified},
		{"partial", result(true, "partial"), resultUnbound},
		{"failed steps", result([]any{map[string]any{"step_id": "x", "step_type": "y", "error": map[string]any{}}}, "failed_steps"), notVerified},
		{"another backup", result("backup-2", "operation_data", "backup_id"), resultUnbound},
		{"another environment", result("production", "operation_data", "environment_id"), resultUnbound},
		{"invalid verification time", result("yesterday", "operation_data", "verified_at"), notVerified},
		{"manifest for another backup", result("backup-2", "operation_data", "manifest", "backup_id"), manifestUnbind},
		{"manifest for another server", result("server-2", "operation_data", "manifest", "server_id"), manifestUnbind},
		{"manifest in another environment", result("production", "operation_data", "manifest", "environment_id"), manifestUnbind},
		{"manifest for another generation", result("generation-other", "operation_data", "manifest", "data_generation_id"), manifestUnbind},
		// The result schema validates the embedded manifest before deeper checks run.
		{"manifest fails schema", result(restoreR3Remove, "operation_data", "manifest", "game"), notVerified},
		{"no objects", result([]any{}, "operation_data", "manifest", "objects"), notVerified},
		{"object of another storage adapter", object("object_ref", "storage.other", "adapter_id"), objectInvalid},
		{"object of another storage profile", object("object_ref", "storage-2", "storage_profile_id"), objectInvalid},
		{"object in another environment", object("object_ref", "production", "environment_id"), objectInvalid},
		{"object ref fails schema", object("object_ref", restoreR3Remove, "key"), notVerified},
		{"negative size", object("size_bytes", -1), notVerified},
		{"another checksum algorithm", object("checksum_algorithm", "md5"), notVerified},
		{"short checksum", object("checksum", "abc"), notVerified},
		{"unversioned object", object("object_ref", restoreR3Remove, "version_id"), "selected backup object lacks an immutable version reference"},
		{"checksum not hexadecimal", object("checksum", strings.Repeat("z", 64)), notVerified},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRestoreR3Fixture(t, restoreR3Replace)
			verified := VerifiedProvisionBackup{Result: restoreR3BackupResult(), IntegrityVerified: true}
			if test.result != nil {
				verified.Result = test.result(t)
			}
			backupResult, manifestRaw, manifest, err := validateServerRestoreBackup(fixture.workflow.schemas, *fixture.server(), "backup-1", fixture.profiles.profile, verified)
			if got := restoreR3ErrText(err); got != test.want {
				t.Fatalf("validateServerRestoreBackup = %q; want %q", got, test.want)
			}
			restoreR3CheckValidatedBackup(t, test.want == "", verified.Result, backupResult, manifestRaw, len(manifest.Objects))
		})
	}
}

// restoreR3CheckValidatedBackup checks a validation returns copies of the
// verified result and its manifest only on success.
func restoreR3CheckValidatedBackup(t *testing.T, valid bool, verified, backupResult, manifestRaw json.RawMessage, objects int) {
	t.Helper()
	if !valid {
		if backupResult != nil || manifestRaw != nil {
			t.Fatalf("rejected backup returned result %s and manifest %s", backupResult, manifestRaw)
		}
		return
	}
	if !bytes.Equal(backupResult, verified) || !equalJSON(manifestRaw, restoreR3Manifest()) || objects != 1 {
		t.Fatalf("validated backup = %s, manifest %s, %d objects", backupResult, manifestRaw, objects)
	}
	if len(verified) > 0 && &backupResult[0] == &verified[0] {
		t.Fatal("validated backup result aliases the resolver's bytes")
	}
}

func TestServerRestoreR3BackupValidationUnverified(t *testing.T) {
	fixture := newRestoreR3Fixture(t, restoreR3Replace)
	verified := VerifiedProvisionBackup{Result: restoreR3BackupResult()}
	_, _, _, err := validateServerRestoreBackup(fixture.workflow.schemas, *fixture.server(), "backup-1", fixture.profiles.profile, verified)
	if got := restoreR3ErrText(err); got != "selected backup is not an integrity-verified server.backup result" {
		t.Fatalf("unverified backup = %q", got)
	}
	verified.IntegrityVerified = true
	profile := fixture.profiles.profile
	profile.StorageVersioningEnabled = false
	verified.Result = restoreR3Patch(t, restoreR3BackupResult(), restoreR3ManifestObject(t, "object_ref", restoreR3Remove, "version_id"), "operation_data", "manifest", "objects")
	if _, _, _, err := validateServerRestoreBackup(fixture.workflow.schemas, *fixture.server(), "backup-1", profile, verified); err != nil {
		t.Fatalf("unversioned object without versioning = %v", err)
	}
}

func TestServerRestoreR3ProfileValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*ServerRestoreProfile)
		want   string
	}{
		{"valid", func(*ServerRestoreProfile) {}, ""},
		{"no profile ID", func(p *ServerRestoreProfile) { p.ProfileID = "" }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"other game adapter", func(p *ServerRestoreProfile) { p.GameAdapterID = "game.other" }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"no storage adapter", func(p *ServerRestoreProfile) { p.StorageAdapterID = "" }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"release not an object", func(p *ServerRestoreProfile) { p.GameReleaseDescription = json.RawMessage(`[]`) }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"no restore checks", func(p *ServerRestoreProfile) { p.RequiredRestoreCheckIDs = nil }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"no unit", func(p *ServerRestoreProfile) { p.UnitRef = nil }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"no readiness set", func(p *ServerRestoreProfile) { p.ReadinessCheckSetID = "" },
			"invalid trusted restore runtime/readiness profile: trusted start profile must select a release and bounded readiness check set"},
		{"too many checks", func(p *ServerRestoreProfile) { p.RequiredCompatibilityCheckIDs = make([]string, 65) }, "trusted restore check sets exceed protocol limits"},
		{"duplicate compatibility check", func(p *ServerRestoreProfile) { p.RequiredCompatibilityCheckIDs = []string{"a", "a"} }, `trusted restore check ID "a" is duplicated`},
		{"empty restore check", func(p *ServerRestoreProfile) { p.RequiredRestoreCheckIDs = []string{"a", ""} }, "trusted restore check IDs must not be empty"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			profile := restoreR3Profile()
			test.change(&profile)
			if got := restoreR3ErrText(validateServerRestoreProfile(restoreR3Server(), profile)); got != test.want {
				t.Fatalf("validateServerRestoreProfile = %q; want %q", got, test.want)
			}
		})
	}
}
