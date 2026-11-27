package controllerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// runtimeR6ExitLogEnv names a file the helper adapter child appends its
// reported adapter ID to when it exits, so tests can see which processes were
// stopped and in which order.
const runtimeR6ExitLogEnv = "GSC_CONTROLLERRUNTIME_EXIT_LOG"

// runtimeR6LogExit records the helper child's exit when the test asked for it.
func runtimeR6LogExit(adapterID string) error {
	path := os.Getenv(runtimeR6ExitLogEnv)
	if path == "" {
		return nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(file, adapterID); err != nil {
		_ = file.Close() // the write error is the one worth reporting
		return err
	}
	return file.Close()
}

// runtimeR6Process is one process adapter: its profile ID and the ID its
// child reports.
type runtimeR6Process struct{ id, reports string }

// runtimeR6ConfigText returns a configuration without credential-using
// adapters. With no processes, execution.local is declared only; otherwise
// each process is a process adapter whose child logs its exit to exitLog.
func runtimeR6ConfigText(f fixture, exitLog string, processes ...runtimeR6Process) string {
	var adapters strings.Builder
	if len(processes) == 0 {
		adapters.WriteString("  - id: execution.local\n    category: execution\n")
	}
	for _, process := range processes {
		fmt.Fprintf(&adapters, `  - id: %s
    category: execution
    implementation: process
    process:
      path: %s
      args: ["-test.run=^TestHelperRuntimeAdapter$"]
      env: [%s=%s, %s=%s]
`, process.id, f.executable, childAdapterEnv, process.reports, runtimeR6ExitLogEnv, exitLog)
	}
	return `schema_version: 1
controller:
  mode: local
  state_store:
    type: bbolt
    path: ./state/controller.db
  defaults:
    operation_timeout: 1m
    retry_limit: 2
  audit:
    enabled: true
    destination: local
adapters:
  - id: hosting.local
    category: hosting
  - id: storage.local
    category: storage
` + adapters.String() + `  - id: game.paper
    category: game
environments:
  - id: production
    classification: production
    production_backup_access: read_write
  - id: staging
    classification: staging
    production_backup_access: read_only
servers:
  - id: staging-server
    environment_id: staging
    adapters:
      hosting: hosting.local
      storage: storage.local
      execution: execution.local
      game: game.paper
    lifecycle:
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
`
}

// runtimeR6ExitLog returns the exit log path inside the fixture.
func runtimeR6ExitLog(f fixture) string { return filepath.Join(f.dir, "exits.log") }

// runtimeR6Exits returns the adapter IDs whose child exited, in exit order.
func runtimeR6Exits(t *testing.T, f fixture) string {
	t.Helper()
	data, err := os.ReadFile(runtimeR6ExitLog(f))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// runtimeR6AssertStoreReleased fails unless the state store can be opened
// again; bbolt would time out on a lock a failed build left held.
func runtimeR6AssertStoreReleased(t *testing.T, f fixture) {
	t.Helper()
	database, err := store.OpenBolt(filepath.Join(f.dir, "state", "controller.db"))
	if err != nil {
		t.Fatalf("store was left open after a failed build: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

// runtimeR6AssertNoState fails if the build created the state directory.
func runtimeR6AssertNoState(t *testing.T, f fixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.dir, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state directory exists after an early failure: %v", err)
	}
}

// runtimeR6AssertBuildError checks the exact error of a failed build.
func runtimeR6AssertBuildError(t *testing.T, runtime *Runtime, err error, want string) {
	t.Helper()
	if runtime != nil {
		t.Fatal("a failed build returned a runtime")
	}
	if err == nil || err.Error() != want {
		t.Fatalf("Build error = %v, want %q", err, want)
	}
}

// runtimeR6DefineParameters are valid server.define parameters for the
// operation runtimeR6AcceptDefine stores.
var runtimeR6DefineParameters = json.RawMessage(`{"server_id":"server-define","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)

// runtimeR6AcceptDefine stores an accepted server.define operation.
func runtimeR6AcceptDefine(t *testing.T, operations *store.BoltStore, protocolVersion string, parameters json.RawMessage) {
	t.Helper()
	record := store.OperationRecord{OperationID: "define-operation", OperationType: "server.define", ProtocolVersion: protocolVersion,
		Target: store.OperationTarget{ServerID: "server-define", EnvironmentID: "staging"}, ActorID: "operator-1", IdempotencyKey: "define-key",
		RequestFingerprint: "sha256:" + strings.Repeat("a", 64), RequestSnapshot: store.RequestSnapshot{OperationType: "server.define", Parameters: parameters}}
	proof := store.IdempotencyProof{ActiveKeyVersion: "test-v1", Candidates: []store.IdempotencyVerifier{{KeyVersion: "test-v1",
		Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	if _, _, err := operations.AcceptOperation(record, proof, []string{store.LogicalServerLockScope("staging", "server-define")}); err != nil {
		t.Fatal(err)
	}
}

// runtimeR6SeedOperation leaves one unfinished operation in the state store.
func runtimeR6SeedOperation(t *testing.T, f fixture) {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.OpenBolt(filepath.Join(f.dir, "state", "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeR6AcceptDefine(t, operations, schemas.ProtocolVersion(), runtimeR6DefineParameters)
	if err := operations.Close(); err != nil {
		t.Fatal(err)
	}
}

// runtimeR6IdempotencyKeys configures one idempotency key version.
func runtimeR6IdempotencyKeys(value *config.Config) {
	value.Controller.IdempotencyKeys = config.IdempotencyKeys{Active: "v1", Versions: map[string]string{"v1": "systemd-credential://idempotency-v1"}}
}

func TestRuntimeR6BuildRequiresContextAndControllerVersion(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f)))
	const want = "context and controller version are required"
	var missing context.Context
	runtime, err := Build(missing, value, f.configPath, Options{ControllerVersion: "test-build"})
	runtimeR6AssertBuildError(t, runtime, err, want)
	runtime, err = Build(context.Background(), value, f.configPath, Options{})
	runtimeR6AssertBuildError(t, runtime, err, want)
	runtimeR6AssertNoState(t, f)
}

func TestRuntimeR6BuildValidatesBeforeOpeningTheStore(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f), runtimeR6Process{"execution.local", "execution.local"}))
	notExecutable := filepath.Join(f.dir, "adapter")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, ok := config.SettingsOf[*ProcessSettings](value.Adapters[2])
	if !ok {
		t.Fatal("execution.local has no process settings")
	}
	settings.Path = notExecutable
	runtime, err := f.build(t, value, f.credentials)
	runtimeR6AssertBuildError(t, runtime, err, "adapter configuration is invalid:\n - adapter execution.local: process.path is not an executable file")
	runtimeR6AssertNoState(t, f)
	if exits := runtimeR6Exits(t, f); exits != "" {
		t.Fatalf("processes ran before validation: %q", exits)
	}
}

func TestRuntimeR6BuildStoreFailureStartsNoAdapter(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f), runtimeR6Process{"execution.local", "execution.local"}))
	blocker := filepath.Join(f.dir, "state")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := f.build(t, value, f.credentials)
	runtimeR6AssertBuildError(t, runtime, err, "create operation store directory: mkdir "+blocker+": not a directory")
	if exits := runtimeR6Exits(t, f); exits != "" {
		t.Fatalf("processes ran before the store opened: %q", exits)
	}
}

func TestRuntimeR6BuildFailuresReleaseEverythingBuilt(t *testing.T) {
	local := runtimeR6Process{"execution.local", "execution.local"}
	for name, test := range map[string]struct {
		processes   []runtimeR6Process
		mutate      func(*config.Config)
		credentials func(fixture) string
		want        string
		exits       string
	}{
		"monitoring policy before adapters": {
			processes: []runtimeR6Process{local},
			mutate:    func(value *config.Config) { value.Controller.Monitoring.Proxy.CheckInterval = "soon" },
			want:      "controller.monitoring.proxy: check_interval must be a duration",
		},
		"second adapter stops both processes in reverse order": {
			processes: []runtimeR6Process{local, {"execution.second", "execution.other"}},
			want:      `adapter execution.second: process reported identity "execution.other" (execution), not the configured one`,
			exits:     "execution.other\nexecution.local\n",
		},
		"operation timeout": {
			processes: []runtimeR6Process{local},
			mutate:    func(value *config.Config) { value.Controller.Defaults.OperationTimeout = "soon" },
			want:      "controller.defaults.operation_timeout is invalid",
			exits:     "execution.local\n",
		},
		"engine limits": {
			processes: []runtimeR6Process{local},
			mutate: func(value *config.Config) {
				limit := 20
				value.Controller.Defaults.RetryLimit = &limit
			},
			want:  "protocol version, step timeout (1ns..24h), and maximum step attempts (1..10) are required",
			exits: "execution.local\n",
		},
		"idempotency keys without credential directory": {
			processes:   []runtimeR6Process{local},
			mutate:      runtimeR6IdempotencyKeys,
			credentials: func(f fixture) string { return filepath.Join(f.dir, "absent") },
			want:        "controller.idempotency_keys need systemd credentials: systemd credential directory is unavailable",
			exits:       "execution.local\n",
		},
		"idempotency key credential missing": {
			processes: []runtimeR6Process{local},
			mutate:    runtimeR6IdempotencyKeys,
			want:      "idempotency key v1 is unavailable",
			exits:     "execution.local\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f), test.processes...))
			if test.mutate != nil {
				test.mutate(&value)
			}
			credentials := f.credentials
			if test.credentials != nil {
				credentials = test.credentials(f)
			}
			runtime, err := f.build(t, value, credentials)
			runtimeR6AssertBuildError(t, runtime, err, test.want)
			runtimeR6AssertStoreReleased(t, f)
			if exits := runtimeR6Exits(t, f); exits != test.exits {
				t.Fatalf("stopped processes = %q, want %q", exits, test.exits)
			}
		})
	}
}

func TestRuntimeR6BuildRecoveryFailureReleasesTheStore(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f)))
	runtimeR6SeedOperation(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime, err := Build(ctx, value, f.configPath, Options{CredentialDirectory: f.credentials, ControllerVersion: "test-build"})
	runtimeR6AssertBuildError(t, runtime, err, "recover durable operations: context canceled")
	runtimeR6AssertStoreReleased(t, f)
}

func TestRuntimeR6BuildRecordsRecoveredOperationsAndKeyring(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f)))
	runtime, err := f.build(t, value, f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.recovered != nil || runtime.keyring != nil || len(runtime.AdapterIDs()) != 0 {
		t.Fatalf("empty build recovered %v, keyring %v, adapters %v", runtime.recovered, runtime.keyring, runtime.AdapterIDs())
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	runtimeR6SeedOperation(t, f)
	runtimeR6IdempotencyKeys(&value)
	if err := os.WriteFile(filepath.Join(f.credentials, "idempotency-v1"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err = f.build(t, value, f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := strings.Join(runtime.recovered, ","); got != "define-operation" {
		t.Fatalf("recovered = %q", got)
	}
	if runtime.keyring == nil {
		t.Fatal("keyring was not built")
	}
	if got := runtime.servers["staging-server"].EnvironmentID; got != "staging" || len(runtime.servers) != 1 {
		t.Fatalf("servers = %v", runtime.servers)
	}
}

func TestRuntimeR6BuildTracksBuiltAdapters(t *testing.T) {
	f := newFixture(t)
	runtime, err := f.build(t, f.load(t, f.configText("execution.local")), f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := fmt.Sprint(runtime.adapterEnvironments); got != "map[storage.wasabi-staging:staging]" {
		t.Fatalf("adapter environments = %s", got)
	}
	if len(runtime.processes) != 1 || runtime.UnreachableProxies() != nil {
		t.Fatalf("processes = %d, unreachable proxies = %v", len(runtime.processes), runtime.UnreachableProxies())
	}
	if len(runtime.MonitoredIntegrations()) != 0 || len(runtime.Notifiers()) != 0 {
		t.Fatalf("monitored = %v, notifiers = %v", runtime.MonitoredIntegrations(), runtime.Notifiers())
	}
}

func TestRuntimeR6BuildCredentialErrorsNameTheFirstAdapter(t *testing.T) {
	for name, test := range map[string]struct{ credentials, want string }{
		"no directory":     {"", "adapter storage.wasabi-staging: adapter storage.wasabi-staging needs systemd credentials: systemd credential directory is required"},
		"absent directory": {"absent", "adapter storage.wasabi-staging: adapter storage.wasabi-staging needs systemd credentials: systemd credential directory is unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			credentials := test.credentials
			if credentials != "" {
				credentials = filepath.Join(f.dir, credentials)
			}
			runtime, err := f.build(t, f.load(t, f.configText("execution.local")), credentials)
			runtimeR6AssertBuildError(t, runtime, err, test.want)
			runtimeR6AssertStoreReleased(t, f)
		})
	}
}

// runtimeR6PartialRuntime returns a runtime with schemas and an empty registry,
// as Build has it when it registers adapters.
func runtimeR6PartialRuntime(t *testing.T) *Runtime {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	return &Runtime{Schemas: schemas, Registry: registry, adapterEnvironments: map[string]string{}}
}

func TestRuntimeR6RegisterAdaptersRefusesUnsupportedImplementation(t *testing.T) {
	runtime := runtimeR6PartialRuntime(t)
	value := config.Config{Adapters: []config.AdapterProfile{{ID: "hosting.local", Category: "hosting"}, {ID: "hosting.cloud", Category: "hosting", Implementation: "cloud"}}}
	err := runtime.registerAdapters(context.Background(), value, Options{ControllerVersion: "test-build"})
	if err == nil || err.Error() != "adapter hosting.cloud: unsupported implementation cloud" {
		t.Fatalf("registerAdapters error = %v", err)
	}
	if len(runtime.AdapterIDs()) != 0 {
		t.Fatalf("adapters = %v", runtime.AdapterIDs())
	}
}

func TestRuntimeR6RegisterAdaptersKeepsProcessWhenRegistrationFails(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, runtimeR6ConfigText(f, runtimeR6ExitLog(f), runtimeR6Process{"execution.local", "execution.local"}))
	runtime := runtimeR6PartialRuntime(t)
	existing := fakeadapter.New(adapter.Descriptor{AdapterID: "execution.local", Category: adapter.CategoryExecution, DisplayName: "existing",
		ImplementationVersion: "test-1", ProtocolVersions: []string{runtime.Schemas.ProtocolVersion()}, ContractVersions: []string{"1"},
		RequiredPermissions: []string{}, Capabilities: []adapter.Capability{}})
	if err := runtime.Registry.Register(existing); err != nil {
		t.Fatal(err)
	}
	err := runtime.registerAdapters(context.Background(), value, Options{ControllerVersion: "test-build"})
	if err == nil || err.Error() != `adapter execution.local: adapter "execution.local" is already registered` {
		t.Fatalf("registerAdapters error = %v", err)
	}
	if len(runtime.processes) != 1 || len(runtime.AdapterIDs()) != 0 {
		t.Fatalf("processes = %d, adapters = %v", len(runtime.processes), runtime.AdapterIDs())
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if exits := runtimeR6Exits(t, f); exits != "execution.local\n" {
		t.Fatalf("stopped processes = %q", exits)
	}
}

func TestRuntimeR6ValidateReportsSortedProblems(t *testing.T) {
	if err := Validate(config.Config{Adapters: []config.AdapterProfile{{ID: "hosting.local", Category: "hosting"}}}); err != nil {
		t.Fatalf("Validate of declared-only adapters = %v", err)
	}
	value := config.Config{Adapters: []config.AdapterProfile{
		{ID: "hosting.zeta", Category: "hosting", Implementation: "zeta"},
		{ID: "hosting.alpha", Category: "hosting", Implementation: "alpha"},
	}}
	const want = "adapter configuration is invalid:\n - adapter hosting.alpha: implementation alpha has no runtime builder\n - adapter hosting.zeta: implementation zeta has no runtime builder"
	if err := Validate(value); err == nil || err.Error() != want {
		t.Fatalf("Validate error = %v, want %q", err, want)
	}
}

func TestRuntimeR6CloseIsSafeOnPartialAndClosedRuntimes(t *testing.T) {
	if err := (&Runtime{}).Close(); err != nil {
		t.Fatalf("Close of an empty runtime = %v", err)
	}
	f := newFixture(t)
	runtime, err := f.build(t, f.load(t, f.configText("execution.local")), f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	runtime.stopMonitoring = func() { stops++ }
	ctx, cancel := context.WithCancel(context.Background())
	runtime.runner = newRunner(ctx, cancel, func(context.Context, string) {})
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if stops != 1 || runtime.stopMonitoring != nil || runtime.runner != nil || ctx.Err() == nil {
		t.Fatalf("monitoring stops = %d, runner = %v, runner context = %v", stops, runtime.runner, ctx.Err())
	}
	if runtime.Store != nil || runtime.processes != nil {
		t.Fatalf("store = %v, processes = %v", runtime.Store, runtime.processes)
	}
	if err := runtime.Close(); err != nil || stops != 1 {
		t.Fatalf("second Close = %v, monitoring stops = %d", err, stops)
	}
	runtimeR6AssertStoreReleased(t, f)
}
