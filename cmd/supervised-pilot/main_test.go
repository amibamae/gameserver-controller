package main

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func installFakeSSH(t *testing.T, failPreflight, failStart bool, sameInvocation ...bool) (string, string) {
	t.Helper()

	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_LOG", logPath)
	t.Setenv("FAIL_PREFLIGHT", map[bool]string{true: "1", false: "0"}[failPreflight])
	t.Setenv("FAIL_START", map[bool]string{true: "1", false: "0"}[failStart])
	same := len(sameInvocation) > 0 && sameInvocation[0]
	t.Setenv("SAME_INVOCATION", map[bool]string{true: "1", false: "0"}[same])
	t.Setenv("PRE_INVOCATION_ID", "00000000000000000000000000000001")
	t.Setenv("POST_INVOCATION_ID", "00000000000000000000000000000002")
	t.Setenv("POST_ACTIVE_STATE", "")
	t.Setenv("POST_SUB_STATE", "")
	t.Setenv("POST_RESULT", "")
	t.Setenv("POST_EXIT_CODE", "")
	t.Setenv("SHOW_COUNT_FILE", logPath+".show-count")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	script := strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$*\" >> \"$SSH_LOG\"",
		"case \"$*\" in",
		"  *\"systemctl show\"*)",
		"    show_count=0",
		"    if [ -f \"$SHOW_COUNT_FILE\" ]; then show_count=$(cat \"$SHOW_COUNT_FILE\"); fi",
		"    show_count=$((show_count + 1))",
		"    printf '%s' \"$show_count\" > \"$SHOW_COUNT_FILE\"",
		"    invocation_id=$PRE_INVOCATION_ID",
		"    if [ \"$show_count\" -gt 1 ]; then invocation_id=$POST_INVOCATION_ID; fi",
		"    if [ \"$show_count\" -gt 1 ] && [ \"$SAME_INVOCATION\" = \"1\" ]; then invocation_id=$PRE_INVOCATION_ID; fi",
		"    active_state=inactive",
		"    sub_state=dead",
		"    result=success",
		"    exit_code=0",
		"    if [ \"$show_count\" -gt 1 ]; then",
		"      if [ -n \"$POST_ACTIVE_STATE\" ]; then active_state=$POST_ACTIVE_STATE; fi",
		"      if [ -n \"$POST_SUB_STATE\" ]; then sub_state=$POST_SUB_STATE; fi",
		"      if [ -n \"$POST_RESULT\" ]; then result=$POST_RESULT; fi",
		"      if [ -n \"$POST_EXIT_CODE\" ]; then exit_code=$POST_EXIT_CODE; fi",
		"    fi",
		"    if [ \"$FAIL_PREFLIGHT\" = \"1\" ]; then",
		"      printf 'ActiveState=failed\\nSubState=failed\\nResult=failed\\nExecMainStatus=1\\nInvocationID=%s\\n' \"$invocation_id\"",
		"    else",
		"      printf 'ActiveState=%s\\nSubState=%s\\nResult=%s\\nExecMainStatus=%s\\nInvocationID=%s\\n' \"$active_state\" \"$sub_state\" \"$result\" \"$exit_code\" \"$invocation_id\"",
		"    fi",
		"    ;;",
		"  *\"systemctl start --wait minecraft-backup.service\"*)",
		"    if [ \"$FAIL_START\" = \"1\" ]; then echo 'simulated remote error' >&2; exit 7; fi",
		"    ;;",
		"  *) echo 'unexpected SSH command' >&2; exit 64 ;;",
		"esac",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return logPath, home
}

func runBackup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func runPilotWithState(t *testing.T, statePath string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runWithSettings(args, &stdout, &stderr, runtimeSettings{stateDBPath: statePath})
	return stdout.String() + stderr.String(), err
}

func provisionArgs() []string {
	return []string{
		"provision",
		"--server-id", "pilot-test", "--environment", "staging",
		"--region", "ewr", "--plan", "vc2-1c-2gb", "--os-id", "362",
		"--hostname", "pilot-test.example.invalid", "--ssh-key-id", "sshkey-123",
	}
}

func TestVultrProvisionDefaultsToDryRunWithoutCredentialOrDatabase(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	output, err := runPilotWithState(t, statePath, provisionArgs()...)
	if err != nil {
		t.Fatalf("provision dry run failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Dry run: no provider API call, credential read, or capacity reservation") ||
		!strings.Contains(output, "Configured hard limit: 1 controller-owned VM") {
		t.Fatalf("missing dry-run or capacity information: %q", output)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("dry run created the state database: %v", err)
	}
}

func TestVultrProvisionRequiresExactConfirmationBeforeCredentialAccess(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	args := append(provisionArgs(), "--execute", "--confirm-create", "wrong@staging")
	output, err := runPilotWithState(t, statePath, args...)
	if err == nil || !strings.Contains(err.Error(), "must exactly match") {
		t.Fatalf("expected exact-create confirmation error, got %v (%s)", err, output)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("mismatched confirmation created the state database: %v", err)
	}
}

func TestVultrProvisionDryRunRejectsOperationID(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	args := append(provisionArgs(),
		"--operation-id", "0123456789abcdef0123456789abcdef")
	output, err := runPilotWithState(t, statePath, args...)
	if err == nil || !strings.Contains(err.Error(), "--operation-id requires --execute") {
		t.Fatalf("expected dry-run operation ID rejection, got %v (%s)", err, output)
	}
}

func TestProvisionCannotOverrideEmbeddedCapacityPolicyOrStateDatabase(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	for _, args := range [][]string{
		append(provisionArgs(), "--capacity-policy", filepath.Join(t.TempDir(), "permissive.json")),
		append(provisionArgs(), "--state-db", filepath.Join(t.TempDir(), "empty.db")),
	} {
		if _, err := runPilotWithState(t, statePath, args...); err == nil {
			t.Fatalf("accepted runtime capacity override: %v", args[len(provisionArgs()):])
		}
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("rejected override created the capacity database: %v", err)
	}
}

func TestCapacityStatePathIgnoresHomeEnvironmentOverride(t *testing.T) {
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	path, err := (runtimeSettings{}).capacityStatePath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(currentUser.HomeDir, ".local", "state", "gsc-pilot", "pilot.db")
	if path != want {
		t.Fatalf("capacity state path = %q, want account-owned path %q", path, want)
	}
}

func TestResolveCapacityStatePathUsesAccountStateDirectory(t *testing.T) {
	accountHome := t.TempDir()
	workingDirectory := t.TempDir()
	got, err := resolveCapacityStatePath(accountHome, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(accountHome, ".local", "state", "gsc-pilot", "pilot.db")
	if got != want {
		t.Fatalf("capacity state path = %q, want %q", got, want)
	}
}

func TestResolveCapacityStatePathRefusesUndiscoveredLegacyDatabase(t *testing.T) {
	accountHome := t.TempDir()
	workingDirectory := t.TempDir()
	legacyPath := filepath.Join(workingDirectory, "var", "pilot.db")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("previous capacity database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveCapacityStatePath(accountHome, workingDirectory)
	if err == nil || !strings.Contains(err.Error(), legacyPath) || !strings.Contains(err.Error(), filepath.Join(accountHome, ".local", "state", "gsc-pilot", "pilot.db")) {
		t.Fatalf("expected migration refusal naming both paths, got %v", err)
	}
}

func TestVultrProvisionUsesSystemdCredentialDirectory(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state", "pilot.db")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	args := append(provisionArgs(), "--execute", "--confirm-create", "pilot-test@staging")
	output, err := runPilotWithState(t, statePath, args...)
	if err == nil || !strings.Contains(err.Error(), "systemd credential directory") {
		t.Fatalf("expected missing systemd credential error, got %v (%s)", err, output)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("missing credential directory created the state database: %v", err)
	}
}

func TestCapacityCommandShowsPersistedReservations(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = repository.ReserveCapacity(store.CapacityReservation{
		OperationID: "0123456789abcdef0123456789abcdef", ServerID: "pilot-test",
		EnvironmentID: "staging", CapacityGroup: "shared_nonproduction",
		Provider: "vultr", ResourceKey: "gsc-managed-test",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := runPilotWithState(t, statePath, "capacity")
	if err != nil {
		t.Fatalf("capacity command failed: %v\n%s", err, output)
	}
	for _, expected := range []string{"Managed VM capacity: 1/1", "pilot-test", "staging", "reserved", "gsc-managed-test"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("capacity output %q does not include %q", output, expected)
		}
	}
}

func TestBootStatusKeepsProgressIndeterminateUntilEnoughLocalSamples(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: "0123456789abcdef0123456789abcdef", ServerID: "pilot-test",
		EnvironmentID: "staging", CapacityGroup: "shared_nonproduction",
		Provider: "vultr", ResourceKey: "gsc-boot-status",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	provisionedAt := time.Now().UTC()
	if _, err := repository.ObserveCapacity(reservation.OperationID, store.CapacityProviderObservation{
		Provider: reservation.Provider, ResourceKey: reservation.ResourceKey, ProviderInstanceID: "vm-boot-status",
		State: "present", ObservedAt: provisionedAt, ProvisionedAt: provisionedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := showBootStatus("pilot-test", "staging", statePath, provisionedAt.Add(5*time.Minute), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Paper readiness is pending") || !strings.Contains(output.String(), "Estimate and progress are indeterminate (0 completed boot sample(s); 3 required)") {
		t.Fatalf("boot-status output = %q", output.String())
	}

	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	readyAt := provisionedAt.Add(time.Second)
	if _, err := repository.RecordCapacityReady("pilot-test", "staging", "vm-boot-status", readyAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := showBootStatus("pilot-test", "staging", statePath, readyAt, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Paper readiness was confirmed") || !strings.Contains(output.String(), "100% (initial boot completed)") {
		t.Fatalf("ready boot-status output = %q", output.String())
	}

	repository, err = store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MarkCapacityDestroySent("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := showBootStatus("pilot-test", "staging", statePath, readyAt.Add(time.Second), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "previously confirmed") || !strings.Contains(output.String(), "destroy_outcome_unknown") ||
		strings.Contains(output.String(), "100% (initial boot completed)") {
		t.Fatalf("destroying boot-status output = %q", output.String())
	}
}

func TestBootStatusUsesLocalBootSamplesForCappedProgress(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "pilot.db")
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for i, duration := range []time.Duration{time.Second, 2 * time.Second, 3 * time.Second} {
		operationID := fmt.Sprintf("%032x", i+1)
		instanceID := fmt.Sprintf("vm-history-%d", i+1)
		reservation, _, err := repository.ReserveCapacity(store.CapacityReservation{
			OperationID: operationID, ServerID: "pilot-test", EnvironmentID: "staging",
			CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-history-" + instanceID,
		}, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		provisionedAt := time.Now().UTC()
		observedAt := time.Now().UTC()
		if _, err := repository.ObserveCapacity(reservation.OperationID, store.CapacityProviderObservation{
			Provider: reservation.Provider, ResourceKey: reservation.ResourceKey, ProviderInstanceID: instanceID,
			State: "present", ObservedAt: observedAt, ProvisionedAt: provisionedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.RecordCapacityReady("pilot-test", "staging", instanceID, provisionedAt.Add(duration)); err != nil {
			t.Fatal(err)
		}
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
	current, _, err := repository.ReserveCapacity(store.CapacityReservation{
		OperationID: "fedcba9876543210fedcba9876543210", ServerID: "pilot-test", EnvironmentID: "staging",
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ResourceKey: "gsc-current-boot",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	provisionedAt := time.Now().UTC()
	if _, err := repository.ObserveCapacity(current.OperationID, store.CapacityProviderObservation{
		Provider: current.Provider, ResourceKey: current.ResourceKey, ProviderInstanceID: "vm-current",
		State: "present", ObservedAt: provisionedAt, ProvisionedAt: provisionedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := showBootStatus("pilot-test", "staging", statePath, provisionedAt.Add(time.Second), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Estimated total: 4s") || !strings.Contains(output.String(), "Progress: [") || strings.Contains(output.String(), "Progress: [####################] 100%") {
		t.Fatalf("boot-status did not show the buffered local estimate below ready: %q", output.String())
	}
}

func TestHistoryShowsBackupAndProvisionAuditEvents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	auditPath, err := defaultAuditPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := appendAuditRecord(auditPath, "0123456789abcdef0123456789abcdef", "started", "test-host", "preflight passed"); err != nil {
		t.Fatal(err)
	}
	if err := appendAuditEvent(auditPath, "fedcba9876543210fedcba9876543210", "provision_confirmed", "provision", "pilot-test@staging", "vultr.instance.create", "instance confirmed"); err != nil {
		t.Fatal(err)
	}
	output, err := runBackup(t, "history")
	if err != nil {
		t.Fatalf("history failed: %v\n%s", err, output)
	}
	for _, expected := range []string{"ACTION\tEVENT", "backup\tstarted", "provision\tprovision_confirmed", "pilot-test@staging", "vultr.instance.create"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("history output %q does not include %q", output, expected)
		}
	}
}

func TestValidAuditRecordRequiresActionSpecificTargetAndService(t *testing.T) {
	base := auditRecord{
		OperationID: "0123456789abcdef0123456789abcdef", Event: "provision_requested",
		Action: "provision", Target: "pilot-test@staging", Service: "vultr.instance.create",
	}
	if !validAuditRecord(base) {
		t.Fatal("expected valid provisioning audit record")
	}
	base.Target = "pilot-test@production@staging"
	if validAuditRecord(base) {
		t.Fatal("accepted malformed provisioning target")
	}
	base.Action, base.Target, base.Service, base.Event = "backup", "test-host", backupUnit, "provision_requested"
	if validAuditRecord(base) {
		t.Fatal("accepted provisioning event as backup audit record")
	}
}

func TestBackupDefaultsToDryRunWithoutSSH(t *testing.T) {
	logPath, _ := installFakeSSH(t, false, false)

	output, err := runBackup(t, "backup", "--host", "server.example")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Dry run:") {
		t.Fatalf("expected dry-run output, got %q", output)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("dry run invoked SSH; log stat error = %v", err)
	}
}

func TestBackupRequiresExactHostConfirmation(t *testing.T) {
	logPath, _ := installFakeSSH(t, false, false)

	_, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "other.example")
	if err == nil || !strings.Contains(err.Error(), "must exactly match") {
		t.Fatalf("expected exact-host confirmation error, got %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("mismatched confirmation invoked SSH; log stat error = %v", err)
	}
}

func TestBackupSuccessIsAuditedAndUsesOnlyBackupUnit(t *testing.T) {
	logPath, home := installFakeSSH(t, false, false)

	output, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
	if err != nil {
		t.Fatalf("backup failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "completed successfully") {
		t.Fatalf("expected success output, got %q", output)
	}
	if !strings.Contains(output, "does not verify backup contents or storage integrity") {
		t.Fatalf("expected the unverified-content limitation in success output, got %q", output)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "\"event\":\"started\"") || !strings.Contains(string(audit), "\"event\":\"service_succeeded\"") {
		t.Fatalf("expected started and succeeded audit records, got %s", audit)
	}
	info, err := os.Stat(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("audit file mode = %#o, want %#o", got, 0o600)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "systemctl start --wait minecraft-backup.service") != 1 {
		t.Fatalf("expected exactly one backup start, got %s", commands)
	}
	for _, forbidden := range []string{"minecraft-vanilla.service", "systemctl stop", "systemctl restore", "systemctl destroy"} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("unexpected command %q in SSH log: %s", forbidden, commands)
		}
	}
}

func TestBackupRefusesFailedUnitAndAuditsRejection(t *testing.T) {
	logPath, home := installFakeSSH(t, true, false)

	_, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
	if err == nil || !strings.Contains(err.Error(), "refusing to retry failed unit") {
		t.Fatalf("expected failed-unit refusal, got %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), "systemctl start --wait") {
		t.Fatalf("failed unit was started: %s", commands)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "\"event\":\"rejected\"") {
		t.Fatalf("expected rejected audit record, got %s", audit)
	}
}

func TestBackupUnknownRemoteOutcomeIsNotRetriedAndIsAudited(t *testing.T) {
	logPath, home := installFakeSSH(t, false, true)

	_, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
	if err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("expected unknown-outcome error, got %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "systemctl start --wait minecraft-backup.service") != 1 {
		t.Fatalf("unknown result was retried; SSH log: %s", commands)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "\"event\":\"started\"") || !strings.Contains(string(audit), "\"event\":\"outcome_unknown\"") {
		t.Fatalf("expected started and unknown audit records, got %s", audit)
	}
}

func TestBackupUnknownWhenSystemdInvocationDoesNotChange(t *testing.T) {
	logPath, home := installFakeSSH(t, false, false, true)

	_, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
	if err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("expected stale invocation to be treated as unknown, got %v", err)
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "systemctl start --wait minecraft-backup.service") != 1 {
		t.Fatalf("stale invocation result was retried; SSH log: %s", commands)
	}
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "\"event\":\"outcome_unknown\"") || strings.Contains(string(audit), "\"event\":\"succeeded\"") {
		t.Fatalf("expected unknown, never success, in audit log; got %s", audit)
	}
}

func TestBackupValidatesPriorAndNewInvocationIDs(t *testing.T) {
	validID := "00000000000000000000000000000002"
	tests := []struct {
		name    string
		before  string
		after   string
		unknown bool
	}{
		{name: "missing prior ID with new valid ID", before: "", after: validID},
		{name: "malformed prior ID", before: "bad-prior-id", after: validID, unknown: true},
		{name: "missing new ID", before: "00000000000000000000000000000001", after: "", unknown: true},
		{name: "malformed new ID", before: "00000000000000000000000000000001", after: "bad-new-id", unknown: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logPath, home := installFakeSSH(t, false, false)
			t.Setenv("PRE_INVOCATION_ID", test.before)
			t.Setenv("POST_INVOCATION_ID", test.after)

			output, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
			if test.unknown {
				if err == nil || !strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("expected unknown outcome for invalid invocation IDs, got %v; output=%s", err, output)
				}
			} else if err != nil || !strings.Contains(output, "completed successfully") {
				t.Fatalf("expected a new valid invocation to succeed with no prior ID, got %v; output=%s", err, output)
			}

			commands, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(commands), "systemctl start --wait minecraft-backup.service") != 1 {
				t.Fatalf("invocation ID issue caused a retry; SSH log: %s", commands)
			}

			auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
			audit, readErr := os.ReadFile(auditPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			auditText := string(audit)
			if test.unknown {
				if !strings.Contains(auditText, "\"event\":\"outcome_unknown\"") ||
					strings.Contains(auditText, "\"event\":\"service_succeeded\"") {
					t.Fatalf("expected unknown, never success, in audit log; got %s", audit)
				}
			} else if !strings.Contains(auditText, "\"event\":\"service_succeeded\"") {
				t.Fatalf("expected service success in audit log, got %s", audit)
			}
		})
	}
}

func TestStatusReportsSystemdInvocationID(t *testing.T) {
	logPath, _ := installFakeSSH(t, false, false)
	output, err := runBackup(t, "status", "--host", "server.example")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, output)
	}
	for _, invocation := range []string{
		"invocation=00000000000000000000000000000001",
		"invocation=00000000000000000000000000000002",
	} {
		if !strings.Contains(output, invocation) {
			t.Fatalf("status output is missing %q: %s", invocation, output)
		}
	}
	commands, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(commands), "systemctl show") != 3 {
		t.Fatalf("expected status to inspect three units; SSH log: %s", commands)
	}
}

func TestBackupRequiresServiceInvocationToBeFinished(t *testing.T) {
	tests := []struct {
		name          string
		active        string
		sub           string
		result        string
		exitCode      string
		unknownResult bool
		failedResult  bool
	}{
		{name: "running service", active: "active", sub: "running", unknownResult: true},
		{name: "completed oneshot", active: "inactive", sub: "dead"},
		{name: "completed oneshot retained active", active: "active", sub: "exited"},
		{name: "failed state with contradictory success fields", active: "failed", sub: "failed", result: "success", exitCode: "0", failedResult: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logPath, home := installFakeSSH(t, false, false)
			t.Setenv("POST_ACTIVE_STATE", test.active)
			t.Setenv("POST_SUB_STATE", test.sub)
			t.Setenv("POST_RESULT", test.result)
			t.Setenv("POST_EXIT_CODE", test.exitCode)

			output, err := runBackup(t, "backup", "--host", "server.example", "--execute", "--confirm-host", "server.example")
			if test.unknownResult {
				if err == nil || !strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("expected running service to be unknown, got %v; output=%s", err, output)
				}
			} else if test.failedResult {
				if err == nil || !strings.Contains(err.Error(), "did not report success") {
					t.Fatalf("expected failed unit state to reject success, got %v; output=%s", err, output)
				}
			} else if err != nil || !strings.Contains(output, "completed successfully") {
				t.Fatalf("expected completed service success, got %v; output=%s", err, output)
			}

			commands, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Count(string(commands), "systemctl start --wait minecraft-backup.service") != 1 {
				t.Fatalf("backup invocation was repeated; SSH log: %s", commands)
			}

			auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
			audit, readErr := os.ReadFile(auditPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.unknownResult {
				if !strings.Contains(string(audit), `"event":"outcome_unknown"`) || strings.Contains(string(audit), `"event":"service_succeeded"`) {
					t.Fatalf("expected unknown, not success, in audit log; got %s", audit)
				}
			} else if test.failedResult {
				if !strings.Contains(string(audit), `"event":"failed"`) || strings.Contains(string(audit), `"event":"service_succeeded"`) {
					t.Fatalf("expected failed, not success, in audit log; got %s", audit)
				}
			} else if !strings.Contains(string(audit), `"event":"service_succeeded"`) {
				t.Fatalf("expected service success in audit log, got %s", audit)
			}
		})
	}
}

func TestBackupRejectsUnsafeSSHHostsBeforeInvokingSSH(t *testing.T) {
	logPath, _ := installFakeSSH(t, false, false)

	for _, host := range []string{
		"-oProxyCommand=touch /tmp/pilot-host-injection",
		"server.example; echo injected",
		"server.example\n--host=attacker.example",
	} {
		t.Run(strings.ReplaceAll(host, "/", "_"), func(t *testing.T) {
			_, err := runBackup(t, "backup", "--host="+host)
			if err == nil || !strings.Contains(err.Error(), "--host is required") {
				t.Fatalf("expected unsafe host rejection for %q, got %v", host, err)
			}
		})
	}

	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("unsafe host input invoked SSH; log stat error = %v", err)
	}
}
