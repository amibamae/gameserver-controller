package coordination

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func findingStrings(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.String())
	}
	return out
}

// TestValidateCommittedState runs Validate on a copy of the repository's
// COORDINATION directory. It pins the broken references the seed state
// ships with, including the TASK-CORE-001 and TASK-CORE-002 dependencies
// that no task defines.
func TestValidateCommittedState(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{RegistryFile, AgentsFile, FilesFile, DepsFile, AuditFile} {
		data, err := os.ReadFile(filepath.Join("..", "..", "COORDINATION", name))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, name), string(data))
	}
	s := NewStore(dir)
	st := mustLoad(t, s)
	got := findingStrings(Validate(st, t0))
	want := []string{
		"error: TASK-LC-001: dependency TASK-CORE-002 is not defined in registry.json",
		"error: TASK-LC-002: dependency TASK-CORE-001 is not defined in registry.json",
		`error: TASK-LC-002: claimedAt "2026-11-XXT00:00:00Z" is not an RFC 3339 time`,
		"error: TASK-LC-003: dependency TASK-CORE-002 is not defined in registry.json",
		"error: TASK-LC-004: dependency TASK-CORE-001 is not defined in registry.json",
		"error: agent-9: currentTask monitoring-agent-coordination is not defined in registry.json",
		"error: DEP-CORE-001: requiredBy TASK-CORE-001 is not defined in registry.json",
		"error: DEP-ADAPTER-VELOCITY: requiredBy TASK-PROD-001 is not defined in registry.json",
		"error: DEP-ADAPTER-DNS: requiredBy TASK-PROD-002 is not defined in registry.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings =\n%s", strings.Join(got, "\n"))
	}
}

func TestValidateCleanStateHasOnlySeededFinding(t *testing.T) {
	s, _ := newTestStore(t)
	got := findingStrings(Validate(mustLoad(t, s), t0))
	if !reflect.DeepEqual(got, []string{"error: T2: dependency MISSING is not defined in registry.json"}) {
		t.Fatalf("findings = %q", got)
	}
}

func TestValidateReportsBrokenState(t *testing.T) {
	s, _ := newTestStore(t)
	st := mustLoad(t, s)
	bad := strPtr("bad")
	st.Registry.Tasks = append(st.Registry.Tasks,
		Task{ID: "T1", Status: "parked", Dependencies: []string{"T1"}},
		Task{ID: "", Status: StatusAvailable},
		Task{ID: "H1", Status: StatusClaimed},
		Task{ID: "H2", Status: StatusClaimed, AssignedTo: strPtr("a1")},
		Task{ID: "H3", Status: StatusBlocked, AssignedTo: strPtr("ghost"), ExpiresAt: strPtr(ts(-1)), HandoffFrom: "gone",
			ProgressAt: "bad", CompletedAt: "bad"},
		Task{ID: "H4", Status: StatusClaimed, AssignedTo: strPtr("a2"), ExpiresAt: strPtr(ts(ClaimTTL)), ClaimedAt: bad},
	)
	st.Agents.Agents = append(st.Agents.Agents,
		Agent{ID: "a1", Status: "lost", HandoffFrom: "gone", LastHeartbeat: bad, CurrentTask: strPtr("T1")},
		Agent{ID: "a4", Status: AgentActive, CurrentTask: strPtr("T3")},
	)
	st.Files.CurrentOwners = map[string]Reservation{
		"r/a": {Agent: "ghost", Task: "NOPE", ReservedAt: "bad", ExpiresAt: "bad"},
		"r/b": {Agent: "a1", Task: "T1", ReservedAt: ts(0), ExpiresAt: ts(-1)},
	}
	st.Deps.Dependencies = append(st.Deps.Dependencies, Dependency{ID: "DEP-1"})
	got := findingStrings(Validate(st, t0))
	want := []string{
		"error: T1: duplicate task id in registry.json",
		"error: registry.json: a task has an empty id",
		"error: a1: duplicate agent id in .agents.json",
		"error: DEP-1: duplicate dependency id in .deps.json",
		"error: T2: dependency MISSING is not defined in registry.json",
		`error: T1: unknown status "parked"`,
		"error: T1: depends on itself",
		"error: H1: is claimed but has no assignedTo",
		"error: H2: is claimed but has no expiresAt",
		"error: H3: assignedTo ghost is not a registered agent",
		"error: H3: handoffFrom gone is not a registered agent",
		`error: H3: progressAt "bad" is not an RFC 3339 time`,
		`error: H3: completedAt "bad" is not an RFC 3339 time`,
		"warning: H3: claim by ghost expired at " + ts(-1) + "; run reconcile",
		`error: H4: claimedAt "bad" is not an RFC 3339 time`,
		`warning: H4: claimed by a2, but that agent's currentTask is ""`,
		`error: a1: unknown status "lost"`,
		"error: a1: handoffFrom gone is not a registered agent",
		`error: a1: lastHeartbeat "bad" is not an RFC 3339 time`,
		"warning: a1: currentTask T1 is not claimed by this agent",
		"warning: a4: currentTask T3 is not claimed by this agent",
		"error: r/a: reservation agent ghost is not a registered agent",
		"error: r/a: reserved for NOPE, which is not defined in registry.json",
		`error: r/a: reservedAt "bad" is not an RFC 3339 time`,
		`error: r/a: expiresAt "bad" is not an RFC 3339 time`,
		"warning: r/b: reserved by a1 for T1, which that agent no longer holds; run reconcile",
		"warning: r/b: reservation by a1 expired at " + ts(-1) + "; run reconcile",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findings =\n%s", strings.Join(got, "\n"))
	}
}

// availableSummary lists Available's result as "<task> by <expired
// owner> warnings <count>".
func availableSummary(t *testing.T, st *State, now time.Time) []string {
	t.Helper()
	got, err := Available(st, now)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	out := make([]string, 0, len(got))
	for _, c := range got {
		out = append(out, fmt.Sprintf("%s by %s warnings %d", c.Task.ID, orDash(c.ExpiredOwner), len(c.Warnings)))
	}
	return out
}

func TestAvailableListsClaimableTasks(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	st := mustLoad(t, s)
	wantEqual(t, "available", availableSummary(t, st, t0), []string{"T2 by - warnings 2"})
	c.advance(ClaimTTL)
	wantEqual(t, "available after expiry", availableSummary(t, st, c.now),
		[]string{"T1 by a1 warnings 0", "T2 by - warnings 2"})
	st.Task("T1").ExpiresAt = strPtr("bad")
	_, err := Available(st, t0)
	wantErr(t, err, `task T1 has invalid expiry "bad"`)
}
