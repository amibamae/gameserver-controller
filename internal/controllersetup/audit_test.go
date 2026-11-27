package controllersetup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixedAuditClock() time.Time {
	return time.Date(2026, 11, 27, 12, 0, 0, 5, time.FixedZone("x", 3600))
}

func TestAuditLogAppendsPrivateJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "var", "log", "setup-audit.jsonl")
	log := AuditLog{Path: path, Now: fixedAuditClock}
	if err := log.Append(AuditRecord{Command: "apply", Event: "start", UID: 0}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(AuditRecord{Command: "apply", Event: "finish", Outcome: "failed", Detail: "boom", UID: 0}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-11-27T11:00:00.000000005Z","command":"apply","event":"start","uid":0}` + "\n" +
		`{"time":"2026-11-27T11:00:00.000000005Z","command":"apply","event":"finish","outcome":"failed","detail":"boom","uid":0}` + "\n"
	if string(data) != want {
		t.Fatalf("log = %q, want %q", data, want)
	}
}

func TestAuditLogUsesWallClockByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := (AuditLog{Path: path}).Append(AuditRecord{Command: "keys", Event: "start"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("log = %v, %v", info, err)
	}
}

func TestAuditLogRefusals(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "is-a-directory")
	if err := os.Mkdir(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct{ path, want string }{
		"relative":          {"audit.jsonl", "audit log path must be clean and absolute"},
		"unclean":           {dir + "/x/../audit.jsonl", "audit log path must be clean and absolute"},
		"parent is a file":  {filepath.Join(notDir, "audit.jsonl"), "create audit log directory failed"},
		"log is not a file": {logDir, "audit log must be a regular file"},
	} {
		t.Run(name, func(t *testing.T) {
			err := AuditLog{Path: test.path, Now: fixedAuditClock}.Append(AuditRecord{Command: "apply", Event: "start"})
			if err == nil || err.Error() != test.want {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}
