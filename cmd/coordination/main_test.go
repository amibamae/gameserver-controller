package main

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cleanState is a small state with no broken references.
var cleanState = map[string]string{
	"registry.json": `{"version": "1.0", "tasks": [
  {"id": "T1", "title": "one", "phase": "P", "status": "available", "dependencies": [], "acceptanceCriteria": [],
   "estimatedScope": "s", "requiredFiles": [], "assignedTo": null, "claimedAt": null, "expiresAt": null, "notes": ""},
  {"id": "T2", "title": "two", "phase": "P", "status": "available", "dependencies": ["T1"], "acceptanceCriteria": [],
   "estimatedScope": "s", "requiredFiles": [], "assignedTo": null, "claimedAt": null, "expiresAt": null, "notes": ""}]}`,
	".agents.json": `{"version": "1.0", "agents": [
  {"id": "a1", "name": "one", "role": "r", "capabilities": [], "status": "available", "lastHeartbeat": null, "currentTask": null, "worktree": null},
  {"id": "a2", "name": "two", "role": "r", "capabilities": [], "status": "available", "lastHeartbeat": null, "currentTask": null, "worktree": null}]}`,
	".files.json": `{"version": "1.0", "reservations": {}, "currentOwners": {}, "pendingClaims": []}`,
	".deps.json":  `{"version": "1.0", "dependencies": []}`,
	"audit.md":    "# Audit\n",
}

func writeState(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// repoState copies the repository's committed COORDINATION directory.
func repoState(t *testing.T) string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"registry.json", ".agents.json", ".files.json", ".deps.json", "audit.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", stateDirName, name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	return writeState(t, files)
}

type result struct {
	code           int
	stdout, stderr string
}

// runCLI runs the command with env as its whole environment and a
// main-worktree lookup that fails, so no test can reach a real checkout.
func runCLI(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	c := &cli{
		stdout: &out, stderr: &errOut,
		getenv:  func(k string) string { return env[k] },
		findDir: func() (string, error) { return "", errors.New("no git here") },
	}
	code := c.run(args)
	return result{code, out.String(), errOut.String()}
}

func wantRun(t *testing.T, r result, code int, stdout, stderr string) {
	t.Helper()
	if r.code != code || r.stdout != stdout || r.stderr != stderr {
		t.Fatalf("got code %d\nstdout:\n%s\nstderr:\n%s\nwant code %d\nstdout:\n%s\nstderr:\n%s",
			r.code, r.stdout, r.stderr, code, stdout, stderr)
	}
}

const usageLine = "usage: coordination <command> [flags]; commands: available, block, claim, complete, handoff, reconcile, register, release, reserve, status, update, validate\n"

func TestUsageErrors(t *testing.T) {
	dir := writeState(t, cleanState)
	cases := []struct {
		args []string
		want string
	}{
		{nil, "missing command"},
		{[]string{"frobnicate"}, `unknown command "frobnicate"`},
		{[]string{"claim", "--dir", dir, "--bogus"}, "claim: flag provided but not defined: -bogus"},
		{[]string{"status", "--dir", dir, "extra"}, `status: unexpected argument "extra"`},
		{[]string{"claim", "--dir", dir, "--agent", "a1"}, "claim needs --task-id"},
		{[]string{"claim", "--dir", dir, "--task-id", "T1"}, "claim needs --agent or $GSC_AGENT_ID"},
		{[]string{"claim", "--dir", dir, "--agent", "a1", "--task-id", "T1", "--expect-revision", "-2"}, "claim: --expect-revision must be 0 or more"},
		{[]string{"register", "--dir", dir}, "register needs --name"},
		{[]string{"update", "--dir", dir, "--agent", "a1", "--task-id", "T1"}, "update needs --progress"},
		{[]string{"block", "--dir", dir, "--agent", "a1", "--task-id", "T1"}, "block needs --reason"},
		{[]string{"handoff", "--dir", dir, "--agent", "a1", "--task-id", "T1", "--to-agent", "a2"}, "handoff needs --reason"},
		{[]string{"reserve", "--dir", dir, "--agent", "a1"}, "reserve needs --files"},
		{[]string{"release", "--dir", dir, "--agent", "a1"}, "release needs --task-id"},
		{[]string{"complete", "--dir", dir, "--agent", "a1"}, "complete needs --task-id"},
		{[]string{"reconcile", "--dir", dir, "x"}, `reconcile: unexpected argument "x"`},
	}
	for _, tc := range cases {
		r := runCLI(t, nil, tc.args...)
		if r.code != 2 || !strings.HasSuffix(r.stderr, "coordination: "+tc.want+"\n"+usageLine) || r.stdout != "" {
			t.Errorf("%q: code %d, stderr:\n%s", tc.args, r.code, r.stderr)
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	r := runCLI(t, nil, "claim", "-h")
	if r.code != 0 || !strings.Contains(r.stderr, "-task-id") {
		t.Fatalf("help: code %d, stderr %q", r.code, r.stderr)
	}
}

// cliStep is one command of a scripted session and what it must print.
// Empty expectations match anything.
type cliStep struct {
	args         []string
	code         int
	stdoutPrefix string
	stdoutSuffix string
	stderrPrefix string
}

func runSteps(t *testing.T, env map[string]string, steps []cliStep) {
	t.Helper()
	for _, s := range steps {
		r := runCLI(t, env, s.args...)
		if !stepMatches(r, s) {
			t.Fatalf("%q: %+v", s.args, r)
		}
	}
}

func stepMatches(r result, s cliStep) bool {
	return r.code == s.code &&
		strings.HasPrefix(r.stdout, s.stdoutPrefix) &&
		strings.HasSuffix(r.stdout, s.stdoutSuffix) &&
		strings.HasPrefix(r.stderr, s.stderrPrefix)
}

func TestWorkflowThroughTheCLI(t *testing.T) {
	dir := writeState(t, cleanState)
	env := map[string]string{envDir: dir}
	runSteps(t, env, []cliStep{{
		args:         []string{"--register", "--name", "Claude", "--sessionID", "ses-9", "--version", "1.0", "--capabilities", "go, docs,"},
		stdoutPrefix: "register agent-1: Claude (session ses-9, version 1.0)\nagent id: agent-1\nstate revision 1\n",
	}})
	env[envAgent] = "agent-1"
	runSteps(t, env, []cliStep{
		{args: []string{"claim", "--task-id", "T2"}, stdoutSuffix: "warning: T2 depends on T1, which is available\nstate revision 2\n"},
		{args: []string{"claim", "--agent", "a1", "--task-id", "T2"}, code: 1,
			stderrPrefix: "coordination: task T2 is already claimed by agent-1 until "},
		{args: []string{"reserve", "--files", "cmd/a.go,cmd/b.go", "cmd/c.go"},
			stdoutPrefix: "reserve T2: reserved cmd/a.go, cmd/b.go, cmd/c.go until "},
		{args: []string{"update", "--task-id", "T2", "--progress", "half", "--expect-revision", "1"}, code: 1,
			stderrPrefix: "coordination: stale update: coordination state is at revision 3, expected 1; reload state and retry\n"},
		{args: []string{"update", "--task-id", "T2", "--progress", "half", "--expect-revision", "3"}},
		{args: []string{"block", "--task-id", "T2", "--reason", "waiting"}},
		{args: []string{"update", "--task-id", "T2", "--progress", "go", "--unblock"}},
		{args: []string{"handoff", "--task-id", "T2", "--to-agent", "a1", "--reason", "timeout"}},
		{args: []string{"release", "--agent", "a1", "--task-id", "T2"}},
		{args: []string{"complete", "--agent", "a1", "--task-id", "T2", "--pr-number", "7"}},
	})
	wantRun(t, runCLI(t, env, "reconcile"), 0, "nothing to change; state stays at revision 9\n", "")
	wantRows(t, runCLI(t, env, "status").stdout, "agent-1 available -", "T2 completed a1", "(none reserved)")
	wantRun(t, runCLI(t, env, "available"), 0, "T1\tP\tone\n", "")
	wantRun(t, runCLI(t, env, "validate"), 0, "0 error(s), 0 warning(s) at revision 9\n", "")
	wantAuditEntries(t, dir, 10)
}

func wantRows(t *testing.T, out string, rows ...string) {
	t.Helper()
	for _, want := range rows {
		if !hasRow(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func wantAuditEntries(t *testing.T, dir string, want int) {
	t.Helper()
	audit, err := os.ReadFile(filepath.Join(dir, "audit.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(audit), "\n- "); n != want {
		t.Fatalf("audit has %d entries, want %d:\n%s", n, want, audit)
	}
}

func TestStatusShowsReservations(t *testing.T) {
	dir := writeState(t, cleanState)
	runCLI(t, nil, "claim", "--dir", dir, "--agent", "a1", "--task-id", "T1")
	runCLI(t, nil, "reserve", "--dir", dir, "--agent", "a1", "--files", "x/b.go,x/a.go")
	wantRows(t, runCLI(t, nil, "status", "--dir", dir).stdout, "x/a.go a1 T1", "x/b.go a1 T1")
}

// hasRow reports whether a line of out, with its runs of spaces collapsed,
// starts with prefix.
func hasRow(out, prefix string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.Join(strings.Fields(line), " "), prefix) {
			return true
		}
	}
	return false
}

func TestAvailableShowsExpiredClaimsAndNone(t *testing.T) {
	files := maps.Clone(cleanState)
	files["registry.json"] = `{"version": "1.0", "tasks": [
  {"id": "T1", "title": "one", "phase": "P", "status": "claimed", "dependencies": ["T9"], "acceptanceCriteria": [],
   "estimatedScope": "s", "requiredFiles": [], "assignedTo": "a1", "claimedAt": null, "expiresAt": "2000-01-01T00:00:00Z", "notes": ""}]}`
	dir := writeState(t, files)
	wantRun(t, runCLI(t, nil, "available", "--dir", dir), 0,
		"T1\tP\tone\t(expired claim by a1)\n  warning: T1 depends on T9, which is not defined in registry.json\n", "")
	runCLI(t, nil, "claim", "--dir", dir, "--agent", "a2", "--task-id", "T1")
	runCLI(t, nil, "complete", "--dir", dir, "--agent", "a2", "--task-id", "T1")
	wantRun(t, runCLI(t, nil, "available", "--dir", dir), 0, "no tasks available\n", "")

	files["registry.json"] = strings.Replace(files["registry.json"], "2000-01-01T00:00:00Z", "soon", 1)
	wantRun(t, runCLI(t, nil, "available", "--dir", writeState(t, files)), 1, "",
		"coordination: task T1 has invalid expiry \"soon\"\n")
}

func TestValidateReportsCommittedStateErrors(t *testing.T) {
	r := runCLI(t, nil, "validate", "--dir", repoState(t))
	if r.code != 1 || r.stderr != "coordination: validation found errors\n" {
		t.Fatalf("validate: %+v", r)
	}
	for _, want := range []string{
		"error: TASK-LC-001: dependency TASK-CORE-002 is not defined in registry.json\n",
		"error: TASK-LC-002: dependency TASK-CORE-001 is not defined in registry.json\n",
		"9 error(s), 0 warning(s) at revision 0\n",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("validate output lacks %q:\n%s", want, r.stdout)
		}
	}
}

func TestReconcileActor(t *testing.T) {
	dir := repoState(t)
	r := runCLI(t, nil, "reconcile", "--dir", dir)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "reconcile agent-9: marked idle: no heartbeat since -\n") {
		t.Fatalf("reconcile: %+v", r)
	}
	audit, err := os.ReadFile(filepath.Join(dir, "audit.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), " | rev 1 | operator | reconcile | agent-9 | ") {
		t.Fatalf("audit:\n%s", audit)
	}
	wantRun(t, runCLI(t, map[string]string{envAgent: "agent-5"}, "reconcile", "--dir", dir), 0,
		"nothing to change; state stays at revision 1\n", "")
}

func TestCommandsFailClosedWithoutState(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{
		{"status"}, {"available"}, {"validate"},
		{"claim", "--agent", "a1", "--task-id", "T1"},
	} {
		r := runCLI(t, map[string]string{envDir: missing}, args...)
		if r.code != 1 || !strings.HasPrefix(r.stderr, "coordination: create lock ") {
			t.Errorf("%q: %+v", args, r)
		}
	}
}

func TestDirectoryLookupFailure(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"claim", "--agent", "a1", "--task-id", "T1"}} {
		wantRun(t, runCLI(t, nil, args...), 1, "",
			"coordination: locate the coordination directory: no git here; pass --dir or set $GSC_COORDINATION_DIR\n")
	}
}

func TestDash(t *testing.T) {
	s := " "
	if dash(nil) != "-" || dash(&s) != "-" {
		t.Fatal("dash of empty value is not -")
	}
}

func TestFirstWorktree(t *testing.T) {
	good := "worktree /home/u/repo\nHEAD abc\nbranch refs/heads/main\n\nworktree /home/u/wt\n"
	if got, err := firstWorktree([]byte(good)); err != nil || got != "/home/u/repo" {
		t.Fatalf("firstWorktree = %q, %v", got, err)
	}
	bad := map[string]string{
		"":                    "git worktree list printed nothing",
		"HEAD abc\n":          `unexpected git worktree list output "HEAD abc"`,
		"worktree /r\nbare\n": "the main worktree is a bare repository",
		"worktree \n":         `unexpected git worktree list output "worktree "`,
	}
	for out, want := range bad {
		if _, err := firstWorktree([]byte(out)); err == nil || err.Error() != want {
			t.Errorf("firstWorktree(%q) error = %v, want %q", out, err, want)
		}
	}
}

func TestMainWorktreeDir(t *testing.T) {
	saved := gitWorktreeList
	t.Cleanup(func() { gitWorktreeList = saved })
	gitWorktreeList = func() ([]byte, error) { return []byte("worktree /srv/repo\n"), nil }
	if got, err := mainWorktreeDir(); err != nil || got != filepath.Join(filepath.FromSlash("/srv/repo"), "COORDINATION") {
		t.Fatalf("mainWorktreeDir = %q, %v", got, err)
	}
	gitWorktreeList = func() ([]byte, error) { return nil, errors.New("not a git repository") }
	if _, err := mainWorktreeDir(); err == nil || err.Error() != "git worktree list: not a git repository" {
		t.Fatalf("error = %v", err)
	}
	gitWorktreeList = func() ([]byte, error) { return nil, nil }
	if _, err := mainWorktreeDir(); err == nil || err.Error() != "git worktree list printed nothing" {
		t.Fatalf("error = %v", err)
	}
}

// TestMainWorktreeDirFromRealGit checks the default lookup against git
// itself: a linked worktree resolves to the main worktree's directory.
func TestMainWorktreeDirFromRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	main := filepath.Join(root, "main")
	gitRun(t, root, "init", "-q", main)
	gitRun(t, main, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(root, "linked")
	gitRun(t, main, "worktree", "add", "-q", "-b", "side", linked)
	t.Chdir(linked)
	got, err := mainWorktreeDir()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(main)
	if err != nil {
		t.Fatal(err)
	}
	gotParent, err := filepath.EvalSymlinks(filepath.Dir(got))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(gotParent, want) || filepath.Base(got) != "COORDINATION" {
		t.Fatalf("mainWorktreeDir = %q, want %q", got, filepath.Join(want, "COORDINATION"))
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDefaultDirectoryFromMainWorktree(t *testing.T) {
	dir := writeState(t, cleanState)
	var out, errOut bytes.Buffer
	c := &cli{
		stdout: &out, stderr: &errOut,
		getenv:  func(string) string { return "" },
		findDir: func() (string, error) { return dir, nil },
	}
	if code := c.run([]string{"status"}); code != 0 || !strings.HasPrefix(out.String(), "coordination state "+dir+" at revision 0\n") {
		t.Fatalf("status: code %d\n%s%s", code, out.String(), errOut.String())
	}
}
