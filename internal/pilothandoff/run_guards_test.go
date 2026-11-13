package pilothandoff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin how Run refuses to start: each guard runs before any
// provider call or SSH connection, so no network is needed.

// guardStore opens a store holding one reservation for operationID, advanced
// as far as stage says: "reserved", "active" (no handoff), or "handoff".
func guardStore(t *testing.T, stage string) *store.BoltStore {
	t.Helper()
	repository, err := store.OpenBolt(filepath.Join(t.TempDir(), "pilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	reservation, _, err := repository.ReserveCapacity(store.CapacityReservation{OperationID: operationID, ServerID: "pilot", EnvironmentID: "staging",
		CapacityGroup: "shared", Provider: "vultr", ResourceKey: resourceKey}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "reserved" {
		return repository
	}
	if stage == "handoff" {
		public, _, err := sshkey.Generate()
		if err != nil {
			t.Fatal(err)
		}
		_, err = repository.ClaimCapacityCreateWithHandoff(operationID, hosthandoff.New(public, time.Now(), 0))
		if err != nil {
			t.Fatal(err)
		}
	} else if _, err := repository.MarkCapacityCreateSent(operationID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{Provider: "vultr", ResourceKey: resourceKey,
		ProviderInstanceID: instanceID, State: "present", ObservedAt: reservation.CreatedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestRunRefusesBeforeContactingAnything(t *testing.T) {
	provider := &fakeProvider{}
	config := Config{OperationID: operationID, VultrCredentialRef: "vultr", WasabiAccessKeyRef: "access", WasabiSecretKeyRef: "secret", WasabiRegion: "us-east-1"}
	cases := []struct {
		name    string
		stage   string
		mutate  func(*testing.T, *store.BoltStore, *Config)
		wantErr error
		wantMsg string
	}{
		{name: "bad region", stage: "handoff", mutate: func(_ *testing.T, _ *store.BoltStore, c *Config) { c.WasabiRegion = "mars-1" },
			wantMsg: "--wasabi-region must be a Wasabi region such as us-east-1"},
		{name: "unknown operation", stage: "handoff", mutate: func(_ *testing.T, _ *store.BoltStore, c *Config) { c.OperationID = "fedcba9876543210fedcba9876543210" },
			wantErr: store.ErrCapacityNotFound},
		{name: "not active", stage: "reserved", wantErr: ErrNotActive},
		{name: "no handoff record", stage: "active", wantErr: ErrNoHandoff},
		{name: "record for another instance", stage: "handoff", mutate: func(t *testing.T, repository *store.BoltStore, _ *Config) {
			if _, err := repository.UpdateHostHandoff(operationID, func(h *store.HostHandoffRecord) error {
				h.InstanceID = "instance-other"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}, wantMsg: "the handoff record names a different instance"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repository := guardStore(t, test.stage)
			current := config
			if test.mutate != nil {
				test.mutate(t, repository, &current)
			}
			runner := Runner{Store: repository, Provider: provider, Credentials: fakeCredentials{"vultr": "token"}}
			report, err := runner.Run(context.Background(), current)
			switch {
			case err == nil:
				t.Fatal("Run succeeded")
			case test.wantErr != nil && !errors.Is(err, test.wantErr):
				t.Fatalf("Run error = %v, want %v", err, test.wantErr)
			case test.wantMsg != "" && err.Error() != test.wantMsg:
				t.Fatalf("Run error = %q, want %q", err, test.wantMsg)
			}
			if report != (Report{}) {
				t.Fatalf("report = %+v, want empty", report)
			}
		})
	}
	if len(provider.replaced) != 0 {
		t.Fatal("a refused run replaced user data")
	}
}

// TestRunReportsAFailedRestore pins that a restore unit that fails ends the
// run with ErrRestore after every earlier step is recorded.
func TestRunReportsAFailedRestore(t *testing.T) {
	w := newWorld(t, false, false)
	if err := os.WriteFile(filepath.Join(w.dir, "bootstrap-complete"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w.config.Remote.RestoreStatus = "echo failed"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := w.runner.Run(ctx, w.config)
	if !errors.Is(err, ErrRestore) || report.RestoreComplete || !report.UserDataReplaced || report.PinnedFingerprint == "" {
		t.Fatalf("Run = %+v, %v; want ErrRestore after the earlier steps", report, err)
	}
	record, _ := w.store.GetHostHandoff(operationID)
	if !record.BootstrapReady || !record.CredentialsSent || !record.SecurityFinalized || !record.RestoreStarted {
		t.Fatalf("record = %+v", record)
	}
}
