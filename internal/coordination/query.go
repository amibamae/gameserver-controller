package coordination

import "time"

// Claimable is a task an agent could claim now.
type Claimable struct {
	Task *Task
	// ExpiredOwner is the agent whose expired claim a new claim would take
	// over, or "" for an unclaimed task.
	ExpiredOwner string
	// Warnings lists undefined or unfinished dependencies.
	Warnings []string
}

// Available lists the tasks Claim would accept: available ones and held
// ones whose claim has expired, in registry order. A malformed expiry is
// an error, as it is for Claim.
func Available(st *State, now time.Time) ([]Claimable, error) {
	var out []Claimable
	for i := range st.Registry.Tasks {
		t := &st.Registry.Tasks[i]
		ok, err := claimableNow(t, now)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		c := Claimable{Task: t, Warnings: st.DependencyWarnings(t)}
		if isHeld(t) {
			c.ExpiredOwner = deref(t.AssignedTo)
		}
		out = append(out, c)
	}
	return out, nil
}

func claimableNow(t *Task, now time.Time) (bool, error) {
	if t.Status == StatusAvailable {
		return true, nil
	}
	if !isHeld(t) {
		return false, nil
	}
	return ClaimExpired(t, now)
}
