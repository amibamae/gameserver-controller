package coordination

import (
	"strings"
	"testing"
	"time"
)

func TestClaimSetsOwnerExpiryAndAgent(t *testing.T) {
	s, _ := newTestStore(t)
	before := mustLoad(t, s)
	res := mustApply(t, s, Claim("a1", "T2"))
	wantEqual(t, "notes", res.Notes, []string{
		"warning: T2 depends on T1, which is available",
		"warning: T2 depends on MISSING, which is not defined in registry.json",
	})
	st := mustLoad(t, s)
	wantTask := *before.Task("T2")
	wantTask.Status = StatusClaimed
	wantTask.AssignedTo = strPtr("a1")
	wantTask.ClaimedAt = strPtr(ts(0))
	wantTask.ExpiresAt = strPtr(ts(ClaimTTL))
	wantEqual(t, "task", *st.Task("T2"), wantTask)
	wantAgent := *before.Agent("a1")
	wantAgent.Status = AgentActive
	wantAgent.CurrentTask = strPtr("T2")
	wantAgent.LastHeartbeat = strPtr(ts(0))
	wantEqual(t, "agent", *st.Agent("a1"), wantAgent)
	wantEqual(t, "audit", auditLines(t, s), []string{
		"- " + ts(0) + " | rev 1 | a1 | claim | T2 | claimed until " + ts(ClaimTTL),
	})
}

func TestClaimRejections(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	cases := []struct {
		name string
		m    Mutation
		want string
	}{
		{"unregistered agent", Claim("nobody", "T2"), `agent "nobody" is not registered; run register first`},
		{"unknown task", Claim("a2", "NOPE"), `unknown task "NOPE"`},
		{"already claimed", Claim("a2", "T1"), "task T1 is already claimed by a1 until " + ts(ClaimTTL)},
		{"own claim again", Claim("a1", "T1"), "task T1 is already claimed by a1 until " + ts(ClaimTTL)},
		{"agent busy", Claim("a1", "T2"), "agent a1 already holds task T1; complete, release or hand it off first"},
		{"completed", Claim("a2", "T3"), "task T3 is already completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { wantApplyErr(t, s, tc.m, tc.want) })
	}
}

func TestClaimRejectsUnknownStatusAndBadExpiry(t *testing.T) {
	s, _ := newTestStore(t)
	editRegistry(t, s, func(st *State) { st.Task("T1").Status = "parked" })
	wantApplyErr(t, s, Claim("a1", "T1"), `task T1 has unknown status "parked"`)

	s2, _ := newTestStore(t)
	editRegistry(t, s2, func(st *State) {
		st.Task("T1").Status = StatusClaimed
		st.Task("T1").AssignedTo = strPtr("a2")
		st.Task("T1").ExpiresAt = strPtr("soon")
	})
	wantApplyErr(t, s2, Claim("a1", "T1"), `task T1 has invalid expiry "soon"`)
	wantApplyErr(t, s2, Claim("a2", "T2"), `task T1 has invalid expiry "soon"`)
}

// editRegistry rewrites the state through a raw commit, for tests that need
// shapes the public mutations never produce.
func editRegistry(t *testing.T, s *Store, edit func(*State)) {
	t.Helper()
	mustApply(t, s, func(st *State, _ time.Time) (Outcome, error) {
		edit(st)
		return Outcome{Events: []Event{{"test", "edit", "-", "-"}}}, nil
	})
}

func TestClaimTakesOverExpiredClaim(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	c.advance(ClaimTTL)
	res := mustApply(t, s, Claim("a2", "T1"))
	if res.Events[0].Detail != "took over expired claim from a1, dropped 1 reservation(s)" {
		t.Fatalf("events = %+v", res.Events)
	}
	st := mustLoad(t, s)
	if deref(st.Task("T1").AssignedTo) != "a2" || len(st.Files.CurrentOwners) != 0 {
		t.Fatalf("task = %+v, owners = %v", st.Task("T1"), st.Files.CurrentOwners)
	}
	if a1 := st.Agent("a1"); a1.CurrentTask != nil || a1.Status != AgentAvailable {
		t.Fatalf("previous owner = %+v", a1)
	}
	// An agent whose own claim expired may claim something else.
	mustApply(t, s, Claim("a1", "T2"))
}

func TestHandoffMovesClaimAndReservations(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	c.advance(time.Hour)
	res := mustApply(t, s, Handoff("a1", "T1", "a2", "session timeout"))
	if want := "handed off to a2 until " + ts(time.Hour+ClaimTTL) + ": session timeout"; res.Events[0].Detail != want {
		t.Fatalf("detail = %q", res.Events[0].Detail)
	}
	st := mustLoad(t, s)
	task := st.Task("T1")
	if deref(task.AssignedTo) != "a2" || task.HandoffFrom != "a1" || deref(task.ClaimedAt) != ts(time.Hour) {
		t.Fatalf("task = %+v", task)
	}
	if r := st.Files.CurrentOwners["x/a.go"]; r.Agent != "a2" || r.ExpiresAt != ts(time.Hour+ClaimTTL) {
		t.Fatalf("reservation = %+v", r)
	}
	if st.Agent("a1").CurrentTask != nil || deref(st.Agent("a2").CurrentTask) != "T1" {
		t.Fatalf("agents = %+v / %+v", st.Agent("a1"), st.Agent("a2"))
	}
}

func TestHandoffRejections(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Claim("a2", "T2"))
	cases := []struct {
		name string
		m    Mutation
		want string
	}{
		{"unknown task", Handoff("a1", "NOPE", "a3", "r"), `unknown task "NOPE"`},
		{"not owner", Handoff("a3", "T1", "a2", "r"), "task T1 is claimed by a1, not a3"},
		{"not claimed", Handoff("a1", "T3", "a2", "r"), "task T3 is completed, not claimed"},
		{"to self", Handoff("a1", "T1", "a1", "r"), "task T1 is already claimed by a1"},
		{"unknown target", Handoff("a1", "T1", "zz", "r"), `agent "zz" is not registered; run register first`},
		{"busy target", Handoff("a1", "T1", "a2", "r"), "agent a2 already holds task T2; complete, release or hand it off first"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { wantApplyErr(t, s, tc.m, tc.want) })
	}
}

func TestReleaseByOwner(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go", "x/b.go"}))
	res := mustApply(t, s, Release("a1", "T1", false))
	if res.Events[0].Detail != "released claim of a1, dropped 2 reservation(s)" {
		t.Fatalf("detail = %q", res.Events[0].Detail)
	}
	st := mustLoad(t, s)
	task := st.Task("T1")
	if task.Status != StatusAvailable || task.AssignedTo != nil || task.ExpiresAt != nil || task.ReleasedBy != "a1" {
		t.Fatalf("task = %+v", task)
	}
	if len(st.Files.CurrentOwners) != 0 || st.Agent("a1").CurrentTask != nil {
		t.Fatalf("owners = %v, agent = %+v", st.Files.CurrentOwners, st.Agent("a1"))
	}
}

func TestReleaseOwnExpiredClaim(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	c.advance(2 * ClaimTTL)
	mustApply(t, s, Release("a1", "T1", false))
}

func TestReleaseForceReconcile(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	wantApplyErr(t, s, Release("a2", "T1", false),
		"task T1 is claimed by a1; only the owner can release it (use --force-reconcile once the claim has expired)")
	wantApplyErr(t, s, Release("a2", "T1", true),
		"task T1 is claimed by a1 until "+ts(ClaimTTL)+"; --force-reconcile only releases expired claims")
	c.advance(ClaimTTL)
	mustApply(t, s, Release("a2", "T1", true))
	if task := mustLoad(t, s).Task("T1"); task.Status != StatusAvailable || task.ReleasedBy != "" {
		t.Fatalf("task = %+v", task)
	}
	// The forced release does not let the old owner complete the task.
	wantApplyErr(t, s, Complete("a1", "T1", ""), "task T1 is available, not claimed")
}

func TestReleaseRejections(t *testing.T) {
	s, _ := newTestStore(t)
	wantApplyErr(t, s, Release("zz", "T1", false), `agent "zz" is not registered; run register first`)
	wantApplyErr(t, s, Release("a1", "NOPE", false), `unknown task "NOPE"`)
	wantApplyErr(t, s, Release("a1", "T1", false), "task T1 is available, not claimed")
	editRegistry(t, s, func(st *State) {
		st.Task("T1").Status = StatusClaimed
		st.Task("T1").AssignedTo = strPtr("a2")
		st.Task("T1").ExpiresAt = strPtr("bad")
	})
	wantApplyErr(t, s, Release("a1", "T1", true), `task T1 has invalid expiry "bad"`)
}

func TestCompleteByOwner(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	mustApply(t, s, Block("a1", "T1", "waiting"))
	blocked := *mustLoad(t, s).Task("T1")
	c.advance(time.Minute)
	res := mustApply(t, s, Complete("a1", "T1", "PR-12"))
	wantEqual(t, "detail", res.Events[0].Detail, "completed, dropped 1 reservation(s), PR PR-12")
	st := mustLoad(t, s)
	want := blocked
	want.Status = StatusCompleted
	want.ExpiresAt = nil
	want.BlockedReason = ""
	want.CompletedBy = "a1"
	want.CompletedAt = ts(time.Minute)
	want.PRNumber = "PR-12"
	wantEqual(t, "task", *st.Task("T1"), want)
	wantEqual(t, "agent status", st.Agent("a1").Status, AgentAvailable)
	wantEqual(t, "agent task", st.Agent("a1").CurrentTask, (*string)(nil))
	wantEqual(t, "owners", st.Files.CurrentOwners, map[string]Reservation{})
}

func TestCompleteAfterRelease(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Release("a1", "T1", false))
	wantApplyErr(t, s, Complete("a2", "T1", ""), "task T1 is available, not claimed")
	res := mustApply(t, s, Complete("a1", "T1", ""))
	if res.Events[0].Detail != "completed, dropped 0 reservation(s)" {
		t.Fatalf("detail = %q", res.Events[0].Detail)
	}
}

func TestCompleteRejections(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	wantApplyErr(t, s, Complete("zz", "T1", ""), `agent "zz" is not registered; run register first`)
	wantApplyErr(t, s, Complete("a1", "NOPE", ""), `unknown task "NOPE"`)
	wantApplyErr(t, s, Complete("a1", "T3", ""), "task T3 is already completed")
	wantApplyErr(t, s, Complete("a2", "T1", ""), "task T1 is claimed by a1, not a2")
	c.advance(ClaimTTL)
	wantApplyErr(t, s, Complete("a1", "T1", ""), "claim on task T1 by a1 expired at "+ts(ClaimTTL)+"; claim it again")
}

func TestDependencyWarningsForUnfinishedDependency(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Complete("a1", "T1", ""))
	st := mustLoad(t, s)
	got := strings.Join(st.DependencyWarnings(st.Task("T2")), "\n")
	if got != "warning: T2 depends on MISSING, which is not defined in registry.json" {
		t.Fatalf("warnings = %q", got)
	}
}
