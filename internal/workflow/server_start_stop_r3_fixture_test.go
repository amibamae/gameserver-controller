package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin server.start and server.stop planning, precondition,
// reconciliation, failure-handling and projection behaviour against in-memory
// fakes, so the shared unit-step helpers can be refactored safely.

var updateStartStopR3Golden = flag.Bool("update-start-stop-r3-golden", false, "rewrite testdata/server_start_stop_plans.golden.json")

const (
	startStopR3OtherInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-other"}`
	startStopR3OtherUnit     = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper-other"}`
	startStopR3ExternalRef   = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"operation","external_id":"ambiguous"}`
	startStopR3ObservedAt    = "2026-11-13T09:00:00Z"
)

type startStopR3Servers struct {
	store.LifecycleOperationStore
	record store.StoredLogicalServer
}

func (s *startStopR3Servers) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	if s.record.Record.EnvironmentID != environmentID || s.record.Record.ServerID != serverID {
		return store.StoredLogicalServer{}, store.ErrNotFound
	}
	return s.record, nil
}

type startStopR3Adapters struct {
	version     string
	descriptors map[string]adapter.Descriptor
	reply       func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
	calls       []startStopR3Call
}

type startStopR3Call struct {
	adapterID string
	request   adapter.InvokeRequest
	deadline  bool
}

func (a *startStopR3Adapters) ProtocolVersion() string { return a.version }

func (a *startStopR3Adapters) Descriptor(adapterID string) (adapter.Descriptor, error) {
	descriptor, ok := a.descriptors[adapterID]
	if !ok {
		return adapter.Descriptor{}, adapter.ErrAdapterNotFound
	}
	return descriptor, nil
}

func (a *startStopR3Adapters) Invoke(ctx context.Context, adapterID string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	_, hasDeadline := ctx.Deadline()
	a.calls = append(a.calls, startStopR3Call{adapterID: adapterID, request: request, deadline: hasDeadline})
	if a.reply == nil {
		return adapter.InvokeResponse{}, errors.New("no scripted reply")
	}
	return a.reply(request)
}

type startStopR3Schemas struct {
	*schemaregistry.Registry
	reject string
}

func (s *startStopR3Schemas) ValidateJSON(uri string, raw []byte) error {
	if s.reject != "" && strings.HasSuffix(uri, s.reject) {
		return errors.New("rejected by test")
	}
	return s.Registry.ValidateJSON(uri, raw)
}

type startStopR3Profiles struct {
	start    ServerStartProfile
	startErr error
	stop     ServerStopProfile
	stopErr  error
}

func (p *startStopR3Profiles) ResolveServerStart(context.Context, store.LogicalServer) (ServerStartProfile, error) {
	return p.start, p.startErr
}

func (p *startStopR3Profiles) ResolveServerStop(context.Context, store.LogicalServer) (ServerStopProfile, error) {
	return p.stop, p.stopErr
}

type startStopR3Fixture struct {
	schemas  *startStopR3Schemas
	adapters *startStopR3Adapters
	servers  *startStopR3Servers
	profiles *startStopR3Profiles
	start    *ServerStartWorkflow
	stop     *ServerStopWorkflow
}

func (f *startStopR3Fixture) server() *store.LogicalServer { return &f.servers.record.Record }

func (f *startStopR3Fixture) version() string { return f.schemas.ProtocolVersion() }

// startStopR3Capability declares a capability whose schemas are the stem's
// -input and -output schemas.
func startStopR3Capability(version, name, schemaStem string, mutating bool) adapter.Capability {
	return adapter.Capability{
		Name: name, Version: "1", Mutating: mutating,
		InputSchemaURI:      "urn:gameserver-controller:" + version + ":" + schemaStem + "-input",
		OutputSchemaURI:     "urn:gameserver-controller:" + version + ":" + schemaStem + "-output",
		RequiredPermissions: []string{},
	}
}

func startStopR3Constrained(capability adapter.Capability, constraints map[string]any) adapter.Capability {
	capability.Constraints = constraints
	return capability
}

func startStopR3Checks() []ReadinessRequirement {
	return []ReadinessRequirement{
		{CheckID: "paper.startup-complete", CheckVersion: "1"},
		{CheckID: "paper.world-loaded", CheckVersion: "2"},
	}
}

func newStartStopR3Fixture(t *testing.T) *startStopR3Fixture {
	t.Helper()
	registry, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	version := registry.ProtocolVersion()
	declared := []any{}
	for _, check := range startStopR3Checks() {
		declared = append(declared, map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion})
	}
	readiness := map[string]any{"readiness_check_sets": []any{map[string]any{
		"game_release_id": startTestRelease, "environment_ids": []string{startTestEnvID},
		"readiness_check_set_id": startTestSetID, "required_checks": declared,
	}}}
	descriptor := func(id string, category adapter.Category, capabilities ...adapter.Capability) adapter.Descriptor {
		return adapter.Descriptor{
			AdapterID: id, Category: category, DisplayName: id, ImplementationVersion: "test",
			ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
			Capabilities: capabilities,
		}
	}
	adapters := &startStopR3Adapters{version: version, descriptors: map[string]adapter.Descriptor{
		"hosting.fake": descriptor("hosting.fake", adapter.CategoryHosting,
			startStopR3Capability(version, "hosting.instance.inspect", "hosting:instance-inspect", false)),
		"execution.fake": descriptor("execution.fake", adapter.CategoryExecution,
			startStopR3Capability(version, "execution.unit.start", "execution:systemd-unit-start", true),
			startStopR3Capability(version, "execution.unit.inspect", "execution:systemd-unit-inspect", false),
			startStopR3Capability(version, "execution.unit.graceful_stop", "execution:systemd-unit-graceful-stop", true)),
		"game.paper": descriptor("game.paper", adapter.CategoryGame,
			startStopR3Constrained(startStopR3Capability(version, "game.readiness.check", "game:readiness-check", false), readiness),
			startStopR3Capability(version, "game.save.request", "game:save-request", true)),
	}}
	servers := &startStopR3Servers{record: store.StoredLogicalServer{Revision: 7, Record: store.LogicalServer{
		ServerID: startTestServerID, EnvironmentID: startTestEnvID, DisplayName: "Start Stop",
		LifecycleState: protocolstate.ServerStopped, DesiredState: "stopped",
		InstanceRef:   json.RawMessage(startTestInstance),
		GameAdapterID: "game.paper", ExecutionAdapterID: "execution.fake",
	}}}
	profiles := &startStopR3Profiles{
		start: ServerStartProfile{
			GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit),
			ReadinessCheckSetID: startTestSetID, RequiredChecks: startStopR3Checks(),
		},
		stop: ServerStopProfile{GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit)},
	}
	schemas := &startStopR3Schemas{Registry: registry}
	start, err := NewServerStartWorkflow(servers, adapters, schemas, profiles, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := NewServerStopWorkflow(servers, adapters, schemas, profiles)
	if err != nil {
		t.Fatal(err)
	}
	return &startStopR3Fixture{schemas: schemas, adapters: adapters, servers: servers, profiles: profiles, start: start, stop: stop}
}

func startStopR3Operation(operationType, version, parameters string) store.OperationRecord {
	return store.OperationRecord{
		OperationID: operationType + "-r3", OperationType: operationType, ProtocolVersion: version,
		Status:          protocolstate.OperationRunning,
		Target:          store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID},
		RequestSnapshot: store.RequestSnapshot{OperationType: operationType, Parameters: json.RawMessage(parameters)},
	}
}

func startStopR3ErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func startStopR3SetStep(operation *store.OperationRecord, id string, change func(*store.OperationStep)) {
	for i := range operation.Steps {
		if operation.Steps[i].StepID == id {
			change(&operation.Steps[i])
			return
		}
	}
	panic("no step " + id)
}

func startStopR3Succeed(output string) func(*store.OperationStep) {
	return func(step *store.OperationStep) {
		step.Status = protocolstate.StepSucceeded
		step.Attempt = 1
		step.Output = json.RawMessage(output)
	}
}

func startStopR3InstanceOutput(instance, state string) string {
	return fmt.Sprintf(`{"instance_ref":%s,"state":%q,"observed_at":%q}`, instance, state, startStopR3ObservedAt)
}

func startStopR3UnitOutput(unit, state string) string {
	return fmt.Sprintf(`{"unit_ref":%s,"state":%q,"observed_at":%q}`, unit, state, startStopR3ObservedAt)
}

func startStopR3UnitResult(unit, outcome, state string) string {
	return fmt.Sprintf(`{"unit_ref":%s,"outcome":%q,"state":%q,"observed_at":%q}`, unit, outcome, state, startStopR3ObservedAt)
}

func startStopR3SaveOutput(serverID, instance, release, outcome string) string {
	saved := ""
	if outcome == "confirmed" {
		saved = fmt.Sprintf(`,"saved_at":%q`, startStopR3ObservedAt)
	}
	return fmt.Sprintf(`{"target":{"server_id":%q,"environment_id":"staging","instance_ref":%s},"release_id":%q,"outcome":%q,"observed_at":%q%s}`,
		serverID, instance, release, outcome, startStopR3ObservedAt, saved)
}

// startStopR3PlannedStart returns a planned start whose earlier steps all
// carry good evidence; individual cases then break one rule.
func startStopR3PlannedStart(t *testing.T, f *startStopR3Fixture) store.OperationRecord {
	t.Helper()
	operation := startStopR3Operation("server.start", f.version(), `{}`)
	steps, err := f.start.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	startStopR3SetStep(&operation, startInspectInstanceStep, startStopR3Succeed(startStopR3InstanceOutput(startTestInstance, "active")))
	startStopR3SetStep(&operation, startRuntimeStep, startStopR3Succeed(startStopR3UnitResult(startTestUnit, "completed", "running")))
	startStopR3SetStep(&operation, startVerifyRuntimeStep, startStopR3Succeed(startStopR3UnitOutput(startTestUnit, "running")))
	return operation
}

func startStopR3PlannedStop(t *testing.T, f *startStopR3Fixture) store.OperationRecord {
	t.Helper()
	f.server().LifecycleState = protocolstate.ServerReady
	operation := startStopR3Operation("server.stop", f.version(), `{}`)
	steps, err := f.stop.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	startStopR3SetStep(&operation, stopInspectInstanceStep, startStopR3Succeed(startStopR3InstanceOutput(startTestInstance, "active")))
	startStopR3SetStep(&operation, stopInspectUnitStep, startStopR3Succeed(startStopR3UnitOutput(startTestUnit, "running")))
	startStopR3SetStep(&operation, stopSaveGameStep, startStopR3Succeed(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "confirmed")))
	return operation
}

func startStopR3Capabilities(f *startStopR3Fixture, adapterID string, change func([]adapter.Capability) []adapter.Capability) {
	descriptor := f.adapters.descriptors[adapterID]
	copied := append([]adapter.Capability(nil), descriptor.Capabilities...)
	descriptor.Capabilities = change(copied)
	f.adapters.descriptors[adapterID] = descriptor
}

func startStopR3DropCapability(f *startStopR3Fixture, adapterID, name string) {
	startStopR3Capabilities(f, adapterID, func(capabilities []adapter.Capability) []adapter.Capability {
		kept := capabilities[:0]
		for _, capability := range capabilities {
			if capability.Name != name {
				kept = append(kept, capability)
			}
		}
		return kept
	})
}

func startStopR3EditCapability(f *startStopR3Fixture, adapterID, name string, edit func(*adapter.Capability)) {
	startStopR3Capabilities(f, adapterID, func(capabilities []adapter.Capability) []adapter.Capability {
		for i := range capabilities {
			if capabilities[i].Name == name {
				edit(&capabilities[i])
			}
		}
		return capabilities
	})
}

func startStopR3StartProfile(change func(*ServerStartProfile)) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(f *startStopR3Fixture, _ *store.OperationRecord) { change(&f.profiles.start) }
}

func startStopR3StopProfile(change func(*ServerStopProfile)) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(f *startStopR3Fixture, _ *store.OperationRecord) { change(&f.profiles.stop) }
}

func startStopR3Server(change func(*store.LogicalServer)) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(f *startStopR3Fixture, _ *store.OperationRecord) { change(f.server()) }
}

func startStopR3Op(change func(*store.OperationRecord)) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(_ *startStopR3Fixture, o *store.OperationRecord) { change(o) }
}

func startStopR3Readiness(constraints map[string]any) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(f *startStopR3Fixture, _ *store.OperationRecord) {
		startStopR3EditCapability(f, "game.paper", "game.readiness.check", func(c *adapter.Capability) { c.Constraints = constraints })
	}
}

func startStopR3ReadinessSet(release string, environments []string, setID string, checks []any) map[string]any {
	return map[string]any{"readiness_check_sets": []any{map[string]any{
		"game_release_id": release, "environment_ids": environments, "readiness_check_set_id": setID, "required_checks": checks,
	}}}
}

func startStopR3UnitWith(old, replacement string) json.RawMessage {
	return json.RawMessage(strings.Replace(startTestUnit, old, replacement, 1))
}

func startStopR3Step(id string, change func(*store.OperationStep)) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(_ *startStopR3Fixture, o *store.OperationRecord) { startStopR3SetStep(o, id, change) }
}

func startStopR3Status(status protocolstate.StepStatus) func(*store.OperationStep) {
	return func(step *store.OperationStep) { step.Status = status }
}

func startStopR3Output(output string) func(*store.OperationStep) {
	return func(step *store.OperationStep) { step.Output = json.RawMessage(output) }
}

func startStopR3Input(input string) func(*store.OperationStep) {
	return func(step *store.OperationStep) { step.Input = json.RawMessage(input) }
}

func startStopR3RemoveStep(id string) func(*startStopR3Fixture, *store.OperationRecord) {
	return func(_ *startStopR3Fixture, o *store.OperationRecord) {
		kept := o.Steps[:0]
		for _, step := range o.Steps {
			if step.StepID != id {
				kept = append(kept, step)
			}
		}
		o.Steps = kept
	}
}
