package coordination

import (
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentClaimHasOneWinner forces two agents onto the same snapshot:
// both read revision 0 and both decide the task is free before either
// commits. The compare-and-swap lets one commit; the other's commit is
// stale, so it reloads, re-runs its claim on the winner's state and fails
// with "already claimed".
func TestConcurrentClaimHasOneWinner(t *testing.T) {
	base, _ := newTestStore(t)
	var bothLoaded sync.WaitGroup
	bothLoaded.Add(2)
	agents := []string{"a1", "a2"}
	errs := make([]error, len(agents))
	attempts := make([]int, len(agents))
	var done sync.WaitGroup
	for i, agent := range agents {
		s := NewStore(base.Dir)
		s.Now = base.Now
		s.beforeCommit = func() {
			attempts[i]++
			if attempts[i] == 1 {
				bothLoaded.Done()
				bothLoaded.Wait()
			}
		}
		done.Add(1)
		go func() {
			defer done.Done()
			_, errs[i] = s.Apply(AnyRevision, Claim(agent, "T1"))
		}()
	}
	done.Wait()

	st := mustLoad(t, base)
	winner := deref(st.Task("T1").AssignedTo)
	for i, agent := range agents {
		if agent == winner {
			if errs[i] != nil || attempts[i] != 1 {
				t.Fatalf("winner %s: err = %v, attempts = %d", agent, errs[i], attempts[i])
			}
			continue
		}
		wantErr(t, errs[i], fmt.Sprintf("task T1 is already claimed by %s until %s", winner, ts(ClaimTTL)))
		if attempts[i] != 1 {
			t.Fatalf("loser %s reached commit %d times; its retry must fail before committing", agent, attempts[i])
		}
	}
	if st.Revision() != 1 || len(auditLines(t, base)) != 1 {
		t.Fatalf("revision = %d, audit = %v; want exactly one committed claim", st.Revision(), auditLines(t, base))
	}
}

// TestManyConcurrentClaimsOneWinner races many agents on one task through
// the real lock with no forced interleaving.
func TestManyConcurrentClaimsOneWinner(t *testing.T) {
	base, _ := newTestStore(t)
	const n = 12
	var agents []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("r%d", i)
		mustApply(t, base, Register(Registration{ID: id, Name: id}))
		agents = append(agents, id)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, agent := range agents {
		s := NewStore(base.Dir)
		s.Now = base.Now
		s.MaxAttempts = n + 1 // enough to outlast every other writer
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Apply(AnyRevision, Claim(agent, "T1"))
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d claims succeeded, want 1: %v", wins, errs)
	}
	if st := mustLoad(t, base); st.Revision() != n+1 {
		t.Fatalf("revision = %d, want %d registrations plus one claim", st.Revision(), n)
	}
}
