package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var updateProvisionPlanGolden = flag.Bool("update-provision-plan-golden", false, "rewrite testdata/server_provision_plans.golden.json")

// provisionR3ProfileResolver returns a fixed profile or error.
type provisionR3ProfileResolver struct {
	profile ServerProvisionProfile
	err     error
}

func (r provisionR3ProfileResolver) ResolveServerProvision(context.Context, store.LogicalServer, json.RawMessage) (ServerProvisionProfile, error) {
	return r.profile, r.err
}

// provisionR3BackupResolver fails every lookup with err.
type provisionR3BackupResolver struct {
	*staticProvisionBackupResolver
	err error
}

func (r provisionR3BackupResolver) ResolveServerProvisionBackup(ctx context.Context, server store.LogicalServer, profile, backup string) (VerifiedProvisionBackup, error) {
	if r.err != nil {
		return VerifiedProvisionBackup{}, r.err
	}
	return r.staticProvisionBackupResolver.ResolveServerProvisionBackup(ctx, server, profile, backup)
}

// provisionR3Servers reports the stored server with change applied, or err.
type provisionR3Servers struct {
	store.LifecycleOperationStore
	change func(*store.LogicalServer)
	err    error
}

func (r provisionR3Servers) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	if r.err != nil {
		return store.StoredLogicalServer{}, r.err
	}
	stored, err := r.LifecycleOperationStore.GetLogicalServer(environmentID, serverID)
	if err == nil && r.change != nil {
		r.change(&stored.Record)
	}
	return stored, err
}

// provisionR3PinBackupTimes replaces the wall-clock times in the restore
// fixture's backup so the plan is byte-stable.
func provisionR3PinBackupTimes(t *testing.T, resolver *staticProvisionBackupResolver) {
	t.Helper()
	document := decodeBackupDocument(t, resolver.result)
	document.manifest["created_at"] = "2026-11-01T00:00:00Z"
	document.operationData["verified_at"] = "2026-11-01T00:05:00Z"
	resolver.result = document.encode(t)
}

// provisionR3RestoreFixture is the restore fixture with stable backup times.
func provisionR3RestoreFixture(t *testing.T) (provisionPlanFixture, *staticProvisionBackupResolver) {
	t.Helper()
	f := newProvisionPlanFixture(t)
	resolver, _ := provisionRestoreSetup(t, &f)
	provisionR3PinBackupTimes(t, resolver)
	return f, resolver
}

func provisionR3WithParameters(t *testing.T, raw json.RawMessage, change func(map[string]any)) json.RawMessage {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	change(request)
	return mustMarshal(t, request)
}

// provisionR3Rewire rebuilds the fixture workflow with another profile and,
// when given, backup resolver.
func provisionR3Rewire(t *testing.T, f *provisionPlanFixture, profiles ServerProvisionProfileResolver, backups ...ServerProvisionBackupResolver) {
	t.Helper()
	var err error
	f.workflow, err = NewServerProvisionWorkflow(f.store, f.store, f.registry, f.schemas, profiles, time.Minute, backups...)
	if err != nil {
		t.Fatal(err)
	}
}

// TestServerProvisionR3BuildPlanSnapshot pins every planned step (ID,
// capability, exact input bytes and bindings) for each plan shape.
func TestServerProvisionR3BuildPlanSnapshot(t *testing.T) {
	plans := map[string][]store.OperationStep{}
	fresh := newProvisionPlanFixture(t)
	steps, err := fresh.workflow.BuildPlan(context.Background(), fresh.operation)
	if err != nil {
		t.Fatal(err)
	}
	plans["fresh"] = steps

	optional := newProvisionPlanFixture(t)
	optional.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, optional.request, func(r map[string]any) {
		r["network_profile"] = "private-net"
		r["firewall_policy_ref"] = "fw-1"
		r["ssh_key_refs"] = []string{"key-a", "key-b"}
		r["labels"] = map[string]string{"team": "games"}
	})
	if plans["fresh with optional create fields"], err = optional.workflow.BuildPlan(context.Background(), optional.operation); err != nil {
		t.Fatal(err)
	}

	emptyLabels := newProvisionPlanFixture(t)
	emptyLabels.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, emptyLabels.request, func(r map[string]any) {
		r["ssh_key_refs"] = []string{}
		r["labels"] = map[string]string{}
	})
	if plans["fresh with empty key list and labels"], err = emptyLabels.workflow.BuildPlan(context.Background(), emptyLabels.operation); err != nil {
		t.Fatal(err)
	}

	restore, _ := provisionR3RestoreFixture(t)
	if plans["restore"], err = restore.workflow.BuildPlan(context.Background(), restore.operation); err != nil {
		t.Fatal(err)
	}

	got, err := json.MarshalIndent(plans, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "server_provision_plans.golden.json")
	if *updateProvisionPlanGolden {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update-provision-plan-golden to create): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("BuildPlan output differs from %s:\n%s", path, got)
	}
}

// TestServerProvisionR3BuildPlanErrors pins each BuildPlan rejection, in the
// order the checks run.
func TestServerProvisionR3BuildPlanErrors(t *testing.T) {
	const (
		uninitialized = "initialized server.provision workflow and context are required"
		notProvision  = "server.provision operation and loaded protocol version are required"
	)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	type setup struct {
		f   *provisionPlanFixture
		ctx context.Context
	}
	restoreProfile := func(change func(*ServerProvisionProfile)) func(*testing.T, *setup) {
		return func(t *testing.T, s *setup) {
			restore, resolver := provisionR3RestoreFixture(t)
			profile := restore.profile
			change(&profile)
			provisionR3Rewire(t, &restore, provisionR3ProfileResolver{profile: profile}, resolver)
			s.f = &restore
		}
	}
	cases := []struct {
		name  string
		setup func(*testing.T, *setup)
		want  string
	}{
		{"nil context", func(_ *testing.T, s *setup) { s.ctx = nil }, uninitialized},
		{"nil workflow", func(_ *testing.T, s *setup) { s.f.workflow = nil }, uninitialized},
		{"no servers", func(_ *testing.T, s *setup) { s.f.workflow.servers = nil }, uninitialized},
		{"no capacity", func(_ *testing.T, s *setup) { s.f.workflow.capacity = nil }, uninitialized},
		{"no adapters", func(_ *testing.T, s *setup) { s.f.workflow.adapters = nil }, uninitialized},
		{"no schemas", func(_ *testing.T, s *setup) { s.f.workflow.schemas = nil }, uninitialized},
		{"no profiles", func(_ *testing.T, s *setup) { s.f.workflow.profiles = nil }, uninitialized},
		{"cancelled", func(_ *testing.T, s *setup) { s.ctx = cancelled }, context.Canceled.Error()},
		{"wrong type", func(_ *testing.T, s *setup) { s.f.operation.OperationType = "server.start" }, notProvision},
		{"wrong protocol", func(_ *testing.T, s *setup) { s.f.operation.ProtocolVersion = "0.0.1" }, notProvision},
		{"no server target", func(_ *testing.T, s *setup) { s.f.operation.Target.ServerID = "" }, notProvision},
		{"no environment target", func(_ *testing.T, s *setup) { s.f.operation.Target.EnvironmentID = "" }, notProvision},
		{"wrong snapshot type", func(_ *testing.T, s *setup) { s.f.operation.RequestSnapshot.OperationType = "server.start" }, notProvision},
		{"non-object parameters", func(_ *testing.T, s *setup) { s.f.operation.RequestSnapshot.Parameters = json.RawMessage(`[]`) }, notProvision},
		{"undecodable parameters", func(_ *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":5}`)
		}, "decode server.provision parameters"},
		{"request server differs", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) { r["server_id"] = "other" })
		}, "server.provision request identity does not match its operation target"},
		{"request environment differs", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) { r["environment_id"] = "production" })
		}, "server.provision request identity does not match its operation target"},
		{"unknown server", func(t *testing.T, s *setup) {
			s.f.operation.Target.ServerID = "other"
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) { r["server_id"] = "other" })
		}, "load logical server for provision plan: " + store.ErrLogicalServerNotFound.Error()},
		{"server not unprovisioned", func(_ *testing.T, s *setup) {
			s.f.workflow.servers = provisionR3Servers{LifecycleOperationStore: s.f.workflow.servers, change: func(server *store.LogicalServer) {
				server.LifecycleState = protocolstate.ServerProvisioning
			}}
		}, "server.provision requires an unprovisioned logical server without an attached instance"},
		{"server has an instance", func(_ *testing.T, s *setup) {
			s.f.workflow.servers = provisionR3Servers{LifecycleOperationStore: s.f.workflow.servers, change: func(server *store.LogicalServer) {
				server.InstanceRef = json.RawMessage(provisionTestInstance)
			}}
		}, "server.provision requires an unprovisioned logical server without an attached instance"},
		{"game adapter differs", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) {
				r["game"] = map[string]any{"adapter_id": "game.other", "release_ref": "paper-test-1"}
			})
		}, "server.provision request does not match the persisted game and execution definition"},
		{"execution adapter differs", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) {
				r["execution"] = map[string]any{"adapter_id": "execution.other", "profile": "paper-test"}
			})
		}, "server.provision request does not match the persisted game and execution definition"},
		{"execution profile differs", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) {
				r["execution"] = map[string]any{"adapter_id": "execution.fake", "profile": "other"}
			})
		}, "server.provision request does not match the persisted game and execution definition"},
		{"profile resolver fails", func(t *testing.T, s *setup) {
			provisionR3Rewire(t, s.f, provisionR3ProfileResolver{err: errors.New("catalog offline")})
		}, "resolve trusted server provision profile: catalog offline"},
		{"profile does not satisfy request", func(t *testing.T, s *setup) {
			profile := s.f.profile
			profile.RegionID = "ams"
			provisionR3Rewire(t, s.f, provisionR3ProfileResolver{profile: profile})
		}, "trusted server provision profile does not exactly satisfy the selected request"},
		{"restore without backup resolver", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) { r["restore_backup_id"] = "backup-123" })
		}, "server.provision restore requires a trusted backup resolver"},
		{"restore profile without storage profile", restoreProfile(func(p *ServerProvisionProfile) { p.BackupStorageProfileID = "" }),
			"trusted provision profile must match the server storage profile and pin target release metadata and required restore checks"},
		{"restore release metadata invalid", restoreProfile(func(p *ServerProvisionProfile) {
			p.GameReleaseDescription = json.RawMessage(`{"game_adapter_id":"game.paper"}`)
		}),
			"trusted target game release metadata failed schema validation"},
		{"restore release for another release", restoreProfile(func(p *ServerProvisionProfile) {
			p.GameReleaseDescription = provisionR3MustEdit(p.GameReleaseDescription, "release_id", "paper-test-2")
		}), "trusted target release metadata does not match the selected game adapter and release"},
		{"restore release for another adapter", restoreProfile(func(p *ServerProvisionProfile) {
			p.GameReleaseDescription = provisionR3MustEdit(p.GameReleaseDescription, "game_adapter_id", "game.other")
		}), "trusted target release metadata does not match the selected game adapter and release"},
		{"restore backup resolver fails", func(t *testing.T, s *setup) {
			restore, resolver := provisionR3RestoreFixture(t)
			provisionR3Rewire(t, &restore, provisionR3ProfileResolver{profile: restore.profile}, provisionR3BackupResolver{resolver, errors.New("backup missing")})
			s.f = &restore
		}, "resolve verified server backup: backup missing"},
		{"restore backup unverified", func(t *testing.T, s *setup) {
			restore, resolver := provisionR3RestoreFixture(t)
			document := decodeBackupDocument(t, resolver.result)
			document.objectRef["storage_profile_id"] = "other-storage"
			resolver.result = document.encode(t)
			s.f = &restore
		}, "selected backup contains an invalid or cross-profile object reference"},
		{"hosting adapter unknown", func(t *testing.T, s *setup) {
			s.f.operation.RequestSnapshot.Parameters = provisionR3WithParameters(t, s.f.request, func(r map[string]any) { r["hosting_adapter_id"] = "hosting.missing" })
		}, `resolve adapter "hosting.missing" for hosting.instance.create: adapter is not registered`},
		{"readiness set undeclared", func(t *testing.T, s *setup) {
			profile := s.f.profile
			profile.ReadinessCheckSetID = "paper.test:other"
			provisionR3Rewire(t, s.f, provisionR3ProfileResolver{profile: profile})
		}, "trusted readiness profile does not match an exact game-adapter release, environment, set, and check-version declaration"},
		{"unit checksum not encodable", func(t *testing.T, s *setup) {
			profile := s.f.profile
			profile.ArtifactChecksum = json.RawMessage(`{`)
			provisionR3Rewire(t, s.f, provisionR3ProfileResolver{profile: profile})
		}, "encode create-unit input: "},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newProvisionPlanFixture(t)
			s := &setup{f: &f, ctx: context.Background()}
			test.setup(t, s)
			steps, err := s.f.workflow.BuildPlan(s.ctx, s.f.operation)
			if steps != nil {
				t.Fatalf("BuildPlan returned steps with error %v", err)
			}
			if test.name == "unit checksum not encodable" {
				// The JSON encoder can report a different Go type name across toolchains.
				got := provisionErrText(err)
				if !strings.HasPrefix(got, test.want) || !strings.HasSuffix(got, "unexpected end of JSON input") {
					t.Fatalf("BuildPlan error = %q, want a malformed JSON error from create-unit encoding", got)
				}
				return
			}
			if got := provisionErrText(err); got != test.want {
				t.Fatalf("BuildPlan error = %q, want %q", got, test.want)
			}
		})
	}
}

// TestServerProvisionR3ValidateProfile pins every rule of
// validateProvisionProfile against the fixture request.
func TestServerProvisionR3ValidateProfile(t *testing.T) {
	const (
		unsatisfied = "trusted server provision profile does not exactly satisfy the selected request"
		unbound     = "trusted provision unit reference does not bind the configured execution adapter and environment"
	)
	f := newProvisionPlanFixture(t)
	stored, err := f.store.GetLogicalServer(provisionTestEnv, provisionTestServer)
	if err != nil {
		t.Fatal(err)
	}
	var request provisionRequest
	if err := json.Unmarshal(f.request, &request); err != nil {
		t.Fatal(err)
	}
	unit := func(field, value string) func(*ServerProvisionProfile) {
		return func(p *ServerProvisionProfile) {
			p.UnitRef = provisionR3MustEdit(p.UnitRef, field, value)
		}
	}
	cases := []struct {
		name   string
		change func(*ServerProvisionProfile)
		want   string
	}{
		{"valid", func(*ServerProvisionProfile) {}, ""},
		{"no provider", func(p *ServerProvisionProfile) { p.Provider = "" }, unsatisfied},
		{"no capacity group", func(p *ServerProvisionProfile) { p.CapacityGroup = "" }, unsatisfied},
		{"zero capacity limit", func(p *ServerProvisionProfile) { p.CapacityLimit = 0 }, unsatisfied},
		{"zero group limit", func(p *ServerProvisionProfile) { p.CapacityGroupLimit = 0 }, unsatisfied},
		{"no region", func(p *ServerProvisionProfile) { p.RegionID = "" }, unsatisfied},
		{"other region", func(p *ServerProvisionProfile) { p.RegionID = "ams" }, unsatisfied},
		{"no plan", func(p *ServerProvisionProfile) { p.PlanID = "" }, unsatisfied},
		{"other plan", func(p *ServerProvisionProfile) { p.PlanID = "vc2-2c" }, unsatisfied},
		{"os not object", func(p *ServerProvisionProfile) { p.OS = json.RawMessage(`"debian"`) }, unsatisfied},
		{"other os", func(p *ServerProvisionProfile) { p.OS = json.RawMessage(`{"provider_os_id":"debian-12"}`) }, unsatisfied},
		{"bootstrap not object", func(p *ServerProvisionProfile) { p.Bootstrap = json.RawMessage(`[]`) }, unsatisfied},
		{"other bootstrap", func(p *ServerProvisionProfile) { p.Bootstrap = json.RawMessage(`{"revision":"other"}`) }, unsatisfied},
		{"no release", func(p *ServerProvisionProfile) { p.GameReleaseID = "" }, unsatisfied},
		{"other release", func(p *ServerProvisionProfile) { p.GameReleaseID = "paper-test-2" }, unsatisfied},
		{"no artifact", func(p *ServerProvisionProfile) { p.ArtifactURI = "" }, unsatisfied},
		{"no checksum", func(p *ServerProvisionProfile) { p.ArtifactChecksum = nil }, unsatisfied},
		{"no readiness set", func(p *ServerProvisionProfile) { p.ReadinessCheckSetID = "" }, unsatisfied},
		{"no readiness checks", func(p *ServerProvisionProfile) { p.RequiredChecks = nil }, unsatisfied},
		{"unit undecodable", func(p *ServerProvisionProfile) { p.UnitRef = json.RawMessage(`{"adapter_id":5}`) }, unbound},
		{"unit missing", func(p *ServerProvisionProfile) { p.UnitRef = nil }, unbound},
		{"unit null", func(p *ServerProvisionProfile) { p.UnitRef = json.RawMessage(`null`) }, unbound},
		{"unit other adapter", unit("adapter_id", "execution.other"), unbound},
		{"unit other environment", unit("environment_id", "production"), unbound},
		{"unit no kind", unit("resource_kind", ""), unbound},
		{"unit no external id", unit("external_id", ""), unbound},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			profile := f.profile
			test.change(&profile)
			if got := provisionErrText(validateProvisionProfile(stored.Record, request, profile)); got != test.want {
				t.Fatalf("validateProvisionProfile = %q, want %q", got, test.want)
			}
		})
	}
}
