package coordination

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Update records progress on agent's live claim on task and counts as a
// heartbeat. With unblock it also returns a blocked task to claimed.
func Update(agent, task, progress string, unblock bool) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		a, t, err := liveClaim(st, agent, task, now)
		if err != nil {
			return Outcome{}, err
		}
		if unblock && t.Status != StatusBlocked {
			return Outcome{}, fmt.Errorf("task %s is not blocked", task)
		}
		events := []Event{{agent, "update", task, progress}}
		if unblock {
			events = append(events, Event{agent, "unblock", task, "cleared blocker: " + t.BlockedReason})
			t.Status = StatusClaimed
			t.BlockedReason = ""
		}
		t.Progress = progress
		t.ProgressAt = formatTime(now)
		st.refreshClaim(t, a, now)
		return Outcome{Events: events}, nil
	}
}

// Block marks agent's live claim on task blocked. The claim stays with the
// agent and the call counts as a heartbeat.
func Block(agent, task, reason string) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		a, t, err := liveClaim(st, agent, task, now)
		if err != nil {
			return Outcome{}, err
		}
		t.Status = StatusBlocked
		t.BlockedReason = reason
		st.refreshClaim(t, a, now)
		return Outcome{Events: []Event{{agent, "block", task, reason}}}, nil
	}
}

func liveClaim(st *State, agent, task string, now time.Time) (*Agent, *Task, error) {
	a, err := st.requireAgent(agent)
	if err != nil {
		return nil, nil, err
	}
	t, err := st.requireTask(task)
	if err != nil {
		return nil, nil, err
	}
	if err := requireLiveOwner(t, agent, now); err != nil {
		return nil, nil, err
	}
	return a, t, nil
}

// Registration describes an agent registering itself. An empty ID reuses
// the agent registered with the same SessionID, or allocates the next free
// agent-N.
type Registration struct {
	ID           string
	Name         string
	Role         string
	SessionID    string
	Version      string
	Worktree     string
	Capabilities []string
}

// Register adds or refreshes an agent. Its note names the agent's ID.
func Register(r Registration) Mutation {
	return func(st *State, now time.Time) (Outcome, error) {
		if strings.TrimSpace(r.Name) == "" {
			return Outcome{}, fmt.Errorf("agent name must not be empty")
		}
		a, action := st.registrationTarget(r, now)
		r.apply(a)
		touchAgent(a, now)
		if a.Status == AgentIdle {
			a.Status = AgentAvailable
		}
		detail := fmt.Sprintf("%s (session %s, version %s)", a.Name, orDash(a.SessionID), orDash(a.Version))
		return Outcome{Events: []Event{{a.ID, action, a.ID, detail}}, Notes: []string{"agent id: " + a.ID}}, nil
	}
}

func (s *State) registrationTarget(r Registration, now time.Time) (*Agent, string) {
	if a := s.findRegistered(r); a != nil {
		return a, "re-register"
	}
	id := r.ID
	if id == "" {
		id = s.nextAgentID()
	}
	s.Agents.Agents = append(s.Agents.Agents, Agent{
		ID: id, Role: "implementation", Capabilities: []string{},
		Status: AgentAvailable, RegisteredAt: formatTime(now),
	})
	return &s.Agents.Agents[len(s.Agents.Agents)-1], "register"
}

func (s *State) findRegistered(r Registration) *Agent {
	if r.ID != "" {
		return s.Agent(r.ID)
	}
	if r.SessionID == "" {
		return nil
	}
	for i := range s.Agents.Agents {
		if s.Agents.Agents[i].SessionID == r.SessionID {
			return &s.Agents.Agents[i]
		}
	}
	return nil
}

func (s *State) nextAgentID() string {
	highest := 0
	for _, a := range s.Agents.Agents {
		n, err := strconv.Atoi(strings.TrimPrefix(a.ID, "agent-"))
		if err == nil && n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("agent-%d", highest+1)
}

// apply copies the fields the registration sets onto a.
func (r Registration) apply(a *Agent) {
	a.Name = r.Name
	setIfGiven(&a.Role, r.Role)
	setIfGiven(&a.SessionID, r.SessionID)
	setIfGiven(&a.Version, r.Version)
	if r.Worktree != "" {
		a.Worktree = strPtr(r.Worktree)
	}
	if r.Capabilities != nil {
		a.Capabilities = r.Capabilities
	}
}

func setIfGiven(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
