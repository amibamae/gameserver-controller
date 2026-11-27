package coordination

import (
	"fmt"
	"time"
)

// Task returns the task with id, or nil.
func (s *State) Task(id string) *Task {
	for i := range s.Registry.Tasks {
		if s.Registry.Tasks[i].ID == id {
			return &s.Registry.Tasks[i]
		}
	}
	return nil
}

// Agent returns the agent with id, or nil.
func (s *State) Agent(id string) *Agent {
	for i := range s.Agents.Agents {
		if s.Agents.Agents[i].ID == id {
			return &s.Agents.Agents[i]
		}
	}
	return nil
}

func (s *State) requireTask(id string) (*Task, error) {
	if t := s.Task(id); t != nil {
		return t, nil
	}
	return nil, fmt.Errorf("unknown task %q", id)
}

func (s *State) requireAgent(id string) (*Agent, error) {
	if a := s.Agent(id); a != nil {
		return a, nil
	}
	return nil, fmt.Errorf("agent %q is not registered; run register first", id)
}

// isHeld reports whether t is claimed or blocked, the two owned statuses.
func isHeld(t *Task) bool {
	return t.Status == StatusClaimed || t.Status == StatusBlocked
}

// stampExpired parses an RFC 3339 expiry and reports whether now is at or
// past it. A malformed stamp is an error: the state is not trusted.
func stampExpired(what, stamp string, now time.Time) (bool, error) {
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false, fmt.Errorf("%s has invalid expiry %q", what, stamp)
	}
	return !now.Before(at), nil
}

// ClaimExpired reports whether t's claim has passed its expiry.
func ClaimExpired(t *Task, now time.Time) (bool, error) {
	if t.ExpiresAt == nil {
		return false, nil
	}
	return stampExpired("task "+t.ID, *t.ExpiresAt, now)
}

// requireLiveOwner fails unless agent holds an unexpired claim on t.
func requireLiveOwner(t *Task, agent string, now time.Time) error {
	if !isHeld(t) {
		return fmt.Errorf("task %s is %s, not claimed", t.ID, t.Status)
	}
	if owner := deref(t.AssignedTo); owner != agent {
		return fmt.Errorf("task %s is claimed by %s, not %s", t.ID, owner, agent)
	}
	expired, err := ClaimExpired(t, now)
	if err != nil {
		return err
	}
	if expired {
		return fmt.Errorf("claim on task %s by %s expired at %s; claim it again", t.ID, agent, *t.ExpiresAt)
	}
	return nil
}

// heldTask returns the task other than except that agent holds an
// unexpired claim on, or "".
func (s *State) heldTask(agent, except string, now time.Time) (string, error) {
	for i := range s.Registry.Tasks {
		t := &s.Registry.Tasks[i]
		if t.ID == except || !isHeld(t) || deref(t.AssignedTo) != agent {
			continue
		}
		expired, err := ClaimExpired(t, now)
		if err != nil {
			return "", err
		}
		if !expired {
			return t.ID, nil
		}
	}
	return "", nil
}

// refreshClaim records a heartbeat: the agent is active on t, and t's claim
// and the agent's reservations for t live another ClaimTTL.
func (s *State) refreshClaim(t *Task, a *Agent, now time.Time) {
	exp := formatTime(now.Add(ClaimTTL))
	t.ExpiresAt = strPtr(exp)
	a.LastHeartbeat = strPtr(formatTime(now))
	a.Status = AgentActive
	a.CurrentTask = strPtr(t.ID)
	for key, r := range s.Files.CurrentOwners {
		if r.Task == t.ID && r.Agent == a.ID {
			r.ExpiresAt = exp
			s.Files.CurrentOwners[key] = r
		}
	}
}

// dropReservations removes every reservation made for task and returns
// how many there were.
func (s *State) dropReservations(task string) int {
	n := 0
	for key, r := range s.Files.CurrentOwners {
		if r.Task == task {
			delete(s.Files.CurrentOwners, key)
			n++
		}
	}
	return n
}

// freeAgent clears agent's current task if it is task.
func (s *State) freeAgent(agent, task string) {
	a := s.Agent(agent)
	if a == nil || deref(a.CurrentTask) != task {
		return
	}
	a.CurrentTask = nil
	a.Status = AgentAvailable
}

// clearClaim returns t to the pool and frees its owner and reservations.
func (s *State) clearClaim(t *Task) int {
	s.freeAgent(deref(t.AssignedTo), t.ID)
	t.Status = StatusAvailable
	t.AssignedTo = nil
	t.ClaimedAt = nil
	t.ExpiresAt = nil
	t.BlockedReason = ""
	return s.dropReservations(t.ID)
}

// touchAgent records a heartbeat for an agent that acted without a task.
func touchAgent(a *Agent, now time.Time) {
	a.LastHeartbeat = strPtr(formatTime(now))
}
