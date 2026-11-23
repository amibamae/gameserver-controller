package pilothandoff

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

type stagingSSH struct {
	runCalls int
}

func (s *stagingSSH) HostKey(context.Context, Target) (string, error) {
	return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestFakeMismatchedHostKey", nil
}

func (s *stagingSSH) Run(context.Context, Target, string, string, []byte) (Result, error) {
	s.runCalls++
	return Result{ExitCode: 0}, nil
}

func stagedVictoriaLogs() VictoriaLogsConfig {
	return VictoriaLogsConfig{OperationID: operationID, VultrCredentialRef: "vultr",
		IngestCredentialRef: "logs", PrivacyMode: "lifecycle",
		Endpoint: "https://logs.example/insert/jsonline"}
}

func TestStagingHostKeyMismatchNeverSendsLogSecret(t *testing.T) {
	w := newWorld(t, false, false)
	_, err := w.store.UpdateHostHandoff(operationID, func(record *store.HostHandoffRecord) error {
		record.ConnectOneTime(record.OneTimeHostKey, time.Now())
		record.Rotate(w.swapKey + "\n")
		record.BootstrapReady = true
		record.SecurityFinalized = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ssh := &stagingSSH{}
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
	w := newWorld(t, false, false)
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
