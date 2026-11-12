package controllerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/acceptance"
	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const testDefinition = `{"server_id":"paper-new","environment_id":"staging","display_name":"Paper","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`

func buildWithIdempotencyKeys(t *testing.T) *Runtime {
	t.Helper()
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.credentials, "controller-idempotency-key-v1"), []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	value := f.load(t, f.configText("execution.local"))
	value.Controller.IdempotencyKeys = config.IdempotencyKeys{Active: "v1", Versions: map[string]string{"v1": "systemd-credential://controller-idempotency-key-v1"}}
	runtime, err := f.build(t, value, f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func defineRecord(parameters string) store.OperationRecord {
	return store.OperationRecord{OperationType: "server.define", Target: store.OperationTarget{ServerID: "paper-new", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.define", Parameters: json.RawMessage(parameters)}}
}

func TestAcceptRunsServerDefineToSucceeded(t *testing.T) {
	runtime := buildWithIdempotencyKeys(t)
	runtime.StartRunner(context.Background(), nil)
	operator := authkey.Principal{KeyID: "key-1", ActorID: "operator-1", Scopes: []authkey.Scope{{Permission: "server.define", Environment: "staging", Resource: "paper-new"}}}

	accepted, replayed, err := runtime.Accept(context.Background(), operator, "define-1", defineRecord(testDefinition))
	if err != nil || replayed {
		t.Fatalf("Accept = replayed %v, %v", replayed, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, err := runtime.Store.GetOperation(accepted.Record.OperationID)
		if err == nil && stored.Record.Status == protocolstate.OperationSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server.define did not succeed through the runner: %q, %v", stored.Record.Status, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := runtime.Store.GetLogicalServer("staging", "paper-new"); err != nil {
		t.Fatalf("defined server was not stored: %v", err)
	}

	again, replayed, err := runtime.Accept(context.Background(), operator, "define-1", defineRecord(testDefinition))
	if err != nil || !replayed || again.Record.OperationID != accepted.Record.OperationID {
		t.Fatalf("identical retry must replay the original operation: replayed %v, %v", replayed, err)
	}
	changed := strings.Replace(testDefinition, `"Paper"`, `"Other"`, 1)
	if _, _, err := runtime.Accept(context.Background(), operator, "define-1", defineRecord(changed)); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("a changed request under the same key = %v, want idempotency conflict", err)
	}
}

func TestAcceptRefusesWhatCannotRun(t *testing.T) {
	runtime := buildWithIdempotencyKeys(t)
	ctx := context.Background()
	other := authkey.Principal{KeyID: "key-1", ActorID: "operator-1", Scopes: []authkey.Scope{{Permission: "server.define", Environment: "production", Resource: "paper-new"}}}
	if _, _, err := runtime.Accept(ctx, other, "k1", defineRecord(testDefinition)); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("scope for another environment = %v, want ErrNotAuthorized", err)
	}
	definer := authkey.Principal{KeyID: "key-1", ActorID: "operator-1", Scopes: []authkey.Scope{{Permission: "server.define", Environment: "staging", Resource: "paper-new"}}}
	if _, _, err := runtime.Accept(ctx, definer, "k2", defineRecord(`{"server_id":"paper-new","environment_id":"staging"}`)); !errors.Is(err, acceptance.ErrInvalidRequest) {
		t.Fatalf("an invalid definition = %v, want ErrInvalidRequest", err)
	}

	start := store.OperationRecord{OperationType: "server.start", Target: store.OperationTarget{ServerID: "staging-server", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.start", Parameters: json.RawMessage(`{}`)}}
	starter := authkey.Principal{KeyID: "key-1", ActorID: "operator-1", Scopes: []authkey.Scope{{Permission: "server.start", Environment: "staging", Resource: "staging-server"}}}
	if _, _, err := runtime.Accept(ctx, starter, "k3", start); !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("server.start = %v, want ErrUnsupportedOperation until its workflow is registered", err)
	}
	start.Target.ServerID = "api-defined"
	starter.Scopes[0].Resource = "api-defined"
	if _, _, err := runtime.Accept(ctx, starter, "k4", start); !errors.Is(err, ErrNoNetworkProfile) {
		t.Fatalf("a server without a configured network profile = %v, want ErrNoNetworkProfile", err)
	}
}

func TestAcceptNeedsIdempotencyKeys(t *testing.T) {
	f := newFixture(t)
	runtime, err := f.build(t, f.load(t, f.configText("execution.local")), f.credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	definer := authkey.Principal{KeyID: "key-1", ActorID: "operator-1", Scopes: []authkey.Scope{{Permission: "server.define", Environment: "staging", Resource: "paper-new"}}}
	if _, _, err := runtime.Accept(context.Background(), definer, "k1", defineRecord(testDefinition)); !errors.Is(err, ErrNoIdempotencyKeys) {
		t.Fatalf("Accept without keys = %v", err)
	}
}

func TestBuildRejectsShortIdempotencyKeys(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.credentials, "short"), []byte("too-short"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := f.load(t, f.configText("execution.local"))
	value.Controller.IdempotencyKeys = config.IdempotencyKeys{Active: "v1", Versions: map[string]string{"v1": "systemd-credential://short"}}
	if runtime, err := f.build(t, value, f.credentials); err == nil {
		_ = runtime.Close()
		t.Fatal("a short idempotency key was accepted")
	}
}
