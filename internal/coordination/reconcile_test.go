package coordination

import (
	"reflect"
	"testing"
	"time"
)

func TestReconcileReleasesAbandonedOwnership(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	mustApply(t, s, Claim("a2", "T2"))
	editRegistry(t, s, func(st *State) {
		// An orphan: a2's reservation for a task a2 does not hold.
		st.Files.CurrentOwners["z/orphan.go"] = Reservation{Agent: "a2", Task: "T3", ReservedAt: ts(0), ExpiresAt: ts(2 * ClaimTTL)}
		// An active agent that never sent a heartbeat.
		st.Agent("a3").Status = AgentActive
	})
	c.advance(ClaimTTL - time.Hour)
	mustApply(t, s, Update("a2", "T2", "alive", false))
	c.advance(time.Hour)

	res := mustApply(t, s, Reconcile("operator"))
	want := []Event{
		{"operator", "reconcile", "T1", "released claim of a1 that expired at " + ts(ClaimTTL) + ", dropped 1 reservation(s)"},
		{"operator", "reconcile", "z/orphan.go", "dropped reservation of a2 for T3: its agent no longer holds the task"},
		{"operator", "reconcile", "a3", "marked idle: no heartbeat since -"},
	}
	if !reflect.DeepEqual(res.Events, want) {
		t.Fatalf("events =\n%+v\nwant\n%+v", res.Events, want)
	}
	st := mustLoad(t, s)
	if st.Task("T1").Status != StatusAvailable || st.Agent("a1").Status != AgentIdle {
		t.Fatalf("T1 = %+v, a1 = %+v", st.Task("T1"), st.Agent("a1"))
	}
	if st.Task("T2").Status != StatusClaimed || st.Agent("a2").Status != AgentActive {
		t.Fatalf("live claim was touched: %+v", st.Task("T2"))
	}
	if len(st.Files.CurrentOwners) != 0 || st.Agent("a3").Status != AgentIdle {
		t.Fatalf("owners = %v, a3 = %+v", st.Files.CurrentOwners, st.Agent("a3"))
	}
	if res := mustApply(t, s, Reconcile("operator")); res.Committed {
		t.Fatalf("a second reconcile changed %+v", res.Events)
	}
}

func TestReconcileDropsExpiredReservation(t *testing.T) {
	s, _ := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	editRegistry(t, s, func(st *State) {
		st.Files.CurrentOwners["x"] = Reservation{Agent: "a1", Task: "T1", ExpiresAt: ts(-time.Second)}
	})
	res := mustApply(t, s, Reconcile("a2"))
	if len(res.Events) != 1 || res.Events[0].Detail != "dropped reservation of a1 for T1: expired at "+ts(-time.Second) {
		t.Fatalf("events = %+v", res.Events)
	}
}

func TestReconcileIdlesAgentWithOldHeartbeat(t *testing.T) {
	s, c := newTestStore(t)
	editRegistry(t, s, func(st *State) {
		st.Agent("a3").Status = AgentActive
		st.Agent("a3").LastHeartbeat = strPtr(ts(0))
	})
	if res := mustApply(t, s, Reconcile("x")); res.Committed {
		t.Fatalf("a fresh heartbeat was reconciled: %+v", res.Events)
	}
	c.advance(ClaimTTL)
	res := mustApply(t, s, Reconcile("x"))
	if len(res.Events) != 1 || res.Events[0].Detail != "marked idle: no heartbeat since "+ts(0) {
		t.Fatalf("events = %+v", res.Events)
	}
}

func TestReconcileFailsClosedOnBadStamps(t *testing.T) {
	type step = func(*State, string, time.Time) ([]Event, error)
	cases := []struct {
		name string
		step step
		edit func(*State)
		want string
	}{
		{"claim", reconcileClaims, func(st *State) {
			st.Task("T1").Status = StatusClaimed
			st.Task("T1").ExpiresAt = strPtr("x")
		}, `task T1 has invalid expiry "x"`},
		{"reservation", reconcileReservations, func(st *State) {
			st.Files.CurrentOwners["k"] = Reservation{ExpiresAt: "x"}
		}, `reservation k has invalid expiry "x"`},
		{"heartbeat", reconcileAgents, func(st *State) {
			st.Agent("a3").Status = AgentActive
			st.Agent("a3").LastHeartbeat = strPtr("x")
		}, `agent a3 has invalid lastHeartbeat "x"`},
		{"held task of active agent", reconcileAgents, func(st *State) {
			st.Agent("a3").Status = AgentActive
			st.Task("T1").Status = StatusBlocked
			st.Task("T1").AssignedTo = strPtr("a3")
			st.Task("T1").ExpiresAt = strPtr("x")
		}, `task T1 has invalid expiry "x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			st := mustLoad(t, s)
			tc.edit(st)
			_, err := tc.step(st, "x", t0)
			wantErr(t, err, tc.want)
			_, err = Reconcile("x")(st, t0)
			wantErr(t, err, tc.want)
		})
	}
}

func TestReconcileKeepsLiveOwnership(t *testing.T) {
	s, c := newTestStore(t)
	mustApply(t, s, Claim("a1", "T1"))
	mustApply(t, s, Reserve("a1", "T1", []string{"x/a.go"}))
	c.advance(ClaimTTL - time.Second)
	if res := mustApply(t, s, Reconcile("x")); res.Committed {
		t.Fatalf("reconcile released live ownership: %+v", res.Events)
	}
	if len(mustLoad(t, s).Files.CurrentOwners) != 1 {
		t.Fatal("live reservation dropped")
	}
}
