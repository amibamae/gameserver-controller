package coordination

import (
	"fmt"
	"sort"
	"time"
)

// Reconcile releases abandoned ownership: claims and reservations past
// their expiry, reservations whose task their agent no longer holds, and
// active agents with no heartbeat for ClaimTTL and no live claim, which
// become idle. actor names who ran it in the audit log.
func Reconcile(actor string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		var events []Event
		steps := []func(*State, string, time.Time) ([]Event, error){
			reconcileClaims, reconcileReservations, reconcileAgents,
		}
		for _, step := range steps {
			more, err := step(st, actor, now)
			if err != nil {
				return Outcome{}, err
			}
			events = append(events, more...)
		}
		return Outcome{Events: events}, nil
	}
}

func reconcileClaims(st *State, actor string, now time.Time) ([]Event, error) {
	var events []Event
	for i := range st.Registry.Tasks {
		t := &st.Registry.Tasks[i]
		if !isHeld(t) {
			continue
		}
		expired, err := ClaimExpired(t, now)
		if err != nil {
			return nil, err
		}
		if !expired {
			continue
		}
		owner, exp := deref(t.AssignedTo), deref(t.ExpiresAt)
		n := st.clearClaim(t)
		t.ReleasedBy = ""
		st.markIdle(owner)
		events = append(events, Event{actor, "reconcile", t.ID,
			fmt.Sprintf("released claim of %s that expired at %s, dropped %d reservation(s)", owner, exp, n)})
	}
	return events, nil
}

// markIdle sets an agent whose claim was reconciled away to idle, unless it
// has since moved on to another task.
func (s *State) markIdle(agent string) {
	if a := s.Agent(agent); a != nil && a.CurrentTask == nil {
		a.Status = AgentIdle
	}
}

func reconcileReservations(st *State, actor string, now time.Time) ([]Event, error) {
	keys := make([]string, 0, len(st.Files.CurrentOwners))
	for key := range st.Files.CurrentOwners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var events []Event
	for _, key := range keys {
		r := st.Files.CurrentOwners[key]
		why, err := st.abandonedReservation(key, r, now)
		if err != nil {
			return nil, err
		}
		if why == "" {
			continue
		}
		delete(st.Files.CurrentOwners, key)
		events = append(events, Event{actor, "reconcile", key,
			fmt.Sprintf("dropped reservation of %s for %s: %s", r.Agent, r.Task, why)})
	}
	return events, nil
}

// abandonedReservation says why a reservation should go, or "" to keep it.
func (s *State) abandonedReservation(key string, r Reservation, now time.Time) (string, error) {
	expired, err := stampExpired("reservation "+key, r.ExpiresAt, now)
	if err != nil {
		return "", err
	}
	if expired {
		return "expired at " + r.ExpiresAt, nil
	}
	t := s.Task(r.Task)
	if t == nil || !isHeld(t) || deref(t.AssignedTo) != r.Agent {
		return "its agent no longer holds the task", nil
	}
	return "", nil
}

func reconcileAgents(st *State, actor string, now time.Time) ([]Event, error) {
	var events []Event
	for i := range st.Agents.Agents {
		a := &st.Agents.Agents[i]
		stale, err := staleAgent(st, a, now)
		if err != nil {
			return nil, err
		}
		if !stale {
			continue
		}
		a.Status = AgentIdle
		a.CurrentTask = nil
		events = append(events, Event{actor, "reconcile", a.ID,
			fmt.Sprintf("marked idle: no heartbeat since %s", orDash(deref(a.LastHeartbeat)))})
	}
	return events, nil
}

// staleAgent reports whether a is active but has neither a live claim nor
// a heartbeat within ClaimTTL.
func staleAgent(st *State, a *Agent, now time.Time) (bool, error) {
	if a.Status != AgentActive {
		return false, nil
	}
	held, err := st.heldTask(a.ID, "", now)
	if err != nil || held != "" {
		return false, err
	}
	if a.LastHeartbeat == nil {
		return true, nil
	}
	beat, err := time.Parse(time.RFC3339, *a.LastHeartbeat)
	if err != nil {
		return false, fmt.Errorf("agent %s has invalid lastHeartbeat %q", a.ID, *a.LastHeartbeat)
	}
	return !now.Before(beat.Add(ClaimTTL)), nil
}
