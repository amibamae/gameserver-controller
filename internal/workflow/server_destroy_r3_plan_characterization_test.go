package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the exact errors, check order, and planned
// steps of server.destroy BuildPlan and the request, target, backup, and
// profile validators it shares with the other server.destroy stages.

const (
	destroyR3RequestRequired = "server.destroy operation and loaded protocol version are required"
	destroyR3RequestFields   = "server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID"
	destroyR3BackupMismatch  = "verified backup does not bind the exact server, environment, storage profile, instance, and current data generation"
	destroyR3ProfileMismatch = "trusted destroy profile unit reference does not bind the configured execution adapter and environment"
	destroyR3KnownGeneration = "server.destroy requires a known current data generation"
)

type destroyR3Profiles struct {
	calls   int
	profile ServerDestroyProfile
	err     error
}

func (r *destroyR3Profiles) ResolveServerDestroy(context.Context, store.LogicalServer) (ServerDestroyProfile, error) {
	r.calls++
	return r.profile, r.err
}

type destroyR3Backups struct {
	calls  int
	backup VerifiedServerBackup
	err    error
}

func (r *destroyR3Backups) ResolveVerifiedServerBackup(context.Context, store.LogicalServer, string, string) (VerifiedServerBackup, error) {
	r.calls++
	return r.backup, r.err
}

type destroyR3Authorizer struct {
	calls int
	err   error
}

func (a *destroyR3Authorizer) AuthorizeServerDestroy(context.Context, string, string, store.OperationRecord, store.LogicalServer, VerifiedServerBackup) error {
	a.calls++
	return a.err
}

// destroyR3Schemas rejects every document validated against one schema URI.
type destroyR3Schemas struct {
	schemaRuntime
	rejectURI string
}

func (s destroyR3Schemas) ValidateJSON(uri string, document []byte) error {
	if uri == s.rejectURI {
		return errors.New("rejected by test schema")
	}
	return s.schemaRuntime.ValidateJSON(uri, document)
}

func destroyR3Backup() VerifiedServerBackup {
	return VerifiedServerBackup{
		BackupID: destroyTestBackupID, ServerID: startTestServerID, EnvironmentID: startTestEnvID,
		StorageProfile: destroyTestProfile, DataGenerationID: destroyTestGeneration,
		SourceInstanceRef: json.RawMessage(startTestInstance), IntegrityVerified: true,
	}
}

func destroyR3Parameters() serverDestroyParameters {
	return serverDestroyParameters{
		ExpectedInstanceRef: json.RawMessage(startTestInstance), BackupProfile: destroyTestProfile,
		VerifiedBackupID: destroyTestBackupID, ApprovalID: destroyTestApproval,
	}
}

func destroyR3URN(fixture serverDestroyFixture, name string) string {
	return "urn:gameserver-controller:" + fixture.base.schemas.ProtocolVersion() + ":" + name
}

// destroyR3Case is one server.destroy workflow over the fixture with counting
// resolvers, a counting adapter registry, and an operation copy to change.
type destroyR3Case struct {
	fixture    serverDestroyFixture
	workflow   *ServerDestroyWorkflow
	operation  store.OperationRecord
	profiles   *destroyR3Profiles
	backups    *destroyR3Backups
	authorizer *destroyR3Authorizer
	runtime    *destroyRuntimeOverride
}

func destroyR3NewCase(fixture serverDestroyFixture) *destroyR3Case {
	c := &destroyR3Case{
		fixture:    fixture,
		operation:  fixture.operation.Record,
		profiles:   &destroyR3Profiles{profile: ServerDestroyProfile{UnitRef: json.RawMessage(startTestUnit)}},
		backups:    &destroyR3Backups{backup: destroyR3Backup()},
		authorizer: &destroyR3Authorizer{},
		runtime:    &destroyRuntimeOverride{adapterRuntime: fixture.base.registry},
	}
	c.operation.Steps = append([]store.OperationStep(nil), c.operation.Steps...)
	workflow := *fixture.workflow
	workflow.profiles, workflow.backups, workflow.authorizer, workflow.adapters = c.profiles, c.backups, c.authorizer, c.runtime
	c.workflow = &workflow
	return c
}

// destroyR3WithServer rewrites the logical server the workflow loads.
func (c *destroyR3Case) destroyR3WithServer(change func(*store.LogicalServer)) {
	c.workflow.servers = &destroyStoreOverride{BoltStore: c.fixture.base.store, server: func(stored store.StoredLogicalServer) store.StoredLogicalServer {
		change(&stored.Record)
		return stored
	}}
}

func destroyR3WantError(t *testing.T, label string, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("%s = %v, want nil", label, err)
		}
		return
	}
	if err == nil || err.Error() != want {
		t.Fatalf("%s = %v, want exactly %q", label, err, want)
	}
}

func destroyR3CapabilityVersion(t *testing.T, fixture serverDestroyFixture, adapterID, name string) string {
	t.Helper()
	descriptor, err := fixture.base.registry.Descriptor(adapterID)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Name == name {
			return capability.Version
		}
	}
	t.Fatalf("adapter %s lacks %s", adapterID, name)
	return ""
}

// destroyR3StepSummary is the comparable form of a planned step.
type destroyR3StepSummary struct {
	stepID, stepType, adapterID, capabilityName, capabilityVersion string
	status                                                         protocolstate.StepStatus
	input, output, externalRef                                     string
	attempt                                                        int
	started                                                        bool
}

func destroyR3SummarizeStep(step store.OperationStep) destroyR3StepSummary {
	return destroyR3StepSummary{
		stepID: step.StepID, stepType: step.StepType, adapterID: step.AdapterID, capabilityName: step.CapabilityName,
		capabilityVersion: step.CapabilityVersion, status: step.Status, input: string(step.Input), output: string(step.Output),
		externalRef: string(step.ExternalOperationRef), attempt: step.Attempt, started: step.StartedAt != nil,
	}
}

// destroyR3Calls counts the resolver and registry calls a case made.
type destroyR3Calls struct{ backups, authorized, profiles, descriptors int }

func (c *destroyR3Case) destroyR3Calls() destroyR3Calls {
	return destroyR3Calls{backups: c.backups.calls, authorized: c.authorizer.calls, profiles: c.profiles.calls, descriptors: c.runtime.descriptorCalls}
}

func TestDestroyR3BuildPlanSteps(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	c := destroyR3NewCase(fixture)
	steps, err := c.workflow.BuildPlan(context.Background(), c.operation)
	if err != nil {
		t.Fatal(err)
	}
	instanceInput := `{"instance_ref":` + startTestInstance + `}`
	want := []store.OperationStep{
		{StepID: "inspect-instance", StepType: "hosting.instance.inspect", AdapterID: "hosting.fake", CapabilityName: "hosting.instance.inspect", Input: json.RawMessage(instanceInput)},
		{StepID: "inspect-runtime", StepType: "execution.unit.inspect", AdapterID: "execution.fake", CapabilityName: "execution.unit.inspect", Input: json.RawMessage(`{"unit_ref":` + startTestUnit + `}`)},
		{StepID: "destroy-instance", StepType: "hosting.instance.destroy", AdapterID: "hosting.fake", CapabilityName: "hosting.instance.destroy", Input: json.RawMessage(instanceInput)},
		{StepID: "verify-instance-absence", StepType: "hosting.instance.inspect", AdapterID: "hosting.fake", CapabilityName: "hosting.instance.inspect", Input: json.RawMessage(instanceInput)},
	}
	if len(steps) != len(want) {
		t.Fatalf("planned %d steps, want %d", len(steps), len(want))
	}
	for i, step := range steps {
		w := want[i]
		w.Status = protocolstate.StepPending
		w.CapabilityVersion = destroyR3CapabilityVersion(t, fixture, w.AdapterID, w.CapabilityName)
		if got, expected := destroyR3SummarizeStep(step), destroyR3SummarizeStep(w); got != expected {
			t.Fatalf("step %d = %#v, want %#v", i, got, expected)
		}
	}
	// One capability lookup per planned step, after one call to each resolver.
	if got := c.destroyR3Calls(); got != (destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 4}) {
		t.Fatalf("resolver calls = %+v, want one each and four descriptor lookups", got)
	}

	failed := destroyR3NewCase(fixture)
	failed.destroyR3WithServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerFailed })
	if _, err := failed.workflow.BuildPlan(context.Background(), failed.operation); err != nil {
		t.Fatalf("BuildPlan for a failed server = %v, want nil", err)
	}
}

func TestDestroyR3BuildPlanExactErrors(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	const initialized = "initialized server.destroy workflow and context are required"
	tests := []struct {
		name   string
		ctx    context.Context
		change func(*destroyR3Case)
		want   string
		// calls are the expected resolver call counts, proving which
		// stages ran before the error.
		calls destroyR3Calls
	}{
		{name: "nil context", ctx: nil, want: initialized},
		{name: "nil workflow", change: func(c *destroyR3Case) { c.workflow = nil }, want: initialized},
		{name: "missing servers", change: func(c *destroyR3Case) { c.workflow.servers = nil }, want: initialized},
		{name: "missing adapters", change: func(c *destroyR3Case) { c.workflow.adapters = nil }, want: initialized},
		{name: "missing schemas", change: func(c *destroyR3Case) { c.workflow.schemas = nil }, want: initialized},
		{name: "missing profiles", change: func(c *destroyR3Case) { c.workflow.profiles = nil }, want: initialized},
		{name: "missing backups", change: func(c *destroyR3Case) { c.workflow.backups = nil }, want: initialized},
		{name: "missing authorizer", change: func(c *destroyR3Case) { c.workflow.authorizer = nil }, want: initialized},
		{name: "ended context", ctx: ended, want: context.Canceled.Error()},
		{name: "another operation type", change: func(c *destroyR3Case) { c.operation.OperationType = "server.stop" }, want: destroyR3RequestRequired},
		{name: "undecodable parameters", change: func(c *destroyR3Case) {
			c.operation.RequestSnapshot.Parameters = json.RawMessage(`{"approval_id":7}`)
		}, want: "decode server.destroy parameters"},
		{name: "missing approval", change: func(c *destroyR3Case) {
			c.operation.RequestSnapshot.Parameters = json.RawMessage(fmt.Sprintf(`{"expected_instance_ref":%s,"backup_profile":%q,"verified_backup_id":%q}`,
				startTestInstance, destroyTestProfile, destroyTestBackupID))
		}, want: destroyR3RequestFields},
		{name: "unknown server", change: func(c *destroyR3Case) { c.operation.Target.ServerID = "server-missing" },
			want: "load logical server for destroy plan: " + store.ErrLogicalServerNotFound.Error()},
		{name: "target instance changed", change: func(c *destroyR3Case) { c.operation.Target.InstanceRef = json.RawMessage(otherDestroyInstance) },
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "unknown data generation", change: func(c *destroyR3Case) {
			c.destroyR3WithServer(func(server *store.LogicalServer) { server.DataGenerationStatus = "unknown" })
		}, want: destroyR3KnownGeneration},
		{name: "running server", change: func(c *destroyR3Case) {
			c.destroyR3WithServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerReady })
		}, want: "server.destroy requires a stopped or failed logical server"},
		{name: "destroying server", change: func(c *destroyR3Case) {
			c.destroyR3WithServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerDestroying })
		}, want: "server.destroy requires a stopped or failed logical server"},
		{name: "target mismatch is reported before the lifecycle", change: func(c *destroyR3Case) {
			c.operation.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
			c.destroyR3WithServer(func(server *store.LogicalServer) { server.LifecycleState = protocolstate.ServerReady })
		}, want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "backup resolver fails", change: func(c *destroyR3Case) { c.backups.err = errors.New("backup store offline") },
			want: "resolve verified backup for server.destroy: backup store offline", calls: destroyR3Calls{backups: 1}},
		{name: "backup integrity unverified", change: func(c *destroyR3Case) { c.backups.backup.IntegrityVerified = false }, want: destroyR3BackupMismatch, calls: destroyR3Calls{backups: 1}},
		{name: "backup for another server", change: func(c *destroyR3Case) { c.backups.backup.ServerID = "other" }, want: destroyR3BackupMismatch, calls: destroyR3Calls{backups: 1}},
		{name: "authorizer rejects", change: func(c *destroyR3Case) { c.authorizer.err = errors.New("approval expired") },
			want: "authorize exact server.destroy request: approval expired", calls: destroyR3Calls{backups: 1, authorized: 1}},
		{name: "profile resolver fails", change: func(c *destroyR3Case) { c.profiles.err = errors.New("profile missing") },
			want: "resolve trusted server destroy profile: profile missing", calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1}},
		{name: "profile for another execution adapter", change: func(c *destroyR3Case) {
			c.profiles.profile.UnitRef = json.RawMessage(`{"adapter_id":"execution.other","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper-start-test"}`)
		}, want: destroyR3ProfileMismatch, calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1}},
		{name: "hosting destroy capability missing", change: func(c *destroyR3Case) {
			c.runtime.descriptor = destroyWithoutCapability("hosting.instance.destroy")
		}, want: `adapter "hosting.fake" does not advertise required capability hosting.instance.destroy`, calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 3}},
		{name: "execution capability missing is reported before the destroy capability", change: func(c *destroyR3Case) {
			c.runtime.descriptor = func(call int, descriptor adapter.Descriptor, err error) (adapter.Descriptor, error) {
				descriptor, err = destroyWithoutCapability("hosting.instance.destroy")(call, descriptor, err)
				return destroyWithoutCapability("execution.unit.inspect")(call, descriptor, err)
			}
		}, want: `adapter "execution.fake" does not advertise required capability execution.unit.inspect`, calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 2}},
		{name: "verify capability descriptor fails last", change: func(c *destroyR3Case) {
			c.runtime.descriptor = func(call int, descriptor adapter.Descriptor, err error) (adapter.Descriptor, error) {
				if call == 4 {
					return adapter.Descriptor{}, errors.New("registry offline")
				}
				return descriptor, err
			}
		}, want: `resolve adapter "hosting.fake" for hosting.instance.inspect: registry offline`, calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 4}},
		{name: "runtime input rejected by its schema", change: func(c *destroyR3Case) {
			c.workflow.schemas = destroyR3Schemas{schemaRuntime: c.fixture.base.schemas, rejectURI: destroyR3URN(c.fixture, "execution:systemd-unit-inspect-input")}
		}, want: "planned inspect-runtime input does not match its declared capability schema: rejected by test schema", calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 4}},
		{name: "instance input rejected at the first step", change: func(c *destroyR3Case) {
			c.workflow.schemas = destroyR3Schemas{schemaRuntime: c.fixture.base.schemas, rejectURI: destroyR3URN(c.fixture, "hosting:instance-inspect-input")}
		}, want: "planned inspect-instance input does not match its declared capability schema: rejected by test schema", calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 4}},
		{name: "destroy input rejected by its schema", change: func(c *destroyR3Case) {
			c.workflow.schemas = destroyR3Schemas{schemaRuntime: c.fixture.base.schemas, rejectURI: destroyR3URN(c.fixture, "hosting:instance-destroy-input")}
		}, want: "planned destroy-instance input does not match its declared capability schema: rejected by test schema", calls: destroyR3Calls{backups: 1, authorized: 1, profiles: 1, descriptors: 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := destroyR3NewCase(fixture)
			if test.change != nil {
				test.change(c)
			}
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			steps, err := c.workflow.BuildPlan(ctx, c.operation)
			destroyR3WantError(t, "BuildPlan", err, test.want)
			if steps != nil {
				t.Fatalf("BuildPlan returned steps %#v with an error", steps)
			}
			if got := c.destroyR3Calls(); got != test.calls {
				t.Fatalf("resolver calls = %+v, want %+v", got, test.calls)
			}
		})
	}
}

func TestDestroyR3NewServerDestroyWorkflow(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	const want = "lifecycle store, adapter registry, schemas, destroy profile, verified backup resolver, and destroy authorizer are required"
	base := fixture.workflow
	for index := 0; index < 6; index++ {
		args := []any{base.servers, base.adapters, base.schemas, base.profiles, base.backups, base.authorizer}
		args[index] = nil
		servers, _ := args[0].(store.LifecycleOperationStore)
		adapters, _ := args[1].(adapterRuntime)
		schemas, _ := args[2].(schemaRuntime)
		profiles, _ := args[3].(ServerDestroyProfileResolver)
		backups, _ := args[4].(ServerDestroyBackupResolver)
		authorizer, _ := args[5].(ServerDestroyAuthorizer)
		workflow, err := NewServerDestroyWorkflow(servers, adapters, schemas, profiles, backups, authorizer)
		if workflow != nil {
			t.Fatalf("argument %d nil: workflow = %#v, want nil", index, workflow)
		}
		destroyR3WantError(t, fmt.Sprintf("NewServerDestroyWorkflow(argument %d nil)", index), err, want)
	}
	workflow, err := NewServerDestroyWorkflow(base.servers, base.adapters, base.schemas, base.profiles, base.backups, base.authorizer)
	if err != nil || workflow == nil || *workflow != *base {
		t.Fatalf("NewServerDestroyWorkflow = %#v, %v; want a workflow over every dependency", workflow, err)
	}
}

func TestDestroyR3ServerDestroyRequest(t *testing.T) {
	const version = "1.0.0"
	parameters, err := json.Marshal(destroyR3Parameters())
	if err != nil {
		t.Fatal(err)
	}
	valid := func() store.OperationRecord {
		return store.OperationRecord{
			OperationType: "server.destroy", ProtocolVersion: version,
			Target:          store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID},
			RequestSnapshot: store.RequestSnapshot{OperationType: "server.destroy", Parameters: parameters},
		}
	}
	withParameters := func(raw string) func(*store.OperationRecord, *string) {
		return func(o *store.OperationRecord, _ *string) { o.RequestSnapshot.Parameters = json.RawMessage(raw) }
	}
	tests := []struct {
		name   string
		change func(*store.OperationRecord, *string)
		want   string
	}{
		{name: "valid"},
		{name: "another operation type", change: func(o *store.OperationRecord, _ *string) { o.OperationType = "server.stop" }, want: destroyR3RequestRequired},
		{name: "another protocol version", change: func(o *store.OperationRecord, _ *string) { o.ProtocolVersion = "0.9.0" }, want: destroyR3RequestRequired},
		{name: "no loaded protocol version", change: func(o *store.OperationRecord, v *string) { o.ProtocolVersion, *v = "", "" }, want: destroyR3RequestRequired},
		{name: "no server", change: func(o *store.OperationRecord, _ *string) { o.Target.ServerID = "" }, want: destroyR3RequestRequired},
		{name: "no environment", change: func(o *store.OperationRecord, _ *string) { o.Target.EnvironmentID = "" }, want: destroyR3RequestRequired},
		{name: "snapshot for another type", change: func(o *store.OperationRecord, _ *string) { o.RequestSnapshot.OperationType = "server.stop" }, want: destroyR3RequestRequired},
		{name: "array parameters", change: withParameters(`[]`), want: destroyR3RequestRequired},
		{name: "null parameters", change: withParameters(`null`), want: destroyR3RequestRequired},
		{name: "no parameters", change: withParameters(``), want: destroyR3RequestRequired},
		{name: "mistyped parameter", change: withParameters(`{"approval_id":7}`), want: "decode server.destroy parameters"},
		{name: "no backup profile", change: withParameters(fmt.Sprintf(`{"expected_instance_ref":%s,"verified_backup_id":"b","approval_id":"a"}`, startTestInstance)), want: destroyR3RequestFields},
		{name: "no backup ID", change: withParameters(fmt.Sprintf(`{"expected_instance_ref":%s,"backup_profile":"p","approval_id":"a"}`, startTestInstance)), want: destroyR3RequestFields},
		{name: "no approval", change: withParameters(fmt.Sprintf(`{"expected_instance_ref":%s,"backup_profile":"p","verified_backup_id":"b"}`, startTestInstance)), want: destroyR3RequestFields},
		{name: "no expected instance", change: withParameters(`{"backup_profile":"p","verified_backup_id":"b","approval_id":"a"}`), want: destroyR3RequestFields},
		{name: "array expected instance", change: withParameters(`{"expected_instance_ref":[1],"backup_profile":"p","verified_backup_id":"b","approval_id":"a"}`), want: destroyR3RequestFields},
		{name: "null expected instance", change: withParameters(`{"expected_instance_ref":null,"backup_profile":"p","verified_backup_id":"b","approval_id":"a"}`), want: destroyR3RequestFields},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation, loaded := valid(), version
			if test.change != nil {
				test.change(&operation, &loaded)
			}
			parameters, err := serverDestroyRequest(operation, loaded)
			destroyR3WantError(t, "serverDestroyRequest", err, test.want)
			if test.want == "" && !destroyR3SameParameters(t, parameters, destroyR3Parameters()) {
				t.Fatalf("parameters = %#v", parameters)
			}
		})
	}
}

func destroyR3SameParameters(t *testing.T, got, want serverDestroyParameters) bool {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	return string(gotJSON) == string(wantJSON)
}

func TestDestroyR3ValidateServerDestroyTarget(t *testing.T) {
	operation := store.OperationRecord{Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance)}}
	server := store.LogicalServer{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance),
		DataGenerationStatus: "known", DataGenerationID: destroyTestGeneration}
	mismatch := store.ErrLogicalServerTargetMismatch.Error()
	tests := []struct {
		name   string
		change func(*store.OperationRecord, *serverDestroyParameters, *store.LogicalServer)
		want   string
	}{
		{name: "exact target"},
		{name: "another server", change: func(_ *store.OperationRecord, _ *serverDestroyParameters, s *store.LogicalServer) {
			s.ServerID = "other"
		}, want: mismatch},
		{name: "another environment", change: func(_ *store.OperationRecord, _ *serverDestroyParameters, s *store.LogicalServer) {
			s.EnvironmentID = "other"
		}, want: mismatch},
		{name: "no current instance", change: func(o *store.OperationRecord, p *serverDestroyParameters, s *store.LogicalServer) {
			s.InstanceRef, o.Target.InstanceRef, p.ExpectedInstanceRef = nil, nil, nil
		}, want: mismatch},
		{name: "target names another instance", change: func(o *store.OperationRecord, _ *serverDestroyParameters, _ *store.LogicalServer) {
			o.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
		}, want: mismatch},
		{name: "parameters name another instance", change: func(_ *store.OperationRecord, p *serverDestroyParameters, _ *store.LogicalServer) {
			p.ExpectedInstanceRef = json.RawMessage(otherDestroyInstance)
		}, want: mismatch},
		{name: "reordered instance JSON", change: func(o *store.OperationRecord, p *serverDestroyParameters, _ *store.LogicalServer) {
			reordered := json.RawMessage(`{"external_id":"vm-start-test","resource_kind":"instance","environment_id":"staging","adapter_id":"hosting.fake"}`)
			o.Target.InstanceRef, p.ExpectedInstanceRef = reordered, reordered
		}},
		{name: "unknown generation", change: func(_ *store.OperationRecord, _ *serverDestroyParameters, s *store.LogicalServer) {
			s.DataGenerationStatus = "unknown"
		}, want: destroyR3KnownGeneration},
		{name: "no generation ID", change: func(_ *store.OperationRecord, _ *serverDestroyParameters, s *store.LogicalServer) {
			s.DataGenerationID = ""
		}, want: destroyR3KnownGeneration},
		{name: "mismatch is reported before the generation", change: func(_ *store.OperationRecord, _ *serverDestroyParameters, s *store.LogicalServer) {
			s.ServerID, s.DataGenerationStatus = "other", "unknown"
		}, want: mismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o, p, s := operation, destroyR3Parameters(), server
			if test.change != nil {
				test.change(&o, &p, &s)
			}
			err := validateServerDestroyTarget(o, p, s)
			destroyR3WantError(t, "validateServerDestroyTarget", err, test.want)
			if test.want == mismatch && !errors.Is(err, store.ErrLogicalServerTargetMismatch) {
				t.Fatalf("target mismatch is not the store sentinel: %v", err)
			}
		})
	}
}

func TestDestroyR3ValidateServerDestroyProfile(t *testing.T) {
	server := store.LogicalServer{EnvironmentID: startTestEnvID, ExecutionAdapterID: "execution.fake"}
	tests := []struct {
		name, unit, want string
	}{
		{name: "exact unit", unit: startTestUnit},
		{name: "no unit", unit: "", want: destroyR3ProfileMismatch},
		{name: "undecodable unit", unit: `{"adapter_id":`, want: destroyR3ProfileMismatch},
		{name: "array unit", unit: `[]`, want: destroyR3ProfileMismatch},
		{name: "null unit", unit: `null`, want: destroyR3ProfileMismatch},
		{name: "another adapter", unit: `{"adapter_id":"execution.other","environment_id":"staging","resource_kind":"systemd-unit","external_id":"u"}`, want: destroyR3ProfileMismatch},
		{name: "another environment", unit: `{"adapter_id":"execution.fake","environment_id":"prod","resource_kind":"systemd-unit","external_id":"u"}`, want: destroyR3ProfileMismatch},
		{name: "another kind", unit: `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"container","external_id":"u"}`, want: destroyR3ProfileMismatch},
		{name: "no external ID", unit: `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":""}`, want: destroyR3ProfileMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var unit json.RawMessage
			if test.unit != "" {
				unit = json.RawMessage(test.unit)
			}
			destroyR3WantError(t, "validateServerDestroyProfile", validateServerDestroyProfile(server, ServerDestroyProfile{UnitRef: unit}), test.want)
		})
	}
}

func TestDestroyR3VerifiedBackup(t *testing.T) {
	server := store.LogicalServer{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance), DataGenerationID: destroyTestGeneration}
	tests := []struct {
		name   string
		change func(*VerifiedServerBackup)
		err    error
		want   string
	}{
		{name: "exact backup"},
		{name: "resolver fails", err: errors.New("lookup failed"), want: "resolve verified backup for server.destroy: lookup failed"},
		{name: "integrity unverified", change: func(b *VerifiedServerBackup) { b.IntegrityVerified = false }, want: destroyR3BackupMismatch},
		{name: "another backup ID", change: func(b *VerifiedServerBackup) { b.BackupID = "other" }, want: destroyR3BackupMismatch},
		{name: "another server", change: func(b *VerifiedServerBackup) { b.ServerID = "other" }, want: destroyR3BackupMismatch},
		{name: "another environment", change: func(b *VerifiedServerBackup) { b.EnvironmentID = "other" }, want: destroyR3BackupMismatch},
		{name: "another storage profile", change: func(b *VerifiedServerBackup) { b.StorageProfile = "other" }, want: destroyR3BackupMismatch},
		{name: "another generation", change: func(b *VerifiedServerBackup) { b.DataGenerationID = "other" }, want: destroyR3BackupMismatch},
		{name: "another source instance", change: func(b *VerifiedServerBackup) { b.SourceInstanceRef = json.RawMessage(otherDestroyInstance) }, want: destroyR3BackupMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backups := &destroyR3Backups{backup: destroyR3Backup(), err: test.err}
			if test.change != nil {
				test.change(&backups.backup)
			}
			workflow := &ServerDestroyWorkflow{backups: backups}
			backup, err := workflow.verifiedBackup(context.Background(), server, destroyR3Parameters())
			destroyR3WantError(t, "verifiedBackup", err, test.want)
			if test.want != "" && (backup.BackupID != "" || backup.SourceInstanceRef != nil || backup.IntegrityVerified) {
				t.Fatalf("rejected backup returned %#v, want zero", backup)
			}
			if test.want == "" && backup.BackupID != destroyTestBackupID {
				t.Fatalf("verified backup = %#v", backup)
			}
		})
	}
}
