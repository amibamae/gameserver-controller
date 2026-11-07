package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
