package coordination

import (
	"fmt"
	"time"
)

// Claim gives task to agent for ClaimTTL. It fails while another agent
// holds an unexpired claim, and while agent holds another task. An expired
// claim is taken over. Unfinished dependencies are reported as notes, not
// refused: the spec has the coordinator warn about them.
func Claim(agent, task string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		a, err := st.requireAgent(agent)
		if err != nil {
			return Outcome{}, err
		}
		t, err := st.requireTask(task)
		if err != nil {
			return Outcome{}, err
		}
		if err := checkAgentFree(st, agent, task, now); err != nil {
			return Outcome{}, err
		}
		takeover, err := claimable(t, now)
		if err != nil {
			return Outcome{}, err
		}
		var events []Event
		if takeover != "" {
			n := st.clearClaim(t)
			events = append(events, Event{agent, "reconcile", task,
				fmt.Sprintf("took over expired claim from %s, dropped %d reservation(s)", takeover, n)})
		}
		t.Status = StatusClaimed
		t.AssignedTo = strPtr(agent)
		t.ClaimedAt = strPtr(formatTime(now))
		t.ReleasedBy = ""
		st.refreshClaim(t, a, now)
		events = append(events, Event{agent, "claim", task, "claimed until " + *t.ExpiresAt})
		return Outcome{Events: events, Notes: st.DependencyWarnings(t)}, nil
	}
}

func checkAgentFree(st *State, agent, task string, now time.Time) error {
	other, err := st.heldTask(agent, task, now)
	if err != nil {
		return err
	}
	if other != "" {
		return fmt.Errorf("agent %s already holds task %s; complete, release or hand it off first", agent, other)
	}
	return nil
}

// claimable reports whether t can be claimed now. It returns the previous
// owner when the claim is an expired one being taken over.
func claimable(t *Task, now time.Time) (string, error) {
	switch t.Status {
	case StatusAvailable:
		return "", nil
	case StatusCompleted:
		return "", fmt.Errorf("task %s is already completed", t.ID)
	case StatusClaimed, StatusBlocked:
		expired, err := ClaimExpired(t, now)
		if err != nil {
			return "", err
		}
		if !expired {
			return "", fmt.Errorf("task %s is already claimed by %s until %s", t.ID, deref(t.AssignedTo), deref(t.ExpiresAt))
		}
		return deref(t.AssignedTo), nil
	default:
		return "", fmt.Errorf("task %s has unknown status %q", t.ID, t.Status)
	}
}

// DependencyWarnings lists t's dependencies that are undefined or not yet
// completed.
func (s *State) DependencyWarnings(t *Task) []string {
	var notes []string
	for _, dep := range t.Dependencies {
		d := s.Task(dep)
		switch {
		case d == nil:
			notes = append(notes, fmt.Sprintf("warning: %s depends on %s, which is not defined in %s", t.ID, dep, RegistryFile))
		case d.Status != StatusCompleted:
			notes = append(notes, fmt.Sprintf("warning: %s depends on %s, which is %s", t.ID, dep, d.Status))
		}
	}
	return notes
}

// Handoff moves agent's live claim on task, and its reservations for it,
// to another registered agent that holds no other task.
func Handoff(agent, task, to, reason string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		t, err := st.requireTask(task)
		if err != nil {
			return Outcome{}, err
		}
		if err := requireLiveOwner(t, agent, now); err != nil {
			return Outcome{}, err
		}
		if to == agent {
			return Outcome{}, fmt.Errorf("task %s is already claimed by %s", task, to)
		}
		target, err := st.requireAgent(to)
		if err != nil {
			return Outcome{}, err
		}
		if err := checkAgentFree(st, to, task, now); err != nil {
			return Outcome{}, err
		}
		st.freeAgent(agent, task)
		st.moveReservations(task, agent, to)
		t.AssignedTo = strPtr(to)
		t.HandoffFrom = agent
		t.ClaimedAt = strPtr(formatTime(now))
		st.refreshClaim(t, target, now)
		detail := fmt.Sprintf("handed off to %s until %s: %s", to, *t.ExpiresAt, reason)
		return Outcome{Events: []Event{{agent, "handoff", task, detail}}}, nil
	}
}

func (s *State) moveReservations(task, from, to string) {
	for key, r := range s.Files.CurrentOwners {
		if r.Task == task && r.Agent == from {
			r.Agent = to
			s.Files.CurrentOwners[key] = r
		}
	}
}

// Release returns task to the pool and drops its reservations. Only the
// owner may release a claim, expired or not; with forceReconcile any
// registered agent may release a claim that has expired.
func Release(agent, task string, forceReconcile bool) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		a, err := st.requireAgent(agent)
		if err != nil {
			return Outcome{}, err
		}
		t, err := st.requireTask(task)
		if err != nil {
			return Outcome{}, err
		}
		if !isHeld(t) {
			return Outcome{}, fmt.Errorf("task %s is %s, not claimed", task, t.Status)
		}
		owner := deref(t.AssignedTo)
		if err := checkReleaser(t, agent, forceReconcile, now); err != nil {
			return Outcome{}, err
		}
		touchAgent(a, now)
		n := st.clearClaim(t)
		t.ReleasedBy = ""
		if owner == agent {
			t.ReleasedBy = agent
		}
		detail := fmt.Sprintf("released claim of %s, dropped %d reservation(s)", owner, n)
		return Outcome{Events: []Event{{agent, "release", task, detail}}}, nil
	}
}

func checkReleaser(t *Task, agent string, force bool, now time.Time) error {
	owner := deref(t.AssignedTo)
	if owner == agent {
		return nil
	}
	if !force {
		return fmt.Errorf("task %s is claimed by %s; only the owner can release it (use --force-reconcile once the claim has expired)", t.ID, owner)
	}
	expired, err := ClaimExpired(t, now)
	if err != nil {
		return err
	}
	if !expired {
		return fmt.Errorf("task %s is claimed by %s until %s; --force-reconcile only releases expired claims", t.ID, owner, deref(t.ExpiresAt))
	}
	return nil
}

// Complete marks task completed. The agent must hold a live claim on it,
// or be the agent that last released it (the spec's release-then-complete
// order) while nobody else has claimed it since.
func Complete(agent, task, pr string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		a, err := st.requireAgent(agent)
		if err != nil {
			return Outcome{}, err
		}
		t, err := st.requireTask(task)
		if err != nil {
			return Outcome{}, err
		}
		if err := checkCompleter(t, agent, now); err != nil {
			return Outcome{}, err
		}
		n := st.dropReservations(task)
		st.freeAgent(agent, task)
		touchAgent(a, now)
		t.Status = StatusCompleted
		t.AssignedTo = strPtr(agent)
		t.ExpiresAt = nil
		t.BlockedReason = ""
		t.ReleasedBy = ""
		t.CompletedBy = agent
		t.CompletedAt = formatTime(now)
		t.PRNumber = pr
		detail := fmt.Sprintf("completed, dropped %d reservation(s)", n)
		if pr != "" {
			detail += ", PR " + pr
		}
		return Outcome{Events: []Event{{agent, "complete", task, detail}}}, nil
	}
}

func checkCompleter(t *Task, agent string, now time.Time) error {
	if t.Status == StatusCompleted {
		return fmt.Errorf("task %s is already completed", t.ID)
	}
	if t.Status == StatusAvailable && t.ReleasedBy == agent {
		return nil
	}
	return requireLiveOwner(t, agent, now)
}
