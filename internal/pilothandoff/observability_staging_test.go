package pilothandoff

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type stagingSSH struct {
	runCalls    int
	hostKey     string
	hostErr     error
	result      Result
	runErr      error
	command     string
	expectedKey string
	payload     []byte
}

func (s *stagingSSH) HostKey(context.Context, Target) (string, error) {
	return s.hostKey, s.hostErr
}

func (s *stagingSSH) Run(_ context.Context, _ Target, expectedKey, command string, stdin []byte) (Result, error) {
	s.runCalls++
	s.command, s.expectedKey = command, expectedKey
	s.payload = append([]byte(nil), stdin...)
	return s.result, s.runErr
}

// Unlike newWorld, this fixture needs no sshd: all transport is scripted.
func newObservabilityStage(t *testing.T) *world {
	t.Helper()
	oneTime, _, err := sshkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pinned, _, err := sshkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.OpenBolt(filepath.Join(t.TempDir(), "pilot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Error(err)
		}
	})
	reserveHandoffFixture(t, repository, oneTime)
	w := &world{store: repository, swapKey: sshkey.AuthorizedKey(pinned)}
	w.runner = Runner{Store: repository, Provider: &fakeProvider{}, Credentials: fakeCredentials{"vultr": "synthetic", "logs": strings.Repeat("x", 32)}}
	return w
}

func completeObservabilityStage(t *testing.T, w *world) {
	t.Helper()
	_, err := w.store.UpdateHostHandoff(operationID, func(record *store.HostHandoffRecord) error {
		if record.ConnectOneTime(record.OneTimeHostKey, time.Now()) != hosthandoff.Permitted {
			return errors.New("test one-time connection refused")
		}
		if record.Rotate(w.swapKey+"\n") != hosthandoff.Pinned {
			return errors.New("test host-key rotation refused")
		}
		record.BootstrapReady = true
		record.SecurityFinalized = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func stagedVictoriaLogs() VictoriaLogsConfig {
	return VictoriaLogsConfig{OperationID: operationID, VultrCredentialRef: "vultr",
		IngestCredentialRef: "logs", PrivacyMode: "lifecycle",
		Endpoint: "https://logs.example/insert/jsonline"}
}

func TestStagingHostKeyMismatchNeverSendsLogSecret(t *testing.T) {
	w := newObservabilityStage(t)
	completeObservabilityStage(t, w)
	ssh := &stagingSSH{hostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestFakeMismatchedHostKey"}
	w.runner.SSH = ssh
	w.runner.Credentials = fakeCredentials{"vultr": "token", "logs": strings.Repeat("x", 32)}
	if err := w.runner.ConfigureVictoriaLogs(context.Background(), stagedVictoriaLogs()); !errors.Is(err, ErrHostKey) {
		t.Fatalf("mismatched host key = %v; want ErrHostKey", err)
	}
	if ssh.runCalls != 0 {
		t.Fatal("sent a command or credential after SSH host mismatch")
	}
}

func TestStagingConfirmedDestroyedInstanceNeverReceivesLogConfig(t *testing.T) {
	w := newObservabilityStage(t)
	reservation, err := w.store.GetCapacityReservation(operationID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.store.ObserveCapacity(operationID, store.CapacityProviderObservation{
		Provider: "vultr", ResourceKey: resourceKey, ProviderInstanceID: instanceID,
		State: "destroyed", ObservedAt: reservation.LastObservation.ObservedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	ssh := &stagingSSH{}
	w.runner.SSH = ssh
	if err := w.runner.ConfigureVictoriaLogs(context.Background(), stagedVictoriaLogs()); !errors.Is(err, ErrNotActive) {
		t.Fatalf("destroyed VM config = %v; want ErrNotActive", err)
	}
	if ssh.runCalls != 0 {
		t.Fatal("attempted to contact a destroyed VM")
	}
}

var _ SSH = (*stagingSSH)(nil)

func TestStagingObservabilitySSHFailureAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result Result
		err    error
	}{
		{"authentication", Result{ExitCode: 255, Stderr: []byte("Permission denied: synthetic private details")}, nil},
		{"remote helper", Result{ExitCode: 1, Stderr: []byte("synthetic private details")}, nil},
		{"transport", Result{}, errors.New("synthetic transport failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newObservabilityStage(t)
			completeObservabilityStage(t, w)
			ssh := &stagingSSH{hostKey: w.swapKey, result: tc.result, runErr: tc.err}
			w.runner.SSH = ssh
			err := w.runner.ConfigureVictoriaLogs(context.Background(), stagedVictoriaLogs())
			want := "VM observability configuration failed; check the restricted remote helper"
			if tc.err != nil {
				want = tc.err.Error()
			}
			if err == nil || err.Error() != want {
				t.Fatalf("failure = %v; want %q", err, want)
			}
			ssh.result, ssh.runErr = Result{}, nil
			if err := w.runner.ConfigureVictoriaLogs(context.Background(), stagedVictoriaLogs()); err != nil {
				t.Fatalf("retry after SSH recovery: %v", err)
			}
			if ssh.runCalls != 2 || ssh.expectedKey != w.swapKey {
				t.Fatalf("calls = %d, expected host key = %q", ssh.runCalls, ssh.expectedKey)
			}
			if ssh.command != "sudo -n /usr/local/sbin/gsc-configure-victorialogs" {
				t.Fatalf("unexpected remote command %q", ssh.command)
			}
			var payload victoriaLogsPayload
			if err := json.Unmarshal(ssh.payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.PrivacyMode != "lifecycle" || payload.Token != strings.Repeat("x", 32) {
				t.Fatal("retry did not deliver the private lifecycle policy on stdin")
			}
		})
	}
}

func TestStagingUnreachableObservabilityNeverSendsCommand(t *testing.T) {
	w := newObservabilityStage(t)
	completeObservabilityStage(t, w)
	ssh := &stagingSSH{hostErr: ErrNoHostKey}
	w.runner.SSH = ssh
	err := w.runner.ConfigureVictoriaLogs(context.Background(), stagedVictoriaLogs())
	if err == nil || err.Error() != "VM observability configuration failed; check the restricted remote helper" {
		t.Fatalf("unreachable error = %v", err)
	}
	if ssh.runCalls != 0 {
		t.Fatal("sent a command before verifying the unavailable host")
	}
}
