package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installFakeSSH(t *testing.T, failPreflight, failStart bool) (string, string) {
	t.Helper()

	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SSH_LOG", logPath)
	t.Setenv("FAIL_PREFLIGHT", map[bool]string{true: "1", false: "0"}[failPreflight])
	t.Setenv("FAIL_START", map[bool]string{true: "1", false: "0"}[failStart])
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	script := strings.Join([]string{
		"#!/bin/sh",
		"printf '%s\\n' \"$*\" >> \"$SSH_LOG\"",
		"case \"$*\" in",
		"  *\"systemctl show\"*)",
		"    if [ \"$FAIL_PREFLIGHT\" = \"1\" ]; then",
		"      printf 'ActiveState=failed\\nSubState=failed\\nResult=failed\\nExecMainStatus=1\\n'",
		"    else",
		"      printf 'ActiveState=inactive\\nSubState=dead\\nResult=success\\nExecMainStatus=0\\n'",
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
	auditPath := filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl")
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "\"event\":\"started\"") || !strings.Contains(string(audit), "\"event\":\"succeeded\"") {
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
