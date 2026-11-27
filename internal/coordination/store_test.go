package coordination

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFailsClosedOnUnreadableState(t *testing.T) {
	const prefix = "coordination state is unreadable, refusing to continue: "
	cases := []struct {
		name, file, body, want string
	}{
		{"bad json", RegistryFile, "{", prefix + "parse registry.json: unexpected EOF"},
		{"unknown field", AgentsFile, `{"version": "1.0", "agents": [], "extra": 1}`,
			prefix + `parse .agents.json: json: unknown field "extra"`},
		{"trailing data", FilesFile, `{"version": "1.0"} {}`,
			prefix + "parse .files.json: unexpected data after the JSON document"},
		{"wrong version", DepsFile, `{"version": "2.0", "dependencies": []}`,
			prefix + `.deps.json has unsupported version "2.0" (want "1.0")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			writeFile(t, filepath.Join(s.Dir, tc.file), tc.body)
			_, err := s.Load()
			wantErr(t, err, tc.want)
		})
	}
}

func TestLoadFailsClosedOnMissingFile(t *testing.T) {
	s, _ := newTestStore(t)
	if err := os.Remove(filepath.Join(s.Dir, AgentsFile)); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if err == nil || !strings.HasPrefix(err.Error(), "coordination state is unreadable, refusing to continue: read .agents.json: ") {
		t.Fatalf("Load error = %v", err)
	}
}

func TestLoadRejectsMixedRevisions(t *testing.T) {
	s, _ := newTestStore(t)
	writeFile(t, filepath.Join(s.Dir, FilesFile), `{"version": "1.0", "revision": 3, "currentOwners": {}}`)
	_, err := s.Load()
	wantErr(t, err, "coordination state is inconsistent, refusing to continue: revisions differ (registry.json 0, .agents.json 0, .files.json 3); a write may have been interrupted, so restore the directory from git and retry")
}

func TestApplyCommitsEveryFileAtTheNextRevision(t *testing.T) {
	s, _ := newTestStore(t)
	res := mustApply(t, s, Claim("a1", "T1"))
	if !res.Committed || res.Revision != 1 {
		t.Fatalf("result = %+v, want committed revision 1", res)
	}
	st := mustLoad(t, s)
	if st.Registry.Revision != 1 || st.Agents.Revision != 1 || st.Files.Revision != 1 {
		t.Fatalf("revisions = %d %d %d", st.Registry.Revision, st.Agents.Revision, st.Files.Revision)
	}
	if got := readFile(t, filepath.Join(s.Dir, DepsFile)); got != seedDeps {
		t.Fatalf("%s was rewritten:\n%s", DepsFile, got)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("directory holds %d entries, want the 5 state files and no lock or temporary file", len(entries))
	}
}

func TestApplyRejectsStaleExpectedRevision(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	_, err := s.Apply(0, Update("a1", "T1", "late", false))
	wantErr(t, err, "stale update: coordination state is at revision 1, expected 0; reload state and retry")
	if !errors.Is(err, ErrStale) {
		t.Fatalf("errors.Is(err, ErrStale) = false")
	}
	if _, err := s.Apply(1, Update("a1", "T1", "fresh", false)); err != nil {
		t.Fatalf("Apply at the current revision: %v", err)
	}
}

// TestApplyRejectsStaleCommit pins the compare-and-swap: a writer that
// read revision 0 cannot commit after another writer moved the state on.
func TestApplyRejectsStaleCommit(t *testing.T) {
	s, _ := newTestStore(t)
	other := NewStore(s.Dir)
	other.Now = s.Now
	s.beforeCommit = func() {
		s.beforeCommit = nil
		if _, err := other.Apply(AnyRevision, Claim("a2", "T2")); err != nil {
			t.Errorf("concurrent writer: %v", err)
		}
	}
	_, err := s.Apply(0, Claim("a1", "T1"))
	wantErr(t, err, "stale update: coordination state is at revision 1, expected 0; reload state and retry")
	if st := mustLoad(t, s); st.Task("T1").Status != StatusAvailable {
		t.Fatalf("the stale claim was written")
	}
}

func TestApplyRetriesALostRaceWithoutExpectedRevision(t *testing.T) {
	s, _ := newTestStore(t)
	other := NewStore(s.Dir)
	other.Now = s.Now
	calls := 0
	s.beforeCommit = func() {
		calls++
		if calls == 1 {
			mustApply(t, other, Claim("a2", "T2"))
		}
	}
	res := mustApply(t, s, Claim("a1", "T1"))
	if calls != 2 || res.Revision != 2 {
		t.Fatalf("calls = %d, revision = %d; want a retry that commits revision 2", calls, res.Revision)
	}
}

func TestApplyGivesUpAfterMaxAttempts(t *testing.T) {
	s, _ := newTestStore(t)
	s.MaxAttempts = 2
	other := NewStore(s.Dir)
	other.Now = s.Now
	s.beforeCommit = func() { mustApply(t, other, Register(Registration{Name: "bump"})) }
	_, err := s.Apply(AnyRevision, Claim("a1", "T1"))
	wantErr(t, err, "stale update: coordination state is at revision 2, expected 1; reload state and retry")
}

func TestApplyWithZeroMaxAttemptsStillTriesOnce(t *testing.T) {
	s, _ := newTestStore(t)
	s.MaxAttempts = 0
	if res := mustApply(t, s, Claim("a1", "T1")); res.Revision != 1 {
		t.Fatalf("revision = %d", res.Revision)
	}
}

func TestApplyWithoutEventsCommitsNothing(t *testing.T) {
	s, _ := newTestStore(t)
	res := mustApply(t, s, func(*State, time.Time) (Outcome, error) { return Outcome{Notes: []string{"n"}}, nil })
	if res.Committed || res.Revision != 0 || len(auditLines(t, s)) != 0 {
		t.Fatalf("result = %+v, audit = %v", res, auditLines(t, s))
	}
}

func TestApplyReturnsMutationError(t *testing.T) {
	s, _ := newTestStore(t)
	boom := errors.New("boom")
	_, err := s.Apply(AnyRevision, func(*State, time.Time) (Outcome, error) { return Outcome{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyFailsClosedWhenStateBreaksBeforeCommit(t *testing.T) {
	s, _ := newTestStore(t)
	s.beforeCommit = func() { writeFile(t, filepath.Join(s.Dir, RegistryFile), "{") }
	_, err := s.Apply(AnyRevision, Claim("a1", "T1"))
	wantErr(t, err, "coordination state is unreadable, refusing to continue: parse registry.json: unexpected EOF")
}

func TestApplyWritesNoStateWhenAuditIsMissing(t *testing.T) {
	s, _ := newTestStore(t)
	s.beforeCommit = func() {
		if err := os.Remove(filepath.Join(s.Dir, AuditFile)); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Apply(AnyRevision, Claim("a1", "T1"))
	if err == nil || !strings.HasPrefix(err.Error(), "append audit log, state left at revision 0: read audit log: ") {
		t.Fatalf("error = %v", err)
	}
	if st := mustLoad(t, s); st.Revision() != 0 {
		t.Fatalf("revision = %d, want 0", st.Revision())
	}
}

func TestStoreWriteNamesTheRevisionOnFailure(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "missing"))
	err := s.write(&State{}, 3)
	const want = "write coordination state revision 3 (its audit entry is already recorded; run validate): create temporary file for .agents.json: "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error = %v, want prefix %q", err, want)
	}
}

func TestStaleErrorMessage(t *testing.T) {
	err := fmt.Errorf("wrap: %w", &StaleError{Current: 7, Expected: 5})
	if !errors.Is(err, ErrStale) || errors.Is(err, errors.New("stale update")) {
		t.Fatal("StaleError must match ErrStale and nothing else")
	}
	wantErr(t, errors.Unwrap(err), "stale update: coordination state is at revision 7, expected 5; reload state and retry")
}

func TestLoadDefaultsMissingOwnersMap(t *testing.T) {
	s, _ := newTestStore(t)
	writeFile(t, filepath.Join(s.Dir, FilesFile), `{"version": "1.0"}`)
	if st := mustLoad(t, s); st.Files.CurrentOwners == nil {
		t.Fatal("CurrentOwners is nil")
	}
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x"}))
}

func TestApplyFailsClosedOnUnreadableState(t *testing.T) {
	s, _ := newTestStore(t)
	writeFile(t, filepath.Join(s.Dir, RegistryFile), "[]")
	_, err := s.Apply(AnyRevision, Claim("a1", "T1"))
	wantErr(t, err, "coordination state is unreadable, refusing to continue: parse registry.json: json: cannot unmarshal array into Go value of type coordination.Registry")
}

func TestApplyFailsWhenLockIsTakenBeforeCommit(t *testing.T) {
	s, _ := newTestStore(t)
	s.LockTimeout = 30 * time.Millisecond
	lock := filepath.Join(s.Dir, LockFile)
	s.beforeCommit = func() { writeFile(t, lock, "pid=7\n") }
	_, err := s.Apply(AnyRevision, Claim("a1", "T1"))
	wantErr(t, err, fmt.Sprintf("coordination lock %s is still held after 30ms (pid=7); if no coordination command is running, remove the lock file and retry", lock))
}
