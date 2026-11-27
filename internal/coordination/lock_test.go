package coordination

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockHeldFailsClosedAndNamesHolder(t *testing.T) {
	s, _ := newTestStore(t)
	s.LockTimeout = 30 * time.Millisecond
	lock := filepath.Join(s.Dir, LockFile)
	writeFile(t, lock, "pid=42 acquired=then\n")
	_, err := s.Load()
	wantErr(t, err, fmt.Sprintf("coordination lock %s is still held after 30ms (pid=42 acquired=then); if no coordination command is running, remove the lock file and retry", lock))
	if _, statErr := os.Stat(lock); statErr != nil {
		t.Fatalf("a held lock was removed: %v", statErr)
	}
}

func TestLockWaitsForRelease(t *testing.T) {
	s, _ := newTestStore(t)
	lock := filepath.Join(s.Dir, LockFile)
	writeFile(t, lock, "pid=1\n")
	go func() {
		time.Sleep(3 * lockPoll)
		if err := os.Remove(lock); err != nil {
			t.Errorf("remove lock: %v", err)
		}
	}()
	if _, err := s.Load(); err != nil {
		t.Fatalf("Load after the lock was released: %v", err)
	}
}

func TestLockRecordsOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFile)
	release, err := acquireLock(path, time.Second, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("pid=%d acquired=%s\n", os.Getpid(), ts(0))
	if got := readFile(t, path); got != want {
		t.Fatalf("lock content = %q, want %q", got, want)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	err = release()
	if err == nil || !strings.HasPrefix(err.Error(), "remove lock "+path+": ") {
		t.Fatalf("second release error = %v", err)
	}
}

func TestLockReportsCreateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", LockFile)
	_, err := acquireLock(path, time.Second, t0)
	if err == nil || !strings.HasPrefix(err.Error(), "create lock "+path+": ") {
		t.Fatalf("error = %v", err)
	}
}

func TestLockHeldErrorWithUnreadableHolder(t *testing.T) {
	err := lockHeldError(filepath.Join(t.TempDir(), "gone"), time.Second)
	if !strings.Contains(err.Error(), "(holder unknown)") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteLockOwnerFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, LockFile)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// Writing to a closed file fails; the lock must not be left behind.
	_, err = writeLockOwner(f, path, t0)
	if err == nil || !strings.HasPrefix(err.Error(), "write lock "+path+": ") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("lock left behind: %v", statErr)
	}
}

func TestAuditAppendsListEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, AuditFile)
	writeFile(t, path, "# Log\n\n*footer*")
	events := []Event{{"a1", "claim", "T1", "multi\nline | piped\ttab"}, {"", "x", "", ""}}
	if err := appendAudit(path, 4, t0, events); err != nil {
		t.Fatal(err)
	}
	if err := appendAudit(path, 5, t0, events[:1]); err != nil {
		t.Fatal(err)
	}
	line := "- " + ts(0) + " | rev %d | a1 | claim | T1 | multi line / piped tab\n"
	want := "# Log\n\n*footer*\n\n" + fmt.Sprintf(line, 4) + "- " + ts(0) + " | rev 4 | - | x | - | -\n" + fmt.Sprintf(line, 5)
	if got := readFile(t, path); got != want {
		t.Fatalf("audit =\n%s\nwant\n%s", got, want)
	}
}

func TestAuditSeparator(t *testing.T) {
	cases := map[string]string{"": "", "- a\n": "", "- a": "\n", "text\n": "\n", "text\n\n": "\n"}
	for body, want := range cases {
		path := filepath.Join(t.TempDir(), AuditFile)
		writeFile(t, path, body)
		if got, err := auditSeparator(path); err != nil || got != want {
			t.Errorf("auditSeparator(%q) = %q, %v; want %q", body, got, err, want)
		}
	}
}

func TestAuditOpenFailure(t *testing.T) {
	// A directory can be read by name but not opened for appending.
	dir := t.TempDir()
	err := appendAudit(dir, 1, t0, []Event{{"a", "b", "c", "d"}})
	if err == nil || !strings.HasPrefix(err.Error(), "read audit log: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteJSONAtomicFailures(t *testing.T) {
	dir := t.TempDir()
	err := writeJSONAtomic(filepath.Join(dir, "x.json"), func() {})
	if err == nil || !strings.HasPrefix(err.Error(), "encode x.json: ") {
		t.Fatalf("encode error = %v", err)
	}
	// Renaming a file over a non-empty directory fails on every platform.
	target := filepath.Join(dir, "busy.json")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = writeJSONAtomic(target, map[string]int{"a": 1})
	if err == nil || !strings.HasPrefix(err.Error(), "replace busy.json: ") {
		t.Fatalf("rename error = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}

func TestFillAndCloseReportsWriteFailure(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fillAndClose(f, []byte("x")); err == nil {
		t.Fatal("fillAndClose on a closed file succeeded")
	}
	if err := writeJSONAtomic(filepath.Join(t.TempDir(), "missing", "x.json"), 1); err == nil ||
		!strings.HasPrefix(err.Error(), "create temporary file for x.json: ") {
		t.Fatalf("error = %v", err)
	}
}

func TestRemoveTempIgnoresMissingFile(t *testing.T) {
	if err := removeTemp(filepath.Join(t.TempDir(), "gone")); err != nil {
		t.Fatalf("removeTemp = %v", err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "child"), "x")
	if err := removeTemp(dir); err == nil || !strings.HasPrefix(err.Error(), "remove temporary file: ") {
		t.Fatalf("removeTemp of a non-empty directory = %v", err)
	}
}

func TestClaimExpiredWithoutExpiry(t *testing.T) {
	if expired, err := ClaimExpired(&Task{ID: "T"}, t0); expired || err != nil {
		t.Fatalf("ClaimExpired = %v, %v", expired, err)
	}
}

func TestAuditAppendFailsOnReadOnlyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), AuditFile)
	writeFile(t, path, "# Log\n")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Error(err)
		}
	})
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		if cerr := f.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		t.Skip("running with privileges that ignore read-only files")
	}
	err := appendAudit(path, 1, t0, []Event{{"a", "b", "c", "d"}})
	if err == nil || !strings.HasPrefix(err.Error(), "open audit log: ") {
		t.Fatalf("error = %v", err)
	}
}
