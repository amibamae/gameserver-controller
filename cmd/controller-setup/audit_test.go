package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

func testAudit(t *testing.T) (auditContext, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "log", "setup-audit.jsonl")
	clock := func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) }
	return auditContext{log: controllersetup.AuditLog{Path: path, Now: clock}, isRoot: true, uid: 0}, path
}

func readAudit(t *testing.T, path string) []controllersetup.AuditRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []controllersetup.AuditRecord
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		var record controllersetup.AuditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func TestRunAuditedRecordsStartAndSuccess(t *testing.T) {
	audit, path := testAudit(t)
	ran := false
	err := runAudited([]string{"credentials", "set", "--name", "secret-name"}, audit, func() error { ran = true; return nil })
	if err != nil || !ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	want := []controllersetup.AuditRecord{
		{Time: "2026-11-27T12:00:00Z", Command: "credentials set", Event: "start"},
		{Time: "2026-11-27T12:00:00Z", Command: "credentials set", Event: "finish", Outcome: "succeeded"},
	}
	if got := readAudit(t, path); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("records = %+v, want %+v", got, want)
	}
}

func TestRunAuditedRecordsFailureDetailAndReturnsIt(t *testing.T) {
	audit, path := testAudit(t)
	failure := errors.New("controller installation operations are unavailable")
	err := runAudited([]string{"apply", "--config", "/etc/x.yaml"}, audit, func() error { return failure })
	if !errors.Is(err, failure) || err.Error() != failure.Error() {
		t.Fatalf("err = %v", err)
	}
	records := readAudit(t, path)
	if len(records) != 2 || records[1].Command != "apply" || records[1].Outcome != "failed" || records[1].Detail != failure.Error() {
		t.Fatalf("records = %+v", records)
	}
}

func TestRunAuditedSkipsReadOnlyAndUnprivilegedCommands(t *testing.T) {
	for name, args := range map[string][]string{"plan": {"plan", "--json"}, "empty": nil, "not root": {"apply"}} {
		t.Run(name, func(t *testing.T) {
			audit, path := testAudit(t)
			audit.isRoot = name != "not root"
			ran := false
			if err := runAudited(args, audit, func() error { ran = true; return nil }); err != nil || !ran {
				t.Fatalf("err=%v ran=%v", err, ran)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("audit log written: %v", err)
			}
		})
	}
}

func TestRunAuditedDoesNotRunWhenStartCannotBeRecorded(t *testing.T) {
	audit, _ := testAudit(t)
	audit.log.Path = "relative/audit.jsonl"
	ran := false
	err := runAudited([]string{"keys", "issue"}, audit, func() error { ran = true; return nil })
	if ran || err == nil || err.Error() != "setup audit log is unavailable, so nothing was changed: audit log path must be clean and absolute" {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
}

func TestRunAuditedReportsUnrecordedOutcome(t *testing.T) {
	audit, path := testAudit(t)
	err := runAudited([]string{"ssh-key", "install"}, audit, func() error {
		// Replace the log with a directory so the finish record cannot be appended.
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.Mkdir(path, 0o700)
	})
	if err == nil || err.Error() != "record setup audit outcome: audit log must be a regular file" {
		t.Fatalf("err = %v", err)
	}
}
