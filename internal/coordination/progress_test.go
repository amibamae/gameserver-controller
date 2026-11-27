package coordination

import (
	"reflect"
	"testing"
	"time"
)

func TestUpdateRecordsProgressAndHeartbeat(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	c.advance(20 * time.Hour)
	mustApply(t, s, Update("a1", "T1", "step 2: done", false))
	st := mustLoad(t, s)
	task := st.Task("T1")
	exp := ts(20*time.Hour + ClaimTTL)
	if task.Progress != "step 2: done" || task.ProgressAt != ts(20*time.Hour) || deref(task.ExpiresAt) != exp {
		t.Fatalf("task = %+v", task)
	}
	if r := st.Files.CurrentOwners["x/a.go"]; r.ExpiresAt != exp {
		t.Fatalf("reservation expiry = %s, want %s", r.ExpiresAt, exp)
	}
	if deref(st.Agent("a1").LastHeartbeat) != ts(20*time.Hour) {
		t.Fatalf("heartbeat = %s", deref(st.Agent("a1").LastHeartbeat))
	}
}

func TestBlockAndUnblock(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	wantApplyErr(t, s, Update("a1", "T1", "x", true), "task T1 is not blocked")
	mustApply(t, s, Block("a1", "T1", "waiting for T9"))
	if task := mustLoad(t, s).Task("T1"); task.Status != StatusBlocked || task.BlockedReason != "waiting for T9" {
		t.Fatalf("task = %+v", task)
	}
	// Progress while blocked keeps the block.
	mustApply(t, s, Update("a1", "T1", "still waiting", false))
	if task := mustLoad(t, s).Task("T1"); task.Status != StatusBlocked {
		t.Fatalf("status = %s", task.Status)
	}
	res := mustApply(t, s, Update("a1", "T1", "resumed", true))
	wantEvents := []Event{
		{"a1", "update", "T1", "resumed"},
		{"a1", "unblock", "T1", "cleared blocker: waiting for T9"},
	}
	if !reflect.DeepEqual(res.Events, wantEvents) {
		t.Fatalf("events = %+v", res.Events)
	}
	if task := mustLoad(t, s).Task("T1"); task.Status != StatusClaimed || task.BlockedReason != "" {
		t.Fatalf("task = %+v", task)
	}
}

func TestUpdateAndBlockNeedALiveClaim(t *testing.T) {
	s, c := newTestStore(t)
	wantApplyErr(t, s, Update("zz", "T1", "x", false), `agent "zz" is not registered; run register first`)
	wantApplyErr(t, s, Block("a1", "NOPE", "x"), `unknown task "NOPE"`)
	wantApplyErr(t, s, Block("a1", "T1", "x"), "task T1 is available, not claimed")
	mustApply(t, s, Claim("a1", "T1"))
	wantApplyErr(t, s, Update("a2", "T1", "x", false), "task T1 is claimed by a1, not a2")
	c.advance(ClaimTTL)
	wantApplyErr(t, s, Update("a1", "T1", "x", false), "claim on task T1 by a1 expired at "+ts(ClaimTTL)+"; claim it again")
}

func TestRequireLiveOwnerRejectsBadExpiry(t *testing.T) {
	task := &Task{ID: "T", Status: StatusClaimed, AssignedTo: strPtr("a"), ExpiresAt: strPtr("never")}
	wantErr(t, requireLiveOwner(task, "a", t0), `task T has invalid expiry "never"`)
}

func TestRegisterNewAgent(t *testing.T) {
	s, _ := newTestStore(t)
	res := mustApply(t, s, Register(Registration{
		Name: "Claude", SessionID: "ses-1", Version: "2", Worktree: "/w", Capabilities: []string{"go"},
	}))
	if !reflect.DeepEqual(res.Notes, []string{"agent id: agent-1"}) {
		t.Fatalf("notes = %q", res.Notes)
	}
	if res.Events[0] != (Event{"agent-1", "register", "agent-1", "Claude (session ses-1, version 2)"}) {
		t.Fatalf("event = %+v", res.Events[0])
	}
	a := mustLoad(t, s).Agent("agent-1")
	want := Agent{
		ID: "agent-1", Name: "Claude", Role: "implementation", Capabilities: []string{"go"},
		Status: AgentAvailable, LastHeartbeat: strPtr(ts(0)), Worktree: strPtr("/w"),
		SessionID: "ses-1", Version: "2", RegisteredAt: ts(0),
	}
	if !reflect.DeepEqual(*a, want) {
		t.Fatalf("agent = %+v", *a)
	}
}

func TestRegisterReusesSessionAndID(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Register(Registration{Name: "Claude", SessionID: "ses-1"}))
	c.advance(time.Hour)
	res := mustApply(t, s, Register(Registration{Name: "Claude 2", SessionID: "ses-1", Role: "review"}))
	if res.Events[0].Action != "re-register" || res.Notes[0] != "agent id: agent-1" {
		t.Fatalf("result = %+v", res)
	}
	// An idle agent registering by ID becomes available again.
	mustApply(t, s, Register(Registration{ID: "a3", Name: "three"}))
	st := mustLoad(t, s)
	if a := st.Agent("agent-1"); a.Name != "Claude 2" || a.Role != "review" || deref(a.LastHeartbeat) != ts(time.Hour) {
		t.Fatalf("agent-1 = %+v", a)
	}
	if st.Agent("a3").Status != AgentAvailable || len(st.Agents.Agents) != 4 {
		t.Fatalf("a3 = %+v, agents = %d", st.Agent("a3"), len(st.Agents.Agents))
	}
	// A new session gets a new ID past the highest agent-N.
	res = mustApply(t, s, Register(Registration{Name: "x", SessionID: "ses-2"}))
	if res.Notes[0] != "agent id: agent-2" {
		t.Fatalf("notes = %q", res.Notes)
	}
	res = mustApply(t, s, Register(Registration{ID: "custom", Name: "y"}))
	if res.Events[0].Action != "register" || mustLoad(t, s).Agent("custom") == nil {
		t.Fatalf("result = %+v", res)
	}
}

func TestRegisterNeedsAName(t *testing.T) {
	s, _ := newTestStore(t)
	wantApplyErr(t, s, Register(Registration{Name: "  "}), "agent name must not be empty")
}
