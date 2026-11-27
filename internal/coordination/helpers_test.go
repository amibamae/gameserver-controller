package coordination

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// t0 is the fixed clock every test starts from.
var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

const (
	seedRegistry = `{
  "version": "1.0",
  "tasks": [
    {"id": "T1", "title": "first", "phase": "P0", "status": "available", "dependencies": [],
     "acceptanceCriteria": [], "estimatedScope": "small", "requiredFiles": [],
     "assignedTo": null, "claimedAt": null, "expiresAt": null, "notes": ""},
    {"id": "T2", "title": "second", "phase": "P0", "status": "available", "dependencies": ["T1", "MISSING"],
     "acceptanceCriteria": [], "estimatedScope": "small", "requiredFiles": [],
     "assignedTo": null, "claimedAt": null, "expiresAt": null, "notes": ""},
    {"id": "T3", "title": "done", "phase": "P0", "status": "completed", "dependencies": [],
     "acceptanceCriteria": [], "estimatedScope": "small", "requiredFiles": [],
     "assignedTo": "a1", "claimedAt": null, "expiresAt": null, "notes": ""}
  ]
}
`
	seedAgents = `{
  "version": "1.0",
  "agents": [
    {"id": "a1", "name": "one", "role": "implementation", "capabilities": [], "status": "available",
     "lastHeartbeat": null, "currentTask": null, "worktree": null},
    {"id": "a2", "name": "two", "role": "implementation", "capabilities": [], "status": "available",
     "lastHeartbeat": null, "currentTask": null, "worktree": null},
    {"id": "a3", "name": "three", "role": "implementation", "capabilities": [], "status": "idle",
     "lastHeartbeat": null, "currentTask": null, "worktree": null}
  ]
}
`
	seedFiles = `{"version": "1.0", "reservations": {"area": ["x/*"]}, "currentOwners": {}, "pendingClaims": []}
`
	seedDeps = `{"version": "1.0", "dependencies": [
  {"id": "DEP-1", "title": "d", "requiredBy": ["T1"], "description": ""}
]}
`
	seedAudit = "# Audit\n"
)

// clock is a settable time source for a store.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// seedDir writes the seed state into a new temporary directory.
func seedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		RegistryFile: seedRegistry, AgentsFile: seedAgents, FilesFile: seedFiles,
		DepsFile: seedDeps, AuditFile: seedAudit,
	}
	for name, body := range files {
		writeFile(t, filepath.Join(dir, name), body)
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newTestStore returns a store on a seeded directory with a fixed clock.
func newTestStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{now: t0}
	s := NewStore(seedDir(t))
	s.Now = c.Now
	s.LockTimeout = 2 * time.Second
	return s, c
}

func mustApply(t *testing.T, s *Store, m Mutation) Result {
	t.Helper()
	res, err := s.Apply(AnyRevision, m)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

// wantErr fails unless err's message is exactly want.
func wantErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	if err.Error() != want {
		t.Fatalf("error =\n  %q\nwant\n  %q", err.Error(), want)
	}
}

// wantApplyErr applies m and requires exactly the error want, with the
// state left unchanged.
func wantApplyErr(t *testing.T, s *Store, m Mutation, want string) {
	t.Helper()
	before := readFile(t, filepath.Join(s.Dir, RegistryFile))
	_, err := s.Apply(AnyRevision, m)
	wantErr(t, err, want)
	if after := readFile(t, filepath.Join(s.Dir, RegistryFile)); after != before {
		t.Fatalf("a rejected change rewrote %s", RegistryFile)
	}
}

func mustLoad(t *testing.T, s *Store) *State {
	t.Helper()
	st, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return st
}

func auditLines(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(readFile(t, filepath.Join(s.Dir, AuditFile)), "\n") {
		if strings.HasPrefix(line, "- ") {
			out = append(out, line)
		}
	}
	return out
}

func ts(d time.Duration) string { return formatTime(t0.Add(d)) }

// wantEqual fails unless got deeply equals want.
func wantEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s =\n  %+v\nwant\n  %+v", what, got, want)
	}
}
