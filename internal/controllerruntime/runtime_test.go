package controllerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/adapterprocess"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const childAdapterEnv = "GSC_CONTROLLERRUNTIME_CHILD"

// TestHelperRuntimeAdapter is not a real test: Build re-runs this test binary
// as a process adapter, and this function then serves a scripted fake.
func TestHelperRuntimeAdapter(t *testing.T) {
	adapterID := os.Getenv(childAdapterEnv)
	if adapterID == "" {
		return
	}
	schemas, err := schemaregistry.Load()
	if err != nil {
		os.Exit(2)
	}
	fake := fakeadapter.New(adapter.Descriptor{AdapterID: adapterID, Category: adapter.CategoryExecution, DisplayName: "runtime test adapter",
		ImplementationVersion: "test-1", ProtocolVersions: []string{schemas.ProtocolVersion()}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{{Name: "execution.fake.inspect", Version: "1", InputSchemaURI: "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:external-resource-ref",
			OutputSchemaURI: "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:external-resource-ref", RequiredPermissions: []string{}}}})
	if err := adapterprocess.Serve(context.Background(), fake, os.Stdin, os.Stdout, adapterprocess.ServeOptions{Schemas: schemas}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

type fixture struct {
	dir, configPath, credentials, executable string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return fixture{dir: dir, configPath: filepath.Join(dir, "controller.yaml"), credentials: credentials, executable: executable}
}

// configText returns a valid configuration with a Wasabi storage adapter and a
// process execution adapter whose child reports processID.
func (f fixture) configText(processID string) string {
	return fmt.Sprintf(`schema_version: 1
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
  - id: storage.wasabi-staging
    category: storage
    implementation: wasabi
    secret_refs:
      access_key_id:
        secret_ref: systemd-credential://wasabi-staging-access-key-id
        purpose: staging Wasabi access key ID
      secret_access_key:
        secret_ref: systemd-credential://wasabi-staging-secret-access-key
        purpose: staging Wasabi secret access key
    wasabi:
      storage_profile_id: wasabi-staging
      environment_id: staging
      container: example-staging-backups
      key_prefix: server/
      region: us-west-1
      endpoint_url: https://s3.us-west-1.wasabisys.com
      max_object_bytes: 1073741824
      require_version_ids: true
  - id: execution.local
    category: execution
    implementation: process
    process:
      path: %s
      args: ["-test.run=^TestHelperRuntimeAdapter$"]
      env: [%s=%s]
  - id: game.paper
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
      storage: storage.wasabi-staging
      execution: execution.local
      game: game.paper
    lifecycle:
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
`, f.executable, childAdapterEnv, processID)
}

func (f fixture) load(t *testing.T, text string) config.Config {
	t.Helper()
	if err := os.WriteFile(f.configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := config.Load(f.configPath)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f fixture) build(t *testing.T, value config.Config, credentials string) (*Runtime, error) {
	t.Helper()
	return Build(context.Background(), value, f.configPath, Options{CredentialDirectory: credentials, ControllerVersion: "test-build"})
}

func TestBuildRegistersConfiguredAdaptersAndRunsServerDefine(t *testing.T) {
	f := newFixture(t)
	runtime, err := f.build(t, f.load(t, f.configText("execution.local")), f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := strings.Join(runtime.AdapterIDs(), ","); got != "storage.wasabi-staging,execution.local" {
		t.Fatalf("registered adapters = %s", got)
	}
	wasabi, err := runtime.Registry.Descriptor("storage.wasabi-staging")
	if err != nil || len(wasabi.Capabilities) != 8 {
		t.Fatalf("wasabi descriptor = %d capabilities, %v", len(wasabi.Capabilities), err)
	}
	if _, err := runtime.Registry.Descriptor("hosting.local"); err == nil {
		t.Fatal("a declared-only adapter was registered")
	}

	parameters := json.RawMessage(`{"server_id":"server-define","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
	record := store.OperationRecord{OperationID: "define-operation", OperationType: "server.define", ProtocolVersion: runtime.Schemas.ProtocolVersion(),
		Target: store.OperationTarget{ServerID: "server-define", EnvironmentID: "staging"}, ActorID: "operator-1", IdempotencyKey: "define-key",
		RequestFingerprint: "sha256:" + strings.Repeat("a", 64), RequestSnapshot: store.RequestSnapshot{OperationType: "server.define", Parameters: parameters}}
	proof := store.IdempotencyProof{ActiveKeyVersion: "test-v1", Candidates: []store.IdempotencyVerifier{{KeyVersion: "test-v1",
		Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	if _, _, err := runtime.Store.AcceptOperation(record, proof, []string{store.LogicalServerLockScope("staging", "server-define")}); err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.Engine.Run(context.Background(), "define-operation")
	if err != nil || completed.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("server.define = %q, %v", completed.Record.Status, err)
	}
	if _, err := runtime.Engine.Resume(context.Background(), "operator-1", "define-operation"); err == nil {
		t.Fatal("resume must be refused until an operator approval route exists")
	}
}

func TestBuildFailsClosedAndReleasesResources(t *testing.T) {
	f := newFixture(t)
	for name, test := range map[string]struct {
		processID, credentials, want string
	}{
		"identity mismatch":  {"execution.other", f.credentials, "not the configured one"},
		"missing credential": {"execution.local", filepath.Join(f.dir, "absent"), "needs systemd credentials"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.build(t, f.load(t, f.configText(test.processID)), test.credentials)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want %q", err, test.want)
			}
			// The store must be closed again; bbolt would time out on a held lock.
			database, err := store.OpenBolt(filepath.Join(f.dir, "state", "controller.db"))
			if err != nil {
				t.Fatalf("store was left open after a failed build: %v", err)
			}
			_ = database.Close()
		})
	}
}

func TestValidateRejectsNonExecutableProcessPath(t *testing.T) {
	f := newFixture(t)
	value := f.load(t, f.configText("execution.local"))
	notExecutable := filepath.Join(f.dir, "adapter")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range value.Adapters {
		if value.Adapters[i].Process != nil {
			value.Adapters[i].Process.Path = notExecutable
		}
	}
	if err := Validate(value); err == nil || !strings.Contains(err.Error(), "not an executable file") {
		t.Fatalf("Validate error = %v", err)
	}
}
