package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	provisionR3InstanceRef  = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-r3"}`
	provisionR3BootstrapRef = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"bootstrap-execution","external_id":"bootstrap-r3"}`
	provisionR3RestoreRef   = `{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-r3"}`
)

// provisionR3Capacity records each capacity-store call, in order.
type provisionR3Capacity struct {
	ProvisionCapacityStore
	calls  *[]string
	getErr error
}

func (c provisionR3Capacity) record(name string) { *c.calls = append(*c.calls, name) }

func (c provisionR3Capacity) ReserveCapacity(r store.CapacityReservation, limit, group int) (store.CapacityReservation, bool, error) {
	c.record("ReserveCapacity")
	return c.ProvisionCapacityStore.ReserveCapacity(r, limit, group)
}

func (c provisionR3Capacity) MarkCapacityCreateSent(id string) (store.CapacityReservation, error) {
	c.record("MarkCapacityCreateSent")
	return c.ProvisionCapacityStore.MarkCapacityCreateSent(id)
}

func (c provisionR3Capacity) ObserveCapacity(id string, o store.CapacityProviderObservation) (store.CapacityReservation, error) {
	c.record("ObserveCapacity")
	return c.ProvisionCapacityStore.ObserveCapacity(id, o)
}

func (c provisionR3Capacity) GetCapacityReservation(id string) (store.CapacityReservation, error) {
	c.record("GetCapacityReservation")
	if c.getErr != nil {
		return store.CapacityReservation{}, c.getErr
	}
	return c.ProvisionCapacityStore.GetCapacityReservation(id)
}

func (c provisionR3Capacity) RecordCapacityReady(server, environment, instance string, at time.Time) (store.CapacityReservation, error) {
	c.record("RecordCapacityReady")
	return c.ProvisionCapacityStore.RecordCapacityReady(server, environment, instance, at)
}

// provisionR3Completed is a fixture whose operation has every planned step
// succeeded with consistent evidence, a server bound to the created instance,
// and a capacity reservation whose create outcome is still unknown.
type provisionR3Completed struct {
	f         provisionPlanFixture
	operation store.OperationRecord
	calls     *[]string
}

func provisionR3Complete(t *testing.T, restore bool) provisionR3Completed {
	t.Helper()
	var f provisionPlanFixture
	if restore {
		f, _ = provisionR3RestoreFixture(t)
	} else {
		f = newProvisionPlanFixture(t)
	}
	steps, err := f.workflow.BuildPlan(context.Background(), f.operation)
	if err != nil {
		t.Fatal(err)
	}
	provisionR3ClaimCapacity(t, f)
	now := time.Now().UTC()
	observed := now.Format(time.RFC3339Nano)
	instance := json.RawMessage(provisionR3InstanceRef)
	target := mustMarshal(t, map[string]any{"server_id": provisionTestServer, "environment_id": provisionTestEnv, "instance_ref": instance})
	generation := provisionDataGeneration(f.operation.OperationID)
	bootstrap := mustMarshal(t, map[string]any{"instance_ref": instance, "bootstrap_execution_ref": json.RawMessage(provisionR3BootstrapRef),
		"revision": "bootstrap-1", "digest": "sha256:" + strings.Repeat("a", 64), "state": "succeeded", "observed_at": observed,
		"started_at": observed, "completed_at": observed, "exit_code": 0,
		"validation_evidence": []any{map[string]any{"check_id": "bootstrap-complete", "result": "pass", "observed_at": observed}}})
	restored := mustMarshal(t, map[string]any{"backup_id": "backup-123", "environment_id": provisionTestEnv, "source_data_generation_id": "gen-source",
		"target": target, "target_data_generation_id": generation, "restore_execution_ref": json.RawMessage(provisionR3RestoreRef),
		"outcome": "restored", "observed_at": observed,
		"validation_checks": []any{map[string]any{"check_id": "world-load.check", "result": "pass", "observed_at": observed}}})
	var readinessChecks []any
	for _, check := range f.profile.RequiredChecks {
		readinessChecks = append(readinessChecks, map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion, "result": "pass", "observed_at": observed})
	}
	outputs := map[string]json.RawMessage{
		provisionCreateStep:       mustMarshal(t, map[string]any{"instance_ref": instance, "state": "creating", "observed_at": observed}),
		provisionWaitStep:         mustMarshal(t, map[string]any{"instance_ref": instance, "operation_status": "succeeded", "instance_state": "active", "observed_at": observed}),
		provisionBootstrapStep:    bootstrap,
		provisionBootstrapInspect: bootstrap,
		provisionInstallStep: mustMarshal(t, map[string]any{"target": target, "release_id": "paper-test-1", "outcome": "installed",
			"artifact_verification": "verified", "installation_started": true, "observed_at": observed,
			"artifact_results": []any{map[string]any{"artifact_id": "server-jar", "result": "verified",
				"expected_checksum": map[string]any{"algorithm": "sha256", "digest": strings.Repeat("a", 64)},
				"observed_checksum": map[string]any{"algorithm": "sha256", "digest": strings.Repeat("a", 64)}}}}),
		provisionRestoreStep:        restored,
		provisionRestoreInspectStep: restored,
		provisionCreateUnitStep: mustMarshal(t, map[string]any{"unit_ref": json.RawMessage(provisionTestUnit), "target": target, "release_id": "paper-test-1",
			"definition_checksum": f.profile.ArtifactChecksum, "outcome": "created", "state": "stopped", "started_by_request": false,
			"persistent_data_preserved": true, "observed_at": observed}),
		provisionStartUnitStep:   mustMarshal(t, map[string]any{"unit_ref": json.RawMessage(provisionTestUnit), "outcome": "completed", "state": "running", "observed_at": observed}),
		provisionInspectUnitStep: mustMarshal(t, map[string]any{"unit_ref": json.RawMessage(provisionTestUnit), "state": "running", "observed_at": observed}),
		provisionReadinessStep: mustMarshal(t, map[string]any{"server_id": provisionTestServer, "environment_id": provisionTestEnv, "instance_ref": instance,
			"game_release_id": "paper-test-1", "observed_at": observed, "check_results": readinessChecks}),
	}
	for i := range steps {
		if steps[i].StepID == provisionCompatibilityStep {
			outputs[provisionCompatibilityStep] = provisionCompatibleOutput(t, steps[i], now)
		}
		steps[i].Status = protocolstate.StepSucceeded
		steps[i].Attempt = 1
		steps[i].Output = outputs[steps[i].StepID]
	}
	provisionR3ResolveAll(t, steps)
	operation := f.operation
	operation.Steps = steps
	operation.Status = protocolstate.OperationRunning
	calls := []string{}
	f.workflow.capacity = provisionR3Capacity{ProvisionCapacityStore: f.store, calls: &calls}
	f.workflow.servers = provisionR3Servers{LifecycleOperationStore: f.store, change: func(server *store.LogicalServer) {
		server.InstanceRef = append(json.RawMessage(nil), instance...)
		server.LifecycleState = protocolstate.ServerValidating
		server.DataGenerationStatus = "known"
		server.DataGenerationID = generation
	}}
	return provisionR3Completed{f: f, operation: operation, calls: &calls}
}

// provisionR3ClaimCapacity reserves capacity and claims the create, as
// dispatch does, leaving the create outcome unknown.
func provisionR3ClaimCapacity(t *testing.T, f provisionPlanFixture) {
	t.Helper()
	if _, _, err := f.store.ReserveCapacity(store.CapacityReservation{ProviderAdapterID: "hosting.fake", OperationID: f.operation.OperationID,
		ServerID: provisionTestServer, EnvironmentID: provisionTestEnv, CapacityGroup: f.profile.CapacityGroup, Provider: f.profile.Provider,
		ResourceKey: provisionResourceKey(f.operation.OperationID, f.operation.RequestFingerprint)}, f.profile.CapacityLimit, f.profile.CapacityGroupLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkCapacityCreateSent(f.operation.OperationID); err != nil {
		t.Fatal(err)
	}
}

// provisionR3ResolveAll resolves every step's input as if all had succeeded.
func provisionR3ResolveAll(t *testing.T, steps []store.OperationStep) {
	t.Helper()
	bindingSteps := toBindingSteps(steps)
	for i := range bindingSteps {
		bindingSteps[i].Status = string(protocolstate.StepSucceeded)
	}
	for i := range steps {
		resolved, err := inputbinding.Resolve(bindingSteps, i)
		if err != nil {
			t.Fatal(err)
		}
		steps[i].ResolvedInput = resolved
	}
}

func provisionR3StepIndex(t *testing.T, operation store.OperationRecord, stepID string) int {
	t.Helper()
	i := provisionStepIndex(operation, stepID)
	if i < 0 {
		t.Fatalf("step %q is not planned", stepID)
	}
	return i
}

// provisionR3Clone copies the operation's steps so a case can change them.
func provisionR3Clone(operation store.OperationRecord) store.OperationRecord {
	operation.Steps = append([]store.OperationStep(nil), operation.Steps...)
	return operation
}

// provisionR3EditJSON decodes raw as an object, applies change, and encodes it.
func provisionR3EditJSON(t *testing.T, raw json.RawMessage, change func(map[string]any)) json.RawMessage {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	change(value)
	return mustMarshal(t, value)
}

// provisionR3Output returns a step change that edits the step output.
func provisionR3Output(stepID string, change func(map[string]any)) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, operation *store.OperationRecord) {
		i := provisionR3StepIndex(t, *operation, stepID)
		operation.Steps[i].Output = provisionR3EditJSON(t, operation.Steps[i].Output, change)
	}
}

// provisionR3Resolved returns a step change that edits the resolved input.
func provisionR3Resolved(stepID string, change func(map[string]any)) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, operation *store.OperationRecord) {
		i := provisionR3StepIndex(t, *operation, stepID)
		operation.Steps[i].ResolvedInput = provisionR3EditJSON(t, operation.Steps[i].ResolvedInput, change)
	}
}

func provisionR3Step(stepID string, change func(*store.OperationStep)) func(*testing.T, *store.OperationRecord) {
	return func(t *testing.T, operation *store.OperationRecord) {
		change(&operation.Steps[provisionR3StepIndex(t, *operation, stepID)])
	}
}

func provisionR3Set(field string, value any) func(map[string]any) {
	return func(m map[string]any) { m[field] = value }
}

func provisionR3SetIn(outer, field string, value any) func(map[string]any) {
	return func(m map[string]any) { m[outer].(map[string]any)[field] = value }
}

// TestServerProvisionR3ValidateCompletion pins ValidateCompletion's checks in
// order, and the capacity calls and writes of a successful validation.
func TestServerProvisionR3ValidateCompletion(t *testing.T) {
	const unconfirmed = "server.provision exact instance, bootstrap, game, or execution postconditions were not confirmed"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name    string
		restore bool
		ctx     context.Context
		change  func(*testing.T, *provisionR3Completed)
		want    string
		calls   []string
	}{
		{name: "nil context", ctx: nil, want: "server.provision completion validation requires an operation and context"},
		{name: "wrong type", change: func(_ *testing.T, c *provisionR3Completed) { c.operation.OperationType = "server.start" },
			want: "server.provision completion validation requires an operation and context"},
		{name: "cancelled", ctx: cancelled, want: context.Canceled.Error()},
		{name: "create pending", change: provisionR3Op(provisionR3Step(provisionCreateStep, func(s *store.OperationStep) { s.Status = protocolstate.StepPending })), want: unconfirmed},
		{name: "wait not active", change: provisionR3Op(provisionR3Output(provisionWaitStep, provisionR3Set("instance_state", "stopped"))), want: unconfirmed},
		{name: "bootstrap failed", change: provisionR3Op(provisionR3Output(provisionBootstrapInspect, provisionR3Set("state", "failed"))), want: unconfirmed},
		{name: "install unverified", change: provisionR3Op(provisionR3Output(provisionInstallStep, provisionR3Set("artifact_verification", "skipped"))), want: unconfirmed},
		{name: "unit not created", change: provisionR3Op(provisionR3Output(provisionCreateUnitStep, provisionR3Set("outcome", "failed"))), want: unconfirmed},
		{name: "unit not started", change: provisionR3Op(provisionR3Output(provisionStartUnitStep, provisionR3Set("state", "stopped"))), want: unconfirmed},
		{name: "unit not running", change: provisionR3Op(provisionR3Output(provisionInspectUnitStep, provisionR3Set("state", "stopped"))), want: unconfirmed},
		{name: "server lookup fails", change: func(_ *testing.T, c *provisionR3Completed) {
			c.f.workflow.servers = provisionR3Servers{err: errors.New("store offline")}
		}, want: "store offline"},
		{name: "restore without compatibility", change: func(t *testing.T, c *provisionR3Completed) {
			c.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, c.operation.RequestSnapshot.Parameters, provisionR3Set("restore_backup_id", "backup-123"))
		}, want: "server.provision requested restore lacks exact compatible source and verified restore evidence"},
		{name: "restore profile fails", restore: true, change: func(_ *testing.T, c *provisionR3Completed) {
			c.f.workflow.profiles = provisionR3ProfileResolver{err: errors.New("catalog offline")}
		}, want: "server.provision requested restore lacks exact compatible source and verified restore evidence"},
		{name: "restore incompatible", restore: true, change: provisionR3Op(provisionR3Output(provisionCompatibilityStep, provisionR3Set("compatibility_status", "incompatible"))),
			want: "server.provision requested restore lacks exact compatible source and verified restore evidence"},
		{name: "restore unverified", restore: true, change: provisionR3Op(provisionR3Output(provisionRestoreInspectStep, provisionR3Set("outcome", "rejected"))),
			want: "server.provision requested restore lacks exact compatible source and verified restore evidence"},
		{name: "server bound elsewhere", change: provisionR3Server(func(s *store.LogicalServer) { s.InstanceRef = json.RawMessage(provisionTestInstance) }),
			want: "server.provision logical server is not bound to the exact created instance"},
		{name: "generation unknown", change: provisionR3Server(func(s *store.LogicalServer) { s.DataGenerationStatus = "unknown" }),
			want: "server.provision has no controller-owned data generation for the installed world"},
		{name: "generation foreign", change: provisionR3Server(func(s *store.LogicalServer) { s.DataGenerationID = "gen-other" }),
			want: "server.provision has no controller-owned data generation for the installed world"},
		{name: "readiness evidence stale", change: provisionR3Op(provisionR3Output(provisionReadinessStep, provisionR3Set("observed_at", "2020-01-01T00:00:00Z"))),
			want: "readiness observation is stale or from the future"},
		{name: "readiness pending", change: provisionR3Op(provisionR3Step(provisionReadinessStep, func(s *store.OperationStep) { s.Status = protocolstate.StepPending })),
			want: "readiness evidence must come from a completed game.readiness.check step"},
		{name: "create time invalid", change: provisionR3Op(provisionR3Output(provisionCreateStep, provisionR3Set("observed_at", "yesterday"))),
			want: "server.provision create observation time is invalid"},
		{name: "no reservation", change: func(_ *testing.T, c *provisionR3Completed) {
			capacity := c.f.workflow.capacity.(provisionR3Capacity)
			capacity.getErr = store.ErrCapacityNotFound
			c.f.workflow.capacity = capacity
		}, want: "load provision capacity reservation: " + store.ErrCapacityNotFound.Error(), calls: []string{"GetCapacityReservation"}},
		{name: "observation rejected", change: provisionR3Op(provisionR3Output(provisionCreateStep, provisionR3Set("observed_at", "2020-01-01T00:00:00Z"))),
			want: "record exact provisioned capacity: invalid operation record: provider provisioning time precedes reservation", calls: []string{"GetCapacityReservation", "ObserveCapacity"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := provisionR3Complete(t, test.restore)
			c.operation = provisionR3Clone(c.operation)
			if test.change != nil {
				test.change(t, &c)
			}
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			if got := provisionErrText(c.f.workflow.ValidateCompletion(ctx, c.operation)); got != test.want {
				t.Fatalf("ValidateCompletion = %q, want %q", got, test.want)
			}
			if test.calls != nil && !reflect.DeepEqual(*c.calls, test.calls) {
				t.Fatalf("capacity calls = %v, want %v", *c.calls, test.calls)
			}
		})
	}
}

func provisionR3Op(change func(*testing.T, *store.OperationRecord)) func(*testing.T, *provisionR3Completed) {
	return func(t *testing.T, c *provisionR3Completed) { change(t, &c.operation) }
}

func provisionR3Server(change func(*store.LogicalServer)) func(*testing.T, *provisionR3Completed) {
	return func(_ *testing.T, c *provisionR3Completed) {
		servers := c.f.workflow.servers.(provisionR3Servers)
		base := servers.change
		servers.change = func(s *store.LogicalServer) {
			base(s)
			change(s)
		}
		c.f.workflow.servers = servers
	}
}

// TestServerProvisionR3OutputPredicates pins each clause of the step-output
// predicates: one change to complete evidence turns each one false.
func TestServerProvisionR3OutputPredicates(t *testing.T) {
	unitCreated := func(o store.OperationRecord, _ *provisionR3Completed) bool { return provisionUnitCreated(o) }
	restoreDone := func(o store.OperationRecord, _ *provisionR3Completed) bool { return provisionRestoreSucceeded(o) }
	compatible := func(o store.OperationRecord, c *provisionR3Completed) bool {
		return provisionCompatibilitySucceeded(o, c.f.schemas, c.f.profile)
	}
	type check = func(store.OperationRecord, *provisionR3Completed) bool
	undecodable := func(stepID string) func(*testing.T, *store.OperationRecord) {
		return provisionR3Step(stepID, func(s *store.OperationStep) { s.Output = json.RawMessage(`[`) })
	}
	undecodableInput := func(stepID string) func(*testing.T, *store.OperationRecord) {
		return provisionR3Step(stepID, func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`[`) })
	}
	pending := func(stepID string) func(*testing.T, *store.OperationRecord) {
		return provisionR3Step(stepID, func(s *store.OperationStep) { s.Status = protocolstate.StepPending })
	}
	otherInstance := map[string]any{"adapter_id": "hosting.fake", "environment_id": "staging", "resource_kind": "instance", "external_id": "vm-other"}
	cases := []struct {
		name    string
		restore bool
		check   check
		change  func(*testing.T, *store.OperationRecord)
		profile func(*ServerProvisionProfile)
		want    bool
	}{
		{name: "unit created", check: unitCreated, want: true},
		{name: "unit unchanged", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("outcome", "unchanged")), want: true},
		{name: "unit already running", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("state", "running")), want: true},
		{name: "unit no create output", check: unitCreated, change: pending(provisionCreateStep)},
		{name: "unit step pending", check: unitCreated, change: pending(provisionCreateUnitStep)},
		{name: "unit output undecodable", check: unitCreated, change: undecodable(provisionCreateUnitStep)},
		{name: "unit input undecodable", check: unitCreated, change: undecodableInput(provisionCreateUnitStep)},
		{name: "unit ref not object", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("unit_ref", "unit"))},
		{name: "unit ref other", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3SetIn("unit_ref", "external_id", "other"))},
		{name: "unit outcome failed", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("outcome", "failed"))},
		{name: "unit state absent", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("state", "absent"))},
		{name: "unit started by request", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("started_by_request", true))},
		{name: "unit data not preserved", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("persistent_data_preserved", false))},
		{name: "unit other release", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3Set("release_id", "paper-test-2"))},
		{name: "unit other server", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3SetIn("target", "server_id", "other"))},
		{name: "unit other environment", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3SetIn("target", "environment_id", "production"))},
		{name: "unit other instance", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3SetIn("target", "instance_ref", otherInstance))},
		{name: "unit other checksum", check: unitCreated, change: provisionR3Output(provisionCreateUnitStep, provisionR3SetIn("definition_checksum", "digest", strings.Repeat("c", 64)))},

		{name: "restore succeeded", restore: true, check: restoreDone, want: true},
		{name: "restore outcome unknown then restored", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("outcome", "outcome_unknown")), want: true},
		{name: "restore call pending", restore: true, check: restoreDone, change: pending(provisionRestoreStep)},
		{name: "restore inspect pending", restore: true, check: restoreDone, change: pending(provisionRestoreInspectStep)},
		{name: "restore no create output", restore: true, check: restoreDone, change: pending(provisionCreateStep)},
		{name: "restore call undecodable", restore: true, check: restoreDone, change: undecodable(provisionRestoreStep)},
		{name: "restore inspect undecodable", restore: true, check: restoreDone, change: undecodable(provisionRestoreInspectStep)},
		{name: "restore input undecodable", restore: true, check: restoreDone, change: undecodableInput(provisionRestoreStep)},
		{name: "restore call rejected", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("outcome", "rejected"))},
		{name: "restore inspect unknown", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("outcome", "outcome_unknown"))},
		{name: "restore call other backup", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("backup_id", "other"))},
		{name: "restore inspect other backup", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("backup_id", "other"))},
		{name: "restore call other environment", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("environment_id", "production"))},
		{name: "restore inspect other environment", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("environment_id", "production"))},
		{name: "restore call other source", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("source_data_generation_id", "gen-other"))},
		{name: "restore inspect other source", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("source_data_generation_id", "gen-other"))},
		{name: "restore call other target generation", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreStep, provisionR3Set("target_data_generation_id", "gen-other"))},
		{name: "restore inspect other target generation", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("target_data_generation_id", "gen-other"))},
		{name: "restore input and outputs other generation", restore: true, check: restoreDone, change: func(t *testing.T, o *store.OperationRecord) {
			provisionR3Resolved(provisionRestoreStep, provisionR3Set("target_data_generation_id", "gen-other"))(t, o)
			provisionR3Output(provisionRestoreStep, provisionR3Set("target_data_generation_id", "gen-other"))(t, o)
			provisionR3Output(provisionRestoreInspectStep, provisionR3Set("target_data_generation_id", "gen-other"))(t, o)
		}},
		{name: "restore input targets other instance", restore: true, check: restoreDone, change: func(t *testing.T, o *store.OperationRecord) {
			for _, id := range []string{provisionRestoreStep, provisionRestoreInspectStep} {
				provisionR3Output(id, provisionR3SetIn("target", "instance_ref", otherInstance))(t, o)
			}
			provisionR3Resolved(provisionRestoreStep, provisionR3SetIn("target", "instance_ref", otherInstance))(t, o)
		}},
		{name: "restore call other target", restore: true, check: restoreDone, change: func(t *testing.T, o *store.OperationRecord) {
			for _, id := range []string{provisionRestoreStep, provisionRestoreInspectStep} {
				provisionR3Output(id, provisionR3SetIn("target", "server_id", "other"))(t, o)
			}
		}},
		{name: "restore inspect other target", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3SetIn("target", "server_id", "other"))},
		{name: "restore other execution", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3SetIn("restore_execution_ref", "external_id", "other"))},
		{name: "restore check failed", restore: true, check: restoreDone, change: provisionR3Output(provisionRestoreInspectStep, provisionR3Set("validation_checks", []any{map[string]any{"check_id": "world-load.check", "result": "fail"}}))},

		{name: "compatible", restore: true, check: compatible, want: true},
		{name: "compatibility pending", restore: true, check: compatible, change: pending(provisionCompatibilityStep)},
		{name: "compatibility schema invalid", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("compatibility_status", 5))},
		{name: "compatibility input undecodable", restore: true, check: compatible, change: provisionR3Step(provisionCompatibilityStep, func(s *store.OperationStep) { s.Input = json.RawMessage(`[`) })},
		{name: "compatibility incompatible", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("compatibility_status", "incompatible"))},
		{name: "compatibility other backup", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("backup_id", "other"))},
		{name: "compatibility other environment", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("environment_id", "production"))},
		{name: "compatibility other generation", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("data_generation_id", "gen-other"))},
		{name: "compatibility other source adapter", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("source_game_adapter_id", "game.other"))},
		{name: "compatibility other source version", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("source_game_version", "1.0"))},
		{name: "compatibility other target adapter", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("target_game_adapter_id", "game.other"))},
		{name: "compatibility other target release", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("target_release_id", "paper-test-2"))},
		{name: "compatibility profile other release", restore: true, check: compatible, profile: func(p *ServerProvisionProfile) { p.GameReleaseID = "paper-test-2" }},
		{name: "compatibility other target version", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, provisionR3Set("target_game_version", "1.0"))},
		{name: "compatibility missing check", restore: true, check: compatible, profile: func(p *ServerProvisionProfile) {
			p.RequiredCompatibilityCheckIDs = []string{"world-format.check", "plugins.check"}
		}},
		{name: "compatibility duplicate check", restore: true, check: compatible, profile: func(p *ServerProvisionProfile) {
			p.RequiredCompatibilityCheckIDs = []string{"world-format.check", "plugins.check"}
		}, change: provisionR3Output(provisionCompatibilityStep, func(m map[string]any) {
			checks := m["check_results"].([]any)
			m["check_results"] = []any{checks[0], checks[0]}
		})},
		{name: "compatibility check failed", restore: true, check: compatible, change: provisionR3Output(provisionCompatibilityStep, func(m map[string]any) {
			m["check_results"].([]any)[0].(map[string]any)["result"] = "incompatible"
		})},
		{name: "compatibility other check", restore: true, check: compatible, profile: func(p *ServerProvisionProfile) {
			p.RequiredCompatibilityCheckIDs = []string{"plugins.check"}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c := provisionR3Complete(t, test.restore)
			operation := provisionR3Clone(c.operation)
			if test.change != nil {
				test.change(t, &operation)
			}
			if test.profile != nil {
				test.profile(&c.f.profile)
			}
			if got := test.check(operation, &c); got != test.want {
				t.Fatalf("predicate = %v, want %v", got, test.want)
			}
		})
	}
}

// provisionR3LifecycleOutcome is the expected projection of one case.
type provisionR3LifecycleOutcome struct {
	state      protocolstate.ServerState
	changed    bool
	err        string
	instance   bool
	generation string
	genStatus  string
	desired    string
}

// provisionR3Projection is what ProjectLifecycle returned.
type provisionR3Projection struct {
	next    store.LogicalServer
	changed bool
	err     error
}

func provisionR3CheckLifecycle(t *testing.T, got provisionR3Projection, want provisionR3LifecycleOutcome) {
	t.Helper()
	if want.err != "" {
		if provisionErrText(got.err) != want.err || got.changed || !reflect.DeepEqual(got.next, store.LogicalServer{}) {
			t.Fatalf("ProjectLifecycle = %#v; want error %q", got, want.err)
		}
		return
	}
	if got.err != nil || got.changed != want.changed || !provisionR3LifecycleMatches(got.next, want.state, want.instance, want.desired) ||
		!provisionR3GenerationMatches(got.next, want.generation, want.genStatus) {
		t.Fatalf("ProjectLifecycle = %#v; want %#v", got, want)
	}
}

func provisionR3LifecycleMatches(next store.LogicalServer, state protocolstate.ServerState, instance bool, desired string) bool {
	return next.LifecycleState == state && (len(next.InstanceRef) > 0) == instance && next.DesiredState == desired
}

func provisionR3GenerationMatches(next store.LogicalServer, generation, status string) bool {
	return next.DataGenerationID == generation && next.DataGenerationStatus == status
}

// TestServerProvisionR3ProjectLifecycle pins every lifecycle transition.
func TestServerProvisionR3ProjectLifecycle(t *testing.T) {
	c := provisionR3Complete(t, false)
	stored, err := c.f.store.GetLogicalServer(provisionTestEnv, provisionTestServer)
	if err != nil {
		t.Fatal(err)
	}
	generation := provisionDataGeneration(c.operation.OperationID)
	withSteps := func(status protocolstate.StepStatus, ids ...string) func(*testing.T, *store.OperationRecord) {
		return func(t *testing.T, o *store.OperationRecord) {
			for _, id := range ids {
				o.Steps[provisionR3StepIndex(t, *o, id)].Status = status
			}
		}
	}
	allPending := withSteps(protocolstate.StepPending, provisionCreateStep, provisionStartUnitStep, provisionReadinessStep)
	result, err := c.f.workflow.BuildSuccessResult(context.Background(), c.operation)
	if err != nil {
		t.Fatal(err)
	}
	successResult := func(_ *testing.T, o *store.OperationRecord) {
		o.Status = protocolstate.OperationSucceeded
		o.Result = result
	}
	cases := []struct {
		name     string
		from     protocolstate.ServerState
		instance bool
		status   protocolstate.OperationStatus
		change   func(*testing.T, *store.OperationRecord)
		want     provisionR3LifecycleOutcome
	}{
		{name: "unprovisioned pending", from: protocolstate.ServerUnprovisioned, status: protocolstate.OperationPending, change: allPending, want: provisionR3LifecycleOutcome{state: protocolstate.ServerUnprovisioned}},
		{name: "unprovisioned running", from: protocolstate.ServerUnprovisioned, status: protocolstate.OperationRunning, change: allPending, want: provisionR3LifecycleOutcome{state: protocolstate.ServerProvisioning, changed: true}},
		{name: "provisioning failed", from: protocolstate.ServerProvisioning, status: protocolstate.OperationFailed, want: provisionR3LifecycleOutcome{state: protocolstate.ServerFailed, changed: true}},
		{name: "provisioning before create", from: protocolstate.ServerProvisioning, status: protocolstate.OperationRunning, change: allPending, want: provisionR3LifecycleOutcome{state: protocolstate.ServerProvisioning}},
		{name: "provisioning created", from: protocolstate.ServerProvisioning, status: protocolstate.OperationRunning,
			want: provisionR3LifecycleOutcome{state: protocolstate.ServerPreparing, changed: true, instance: true, generation: generation, genStatus: "unknown"}},
		{name: "preparing failed", from: protocolstate.ServerPreparing, instance: true, status: protocolstate.OperationFailed, want: provisionR3LifecycleOutcome{state: protocolstate.ServerFailed, changed: true, instance: true}},
		{name: "preparing before start", from: protocolstate.ServerPreparing, instance: true, status: protocolstate.OperationRunning, change: withSteps(protocolstate.StepPending, provisionStartUnitStep),
			want: provisionR3LifecycleOutcome{state: protocolstate.ServerPreparing, instance: true}},
		{name: "preparing start without install", from: protocolstate.ServerPreparing, instance: true, status: protocolstate.OperationRunning, change: withSteps(protocolstate.StepPending, provisionInstallStep),
			want: provisionR3LifecycleOutcome{err: "server.provision cannot start before the exact game release is installed"}},
		{name: "preparing started", from: protocolstate.ServerPreparing, instance: true, status: protocolstate.OperationRunning,
			want: provisionR3LifecycleOutcome{state: protocolstate.ServerStarting, changed: true, instance: true, generation: generation, genStatus: "known"}},
		{name: "starting failed", from: protocolstate.ServerStarting, instance: true, status: protocolstate.OperationFailed, want: provisionR3LifecycleOutcome{state: protocolstate.ServerFailed, changed: true, instance: true}},
		{name: "starting before readiness", from: protocolstate.ServerStarting, instance: true, status: protocolstate.OperationRunning, change: withSteps(protocolstate.StepPending, provisionReadinessStep),
			want: provisionR3LifecycleOutcome{state: protocolstate.ServerStarting, instance: true}},
		{name: "starting checked", from: protocolstate.ServerStarting, instance: true, status: protocolstate.OperationRunning, want: provisionR3LifecycleOutcome{state: protocolstate.ServerValidating, changed: true, instance: true}},
		{name: "validating failed", from: protocolstate.ServerValidating, instance: true, status: protocolstate.OperationFailed, want: provisionR3LifecycleOutcome{state: protocolstate.ServerDegraded, changed: true, instance: true}},
		{name: "validating running", from: protocolstate.ServerValidating, instance: true, status: protocolstate.OperationRunning, want: provisionR3LifecycleOutcome{state: protocolstate.ServerValidating, instance: true}},
		{name: "validating succeeded without result", from: protocolstate.ServerValidating, instance: true, status: protocolstate.OperationSucceeded,
			want: provisionR3LifecycleOutcome{err: "terminal server.provision result lacks exact instance and readiness evidence"}},
		{name: "validating succeeded", from: protocolstate.ServerValidating, instance: true, change: successResult, want: provisionR3LifecycleOutcome{state: protocolstate.ServerReady, changed: true, instance: true, desired: "running"}},
		{name: "ready unchanged", from: protocolstate.ServerReady, instance: true, status: protocolstate.OperationSucceeded, want: provisionR3LifecycleOutcome{state: protocolstate.ServerReady, instance: true}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			operation := provisionR3Clone(c.operation)
			operation.Status = test.status
			if test.change != nil {
				test.change(t, &operation)
			}
			current := stored
			current.Record.LifecycleState = test.from
			current.Record.DesiredState = ""
			if test.instance {
				current.Record.InstanceRef = json.RawMessage(provisionR3InstanceRef)
			}
			var got provisionR3Projection
			got.next, got.changed, got.err = c.f.workflow.ProjectLifecycle(operation, current)
			want := test.want
			if want.genStatus == "" {
				want.genStatus = current.Record.DataGenerationStatus
			}
			provisionR3CheckLifecycle(t, got, want)
		})
	}
}

// TestServerProvisionR3ProjectLifecycleTargetMismatch pins the projection guard.
func TestServerProvisionR3ProjectLifecycleTargetMismatch(t *testing.T) {
	c := provisionR3Complete(t, false)
	stored, err := c.f.store.GetLogicalServer(provisionTestEnv, provisionTestServer)
	if err != nil {
		t.Fatal(err)
	}
	current := stored
	current.Record.ServerID = "other"
	if _, _, err := c.f.workflow.ProjectLifecycle(c.operation, current); provisionErrText(err) != "server.provision lifecycle projection target mismatch" {
		t.Fatalf("err = %v", err)
	}
	operation := c.operation
	operation.OperationType = "server.start"
	if _, _, err := c.f.workflow.ProjectLifecycle(operation, stored); provisionErrText(err) != "server.provision lifecycle projection target mismatch" {
		t.Fatalf("err = %v", err)
	}
}

// TestServerProvisionR3ValidateCompletionWrites pins ValidateCompletion: success records capacity in order.
func TestServerProvisionR3ValidateCompletionWrites(t *testing.T) {
	c := provisionR3Complete(t, false)
	if err := c.f.workflow.ValidateCompletion(context.Background(), c.operation); err != nil {
		t.Fatal(err)
	}
	want := []string{"GetCapacityReservation", "ObserveCapacity", "RecordCapacityReady"}
	if !reflect.DeepEqual(*c.calls, want) {
		t.Fatalf("capacity calls = %v, want %v", *c.calls, want)
	}
	reservation, err := c.f.store.GetCapacityReservation(c.operation.OperationID)
	if err != nil || reservation.State != store.CapacityActive || reservation.ProviderInstanceID != "vm-r3" || reservation.ReadyAt.IsZero() {
		t.Fatalf("reservation = %#v, %v", reservation, err)
	}
}

// TestServerProvisionR3ValidateCompletionRestore pins ValidateCompletion: restore success.
func TestServerProvisionR3ValidateCompletionRestore(t *testing.T) {
	c := provisionR3Complete(t, true)
	if err := c.f.workflow.ValidateCompletion(context.Background(), c.operation); err != nil {
		t.Fatal(err)
	}
}

// TestServerProvisionR3ValidateCompletionTwice pins ValidateCompletion: second validation skips the observation.
func TestServerProvisionR3ValidateCompletionTwice(t *testing.T) {
	c := provisionR3Complete(t, false)
	if err := c.f.workflow.ValidateCompletion(context.Background(), c.operation); err != nil {
		t.Fatal(err)
	}
	*c.calls = nil
	if err := c.f.workflow.ValidateCompletion(context.Background(), c.operation); err != nil {
		t.Fatalf("second ValidateCompletion = %v", err)
	}
	if want := []string{"GetCapacityReservation", "RecordCapacityReady"}; !reflect.DeepEqual(*c.calls, want) {
		t.Fatalf("capacity calls = %v, want %v", *c.calls, want)
	}
}
