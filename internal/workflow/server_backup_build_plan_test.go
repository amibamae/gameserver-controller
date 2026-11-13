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

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var updateBackupPlanGolden = flag.Bool("update-backup-plan-golden", false, "rewrite testdata/server_backup_plans.golden.json")

// backupPlanVariant adjusts the plan fixture's server or request.
type backupPlanVariant func(w *ServerBackupWorkflow, operation *store.OperationRecord)

func withBackupServer(change func(*store.LogicalServer)) backupPlanVariant {
	return func(w *ServerBackupWorkflow, _ *store.OperationRecord) {
		repository := w.servers.(testBackupServerRepository)
		change(&repository.record.Record)
		w.servers = repository
	}
}

func withBackupParameters(raw string) backupPlanVariant {
	return func(_ *ServerBackupWorkflow, operation *store.OperationRecord) {
		operation.RequestSnapshot.Parameters = json.RawMessage(raw)
	}
}

func planBackup(t *testing.T, resume bool, variants ...backupPlanVariant) ([]store.OperationStep, error) {
	t.Helper()
	workflow, operation := newBackupPlanFixture(t, resume)
	for _, variant := range variants {
		variant(workflow, &operation)
	}
	return workflow.BuildPlan(context.Background(), operation)
}

// TestServerBackupBuildPlanSnapshot pins every planned step (ID, capability
// and exact input bytes) for each starting state the plan distinguishes.
func TestServerBackupBuildPlanSnapshot(t *testing.T) {
	degraded := withBackupServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerDegraded })
	cases := []struct {
		name     string
		resume   bool
		variants []backupPlanVariant
	}{
		{name: "ready resume", resume: true},
		{name: "ready no resume", resume: false},
		{name: "ready resume by default", variants: []backupPlanVariant{withBackupParameters(`{"backup_profile":"profile-1"}`)}},
		{name: "degraded default", variants: []backupPlanVariant{degraded, withBackupParameters(`{"backup_profile":"profile-1"}`)}},
		{name: "degraded explicit resume", resume: true, variants: []backupPlanVariant{degraded}},
		{name: "stopped asked to resume", resume: true, variants: []backupPlanVariant{withBackupServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerStopped })}},
		{name: "expected instance matches", resume: false, variants: []backupPlanVariant{withBackupParameters(`{"backup_profile":"profile-1","resume_after_backup":false,"expected_instance_ref":{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}}`)}},
	}
	plans := map[string][]store.OperationStep{}
	for _, test := range cases {
		steps, err := planBackup(t, test.resume, test.variants...)
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
	path := filepath.Join("testdata", "server_backup_plans.golden.json")
	if *updateBackupPlanGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("planned steps differ from %s; rerun with -update-backup-plan-golden only for an intended change", path)
	}
}

// failingBackupProfiles is a profile resolver that refuses every profile.
type failingBackupProfiles struct{}

func (failingBackupProfiles) ResolveServerBackup(context.Context, store.LogicalServer, string) (ServerBackupProfile, error) {
	return ServerBackupProfile{}, errors.New("unknown profile")
}

// TestServerBackupBuildPlanRefusals pins the exact error for each refusal.
func TestServerBackupBuildPlanRefusals(t *testing.T) {
	otherInstance := `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-2"}`
	cases := map[string][]backupPlanVariant{
		"wrong operation type": {func(_ *ServerBackupWorkflow, operation *store.OperationRecord) {
			operation.OperationType = "server.stop"
		}},
		"wrong protocol":           {func(_ *ServerBackupWorkflow, operation *store.OperationRecord) { operation.ProtocolVersion = "0.0.1" }},
		"parameters not an object": {withBackupParameters(`[]`)},
		"no backup profile":        {withBackupParameters(`{"resume_after_backup":true}`)},
		"server missing":           {func(_ *ServerBackupWorkflow, operation *store.OperationRecord) { operation.Target.ServerID = "other" }},
		"provisioning server":      {withBackupServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerProvisioning })},
		"network profile":          {withBackupServer(func(server *store.LogicalServer) { server.NetworkProfile = "proxy" })},
		"unknown data generation":  {withBackupServer(func(server *store.LogicalServer) { server.DataGenerationStatus = "unknown" })},
		"no instance":              {withBackupServer(func(server *store.LogicalServer) { server.InstanceRef = nil })},
		"target instance mismatch": {func(_ *ServerBackupWorkflow, operation *store.OperationRecord) {
			operation.Target.InstanceRef = json.RawMessage(otherInstance)
		}},
		"expected instance mismatch":       {withBackupParameters(`{"backup_profile":"profile-1","expected_instance_ref":` + otherInstance + `}`)},
		"profile not resolved":             {func(w *ServerBackupWorkflow, _ *store.OperationRecord) { w.profiles = failingBackupProfiles{} }},
		"game adapter missing":             {withBackupServer(func(server *store.LogicalServer) { server.GameAdapterID = "game.missing" })},
		"game adapter wrong category":      {withBackupServer(func(server *store.LogicalServer) { server.GameAdapterID = "storage.fake" })},
		"execution adapter not in profile": {withBackupServer(func(server *store.LogicalServer) { server.ExecutionAdapterID = "hosting.fake" })},
		"storage capability missing": {func(w *ServerBackupWorkflow, _ *store.OperationRecord) {
			runtime := w.adapters.(backupDescriptorRuntime)
			storage := runtime.descriptors["storage.fake"]
			storage.Capabilities = storage.Capabilities[:1]
			runtime.descriptors["storage.fake"] = storage
		}},
	}
	want := map[string]string{
		"execution adapter not in profile": "trusted backup profile unit reference does not bind the configured execution adapter and environment",
		"storage capability missing":       `adapter "storage.fake" does not advertise required capability storage.object.get`,
		"expected instance mismatch":       "expected_instance_ref does not match the current managed instance",
		"game adapter missing":             "configured game adapter is unavailable or has the wrong category",
		"game adapter wrong category":      "configured game adapter is unavailable or has the wrong category",
		"network profile":                  "server.backup cannot stop a network-routed server until proxy drain is integrated",
		"no backup profile":                "decode server.backup parameters",
		"no instance":                      "server.backup requires an exact managed instance and known data generation",
		"parameters not an object":         "server.backup operation and loaded protocol version are required",
		"profile not resolved":             "resolve trusted server backup profile: unknown profile",
		"provisioning server":              "server.backup requires a ready, degraded, or stopped logical server",
		"server missing":                   "load logical server for backup plan: operation not found",
		"target instance mismatch":         store.ErrLogicalServerTargetMismatch.Error(),
		"unknown data generation":          "server.backup requires an exact managed instance and known data generation",
		"wrong operation type":             "server.backup operation and loaded protocol version are required",
		"wrong protocol":                   "server.backup operation and loaded protocol version are required",
	}
	if len(want) != len(cases) {
		t.Fatalf("%d cases but %d expectations", len(cases), len(want))
	}
	for name, variants := range cases {
		t.Run(name, func(t *testing.T) {
			steps, err := planBackup(t, true, variants...)
			if errText(err) != want[name] || steps != nil {
				t.Fatalf("BuildPlan = %d steps, %q; want %q", len(steps), errText(err), want[name])
			}
		})
	}
	workflow, operation := newBackupPlanFixture(t, true)
	var missing context.Context
	if _, err := workflow.BuildPlan(missing, operation); errText(err) != "initialized backup workflow and context are required" {
		t.Fatalf("nil context = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := workflow.BuildPlan(cancelled, operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
