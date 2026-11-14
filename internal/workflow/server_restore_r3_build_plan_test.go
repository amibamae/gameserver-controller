package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var updateRestoreR3PlanGolden = flag.Bool("update-restore-r3-plan-golden", false, "rewrite testdata/server_restore_plans.golden.json")

func restoreR3Plan(t *testing.T, parameters string, prepare func(*restoreR3Fixture)) ([]store.OperationStep, error) {
	t.Helper()
	fixture := newRestoreR3Fixture(t, parameters)
	if prepare != nil {
		prepare(fixture)
	}
	return fixture.workflow.BuildPlan(context.Background(), fixture.operation)
}

func restoreR3KnownEmpty(fixture *restoreR3Fixture) {
	fixture.profiles.profile.TargetDataKnownEmpty = true
}

// TestServerRestoreR3BuildPlanSnapshot pins every planned step (ID,
// capability, bindings and exact input bytes) for each starting state the
// plan distinguishes.
func TestServerRestoreR3BuildPlanSnapshot(t *testing.T) {
	cases := []struct {
		name       string
		parameters string
		prepare    func(*restoreR3Fixture)
	}{
		{name: "stopped replace", parameters: restoreR3Replace},
		{name: "stopped replace and start", parameters: restoreR3ReplaceStart},
		{name: "failed replace", parameters: restoreR3Replace, prepare: func(f *restoreR3Fixture) { f.server().LifecycleState = protocolstate.ServerFailed }},
		{name: "known empty without replace", parameters: `{"backup_id":"backup-1"}`, prepare: restoreR3KnownEmpty},
		{name: "known empty with start", parameters: `{"backup_id":"backup-1","start_after_restore":true}`, prepare: restoreR3KnownEmpty},
		{name: "exact target instance pinned", parameters: `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","target_instance_ref":` + restoreR3Instance + `}`,
			prepare: func(f *restoreR3Fixture) { f.operation.Target.InstanceRef = json.RawMessage(restoreR3Instance) }},
	}
	plans := map[string][]store.OperationStep{}
	for _, test := range cases {
		steps, err := restoreR3Plan(t, test.parameters, test.prepare)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		plans[test.name] = steps
	}
	got, err := json.MarshalIndent(plans, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "server_restore_plans.golden.json")
	if *updateRestoreR3PlanGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("planned steps differ from %s; rerun with -update-restore-r3-plan-golden only for an intended change", path)
	}
}

// TestServerRestoreR3BuildPlanRefusals pins the exact error for each refusal.
func TestServerRestoreR3BuildPlanRefusals(t *testing.T) {
	dropCapability := func(adapterID, name string) func(*restoreR3Fixture) {
		return func(f *restoreR3Fixture) {
			descriptor := f.adapters.descriptors[adapterID]
			kept := []adapter.Capability{}
			for _, capability := range descriptor.Capabilities {
				if capability.Name != name {
					kept = append(kept, capability)
				}
			}
			descriptor.Capabilities = kept
			f.adapters.descriptors[adapterID] = descriptor
		}
	}
	cases := []struct {
		name       string
		parameters string
		prepare    func(*restoreR3Fixture)
		want       string
	}{
		{"wrong operation type", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.OperationType = "server.start" }, "server.restore operation and loaded protocol version are required"},
		{"wrong protocol", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.ProtocolVersion = "0.0.1" }, "server.restore operation and loaded protocol version are required"},
		{"request snapshot type", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.RequestSnapshot.OperationType = "server.start" }, "server.restore operation and loaded protocol version are required"},
		{"no server id", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.Target.ServerID = "" }, "server.restore operation and loaded protocol version are required"},
		{"no environment id", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.Target.EnvironmentID = "" }, "server.restore operation and loaded protocol version are required"},
		{"parameters not an object", `[]`, nil, "server.restore operation and loaded protocol version are required"},
		{"no backup id", `{"replace_existing_data":true}`, nil, "decode server.restore parameters"},
		{"mistyped parameters", `{"backup_id":1}`, nil, "decode server.restore parameters"},
		{"server missing", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.Target.ServerID = "other" }, "load logical server for restore plan: " + store.ErrNotFound.Error()},
		{"ready server", restoreR3Replace, func(f *restoreR3Fixture) { f.server().LifecycleState = protocolstate.ServerReady }, "server.restore requires a stopped or failed logical server with a managed instance"},
		{"no instance", restoreR3Replace, func(f *restoreR3Fixture) { f.server().InstanceRef = nil }, "server.restore requires a stopped or failed logical server with a managed instance"},
		{"network profile", restoreR3Replace, func(f *restoreR3Fixture) { f.server().NetworkProfile = "proxy" }, "server.restore cannot mutate a network-routed server until proxy isolation is integrated"},
		{"operation target mismatch", restoreR3Replace, func(f *restoreR3Fixture) { f.operation.Target.InstanceRef = json.RawMessage(restoreR3OtherInstance) }, store.ErrLogicalServerTargetMismatch.Error()},
		{"requested target mismatch", `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","target_instance_ref":` + restoreR3OtherInstance + `}`, nil, store.ErrLogicalServerTargetMismatch.Error()},
		{"profile unavailable", restoreR3Replace, func(f *restoreR3Fixture) { f.profiles.err = errors.New("no profile") }, "resolve trusted server restore profile: no profile"},
		{"profile cross storage", restoreR3Replace, func(f *restoreR3Fixture) { f.profiles.profile.StorageProfileID = "storage-2" }, "trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies"},
		{"profile runtime unit", restoreR3Replace, func(f *restoreR3Fixture) {
			f.profiles.profile.UnitRef = json.RawMessage(`{"adapter_id":"execution.other"}`)
		},
			"invalid trusted restore runtime/readiness profile: trusted start profile unit reference does not bind the configured execution adapter and environment"},
		{"profile duplicate check", restoreR3Replace, func(f *restoreR3Fixture) {
			f.profiles.profile.RequiredRestoreCheckIDs = []string{"world.loaded", "world.loaded"}
		}, `trusted restore check ID "world.loaded" is duplicated`},
		{"profile empty check", restoreR3Replace, func(f *restoreR3Fixture) { f.profiles.profile.RequiredCompatibilityCheckIDs = []string{""} }, "trusted restore check IDs must not be empty"},
		{"overwrite not confirmed", `{"backup_id":"backup-1"}`, nil, "restore target is not confirmed empty; replace_existing_data=true and explicit approval are required"},
		{"unknown data not confirmed", `{"backup_id":"backup-1"}`, func(f *restoreR3Fixture) {
			restoreR3KnownEmpty(f)
			f.server().DataGenerationStatus = "unknown"
		}, "restore target is not confirmed empty; replace_existing_data=true and explicit approval are required"},
		{"release fails schema", restoreR3Replace, func(f *restoreR3Fixture) { f.profiles.profile.GameReleaseDescription = json.RawMessage(`{}`) }, "trusted target game release metadata failed schema validation"},
		{"release version differs", restoreR3Replace, func(f *restoreR3Fixture) { f.profiles.profile.GameVersion = "1.21.5" }, "trusted target release metadata does not match the configured game adapter, release, and version"},
		{"backup unavailable", restoreR3Replace, func(f *restoreR3Fixture) { f.backups.err = errors.New("gone") }, "resolve verified restore backup: gone"},
		{"backup unverified", restoreR3Replace, func(f *restoreR3Fixture) { f.backups.backup.IntegrityVerified = false }, "selected backup is not an integrity-verified server.backup result"},
		{"replace without approval", `{"backup_id":"backup-1","replace_existing_data":true}`, nil, "replace_existing_data requires an approval_id"},
		{"start without readiness declaration", restoreR3ReplaceStart, dropCapability("game.paper", "game.readiness.check"), "restore start policy lacks the declared exact readiness check set"},
		{"start without game adapter", restoreR3ReplaceStart, func(f *restoreR3Fixture) { delete(f.adapters.descriptors, "game.paper") }, "restore start policy lacks the declared exact readiness check set"},
		{"hosting adapter missing", restoreR3Replace, func(f *restoreR3Fixture) { delete(f.adapters.descriptors, "hosting.fake") },
			`resolve adapter "hosting.fake" for hosting.instance.inspect: ` + adapter.ErrAdapterNotFound.Error()},
		{"restore inspect capability missing", restoreR3Replace, dropCapability("game.paper", "game.restore.inspect"), `adapter "game.paper" does not advertise required capability game.restore.inspect`},
		{"start capability missing", restoreR3ReplaceStart, dropCapability("execution.fake", "execution.unit.start"), `adapter "execution.fake" does not advertise required capability execution.unit.start`},
		{"start capability missing without start", restoreR3Replace, dropCapability("execution.fake", "execution.unit.start"), ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			steps, err := restoreR3Plan(t, test.parameters, test.prepare)
			if restoreR3ErrText(err) != test.want || (test.want != "" && steps != nil) {
				t.Fatalf("BuildPlan = %d steps, %q; want %q", len(steps), restoreR3ErrText(err), test.want)
			}
		})
	}
	fixture := newRestoreR3Fixture(t, restoreR3Replace)
	var missing context.Context
	if _, err := fixture.workflow.BuildPlan(missing, fixture.operation); restoreR3ErrText(err) != "initialized server.restore workflow and context are required" {
		t.Fatalf("nil context = %v", err)
	}
	var nilWorkflow *ServerRestoreWorkflow
	if _, err := nilWorkflow.BuildPlan(context.Background(), fixture.operation); restoreR3ErrText(err) != "initialized server.restore workflow and context are required" {
		t.Fatalf("nil workflow = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.workflow.BuildPlan(cancelled, fixture.operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
}
