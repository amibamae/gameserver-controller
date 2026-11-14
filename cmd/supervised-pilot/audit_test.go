package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryPreservesRowsBeforeInvalidTimestamp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := defaultAuditPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const first = `{"operation_id":"0123456789abcdef0123456789abcdef","recorded_at":"2026-01-02T03:04:05+01:00","event":"started","action":"backup","target":"pilot-test","service":"minecraft-backup.service","detail":"line\nnext"}`
	const second = `{"recorded_at":"invalid"}`
	if err := os.WriteFile(path, []byte(first+"\n"+second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = showHistory(&output)
	if want := "invalid timestamp in audit log " + path + " line 2"; err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	const want = "RECORDED_AT\tOPERATION_ID\tACTION\tEVENT\tTARGET\tSERVICE\tDETAIL\n2026-01-02T02:04:05Z\t0123456789abcdef0123456789abcdef\tbackup\tstarted\tpilot-test\tminecraft-backup.service\t\"line\\nnext\"\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}
