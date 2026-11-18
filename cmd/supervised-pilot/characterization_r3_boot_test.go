//go:build !windows

package main

// Recovered CLI characterization uses temporary state and fake provider/SSH calls.
import (
	"bytes"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pilotR3Record adds one reservation for pilot-test@staging and returns its
// provisioning time. Unless provisioned, the instance has no provisioning
// time; a positive readyAfter records readiness that long after
// provisioning; destroyed releases it afterwards.
func pilotR3Record(t *testing.T, repository *store.BoltStore, index int, provisioned bool, readyAfter time.Duration, destroyed bool) time.Time {
	t.Helper()
	operationID := fmt.Sprintf("%032x", index)
	instanceID := fmt.Sprintf("vm-%d", index)
	reservation, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: operationID, ServerID: "pilot-test", EnvironmentID: "staging",
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-r3-" + instanceID,
	}, 1, 1)
	pilotR3RequireNoError(t, err)
	var provisionedAt time.Time
	if provisioned {
		provisionedAt = reservation.CreatedAt
	}
	observedAt := time.Now().UTC()
	if observedAt.Before(reservation.CreatedAt) {
		observedAt = reservation.CreatedAt
	}
	if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{
		Provider: reservation.Provider, ResourceKey: reservation.ResourceKey, ProviderInstanceID: instanceID,
		State: "present", ObservedAt: observedAt, ProvisionedAt: provisionedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if readyAfter > 0 {
		if _, err := repository.RecordCapacityReady("pilot-test", "staging", instanceID, provisionedAt.Add(readyAfter)); err != nil {
			t.Fatal(err)
		}
	}
	if destroyed {
		if _, err := repository.MarkCapacityDestroySent(operationID); err != nil {
			t.Fatal(err)
		}
		destroyedAt := time.Now().UTC()
		if destroyedAt.Before(observedAt) {
			destroyedAt = observedAt
		}
		if _, err := repository.ObserveCapacity(operationID, store.CapacityProviderObservation{
			Provider: reservation.Provider, ResourceKey: reservation.ResourceKey,
			ProviderInstanceID: instanceID, State: "destroyed", ObservedAt: destroyedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return provisionedAt
}

func pilotR3BootStatus(t *testing.T, statePath string, now time.Time) string {
	t.Helper()
	var output bytes.Buffer
	pilotR3RequireNoError(t, showBootStatus("pilot-test", "staging", statePath, now, &output))
	return output.String()
}

func TestPilotR3BootStatusOutputs(t *testing.T) {
	var output bytes.Buffer
	for _, args := range [][]string{{"Bad", "staging"}, {"pilot-test", "qa"}} {
		err := showBootStatus(args[0], args[1], filepath.Join(t.TempDir(), "x.db"), time.Now(), &output)
		if pilotR3ErrorText(err) != "a valid server ID, environment, and current time are required" {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if err := showBootStatus("pilot-test", "staging", filepath.Join(t.TempDir(), "x.db"), time.Time{}, &output); pilotR3ErrorText(err) != "a valid server ID, environment, and current time are required" {
		t.Fatal(err)
	}

	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	base := time.Now().UTC()
	pilotR3Record(t, repository, 1, true, 10*time.Second, true)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	got := pilotR3BootStatus(t, statePath, base)
	if got != "No controller-owned VM is currently recorded for pilot-test@staging.\nHistorical boot estimate unavailable (1 completed boot sample(s); 3 required).\n" {
		t.Fatalf("no current VM: %q", got)
	}

	repository, err = store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	pilotR3Record(t, repository, 2, true, 20*time.Second, true)
	pilotR3Record(t, repository, 3, true, 30*time.Second, true)
	pilotR3RequireNoError(t, repository.Close())
	got = pilotR3BootStatus(t, statePath, base)
	if got != "No controller-owned VM is currently recorded for pilot-test@staging.\nHistorical boot estimate: 38s (75th percentile plus 25% buffer; 3 samples).\n" {
		t.Fatalf("history estimate: %q", got)
	}

	// An unprovisioned current VM.
	repository, err = store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	pilotR3Record(t, repository, 4, false, 0, false)
	pilotR3RequireNoError(t, repository.Close())
	got = pilotR3BootStatus(t, statePath, base)
	if got != "Provider presence is confirmed, but no provisioning timestamp is available; Paper boot progress is indeterminate.\nHistorical boot estimate: 38s (75th percentile plus 25% buffer; 3 samples).\n" {
		t.Fatalf("unprovisioned: %q", got)
	}
}

func TestPilotR3BootStatusPendingAndReady(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	for i := 1; i <= 3; i++ {
		pilotR3Record(t, repository, i, true, time.Duration(i)*40*time.Second, true)
	}
	base := pilotR3Record(t, repository, 9, true, 0, false)
	pilotR3RequireNoError(t, repository.Close())
	got := pilotR3BootStatus(t, statePath, base.Add(60*time.Second))
	want := "Paper readiness is pending. Time since provisioning: 1m0s.\n" +
		"Estimated total: 2m30s; estimated time remaining: 1m30s.\n" +
		"Progress: [########------------] 40% (advisory; never reaches 100% before Paper readiness).\n"
	if got != want {
		t.Fatalf("pending:\n got %q\nwant %q", got, want)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(-time.Minute))
	if !strings.HasPrefix(got, "Paper readiness is pending. Time since provisioning: 0s.\nEstimated total: 2m30s; estimated time remaining: 2m30s.\nProgress: [--------------------] 0% ") {
		t.Fatalf("clock skew: %q", got)
	}
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	if !strings.HasPrefix(got, "Paper readiness is pending. Time since provisioning: 1h0m0s.\nEstimated total: 2m30s; estimated time remaining: 0s.\nProgress: [###################-] 99% ") {
		t.Fatalf("overdue: %q", got)
	}

	readyAt := base.Add(95 * time.Second)
	repository, err = store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	if _, err := repository.RecordCapacityReady("pilot-test", "staging", "vm-9", readyAt); err != nil {
		t.Fatal(err)
	}
	pilotR3RequireNoError(t, repository.Close())
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	want = "Paper readiness was confirmed at " + readyAt.Format(time.RFC3339) + ".\n" +
		"Measured provision-to-Paper-ready time: 1m35s.\n" +
		"Progress: [####################] 100% (initial boot completed)\n"
	if got != want {
		t.Fatalf("ready:\n got %q\nwant %q", got, want)
	}

	repository, err = store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	if _, err := repository.MarkCapacityDestroySent(fmt.Sprintf("%032x", 9)); err != nil {
		t.Fatal(err)
	}
	pilotR3RequireNoError(t, repository.Close())
	got = pilotR3BootStatus(t, statePath, base.Add(time.Hour))
	want = "Paper readiness was previously confirmed at " + readyAt.Format(time.RFC3339) + ".\n" +
		"Managed VM state: destroy_outcome_unknown. Current Paper readiness is not confirmed; boot progress is indeterminate.\n" +
		"Historical boot estimate: 1m59s (75th percentile plus 25% buffer; 4 samples).\n"
	if got != want {
		t.Fatalf("destroying:\n got %q\nwant %q", got, want)
	}
}

func TestPilotR3BootStatusCommandAndCapacityOutput(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	stdout, _, err := pilotR3Run(t, statePath, "boot-status", "--server-id", "pilot-test", "--environment", "testing")
	if err != nil || stdout != "No controller-owned VM is currently recorded for pilot-test@testing.\nHistorical boot estimate unavailable (0 completed boot sample(s); 3 required).\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	stdout, _, err = pilotR3Run(t, filepath.Join(t.TempDir(), "empty.db"), "capacity")
	if err != nil || stdout != "Managed VM capacity: 0/2 total.\nproduction: 0/1 slot(s)\nshared_nonproduction: 0/1 slot(s)\nNo managed VM reservations are recorded.\n" {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	repository, err := store.OpenBolt(statePath)
	pilotR3RequireNoError(t, err)
	pilotR3Record(t, repository, 1, false, 0, true)
	if _, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: pilotR3OperationID, ServerID: "pilot-test", EnvironmentID: "staging",
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-r3-reserved",
	}, 1, 1); err != nil {
		t.Fatal(err)
	}
	pilotR3RequireNoError(t, repository.Close())
	stdout, _, err = pilotR3Run(t, statePath, "capacity")
	want := "Managed VM capacity: 1/2 total.\nproduction: 0/1 slot(s)\nshared_nonproduction: 1/1 slot(s)\n" +
		"OPERATION_ID\tSERVER\tENVIRONMENT\tSTATE\tPROVIDER_INSTANCE_ID\tRESOURCE_LABEL\n" +
		fmt.Sprintf("%032x", 1) + "\tpilot-test\tstaging\tdestroyed\tvm-1\tgsc-r3-vm-1\n" +
		pilotR3OperationID + "\tpilot-test\tstaging\treserved\t-\tgsc-r3-reserved\n"
	if err != nil || stdout != want {
		t.Fatalf("err=%v\nstdout=%q\nwant  %q", err, stdout, want)
	}
}
