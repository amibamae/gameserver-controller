package coordination

import (
	"fmt"
	"sort"
	"time"
)

// Finding levels. Errors are broken state; warnings need attention, such
// as an expired claim that reconcile would release.
const (
	LevelError   = "error"
	LevelWarning = "warning"
)

// Finding is one problem Validate found.
type Finding struct {
	Level   string
	Subject string
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.Level, f.Subject, f.Message)
}

var (
	taskStatuses  = map[string]bool{StatusAvailable: true, StatusClaimed: true, StatusBlocked: true, StatusCompleted: true}
	agentStatuses = map[string]bool{AgentIdle: true, AgentAvailable: true, AgentActive: true}
)

type validator struct {
	st       *State
	now      time.Time
	findings []Finding
}

// Validate checks references and timestamps across the state files: task
// dependencies, .deps.json requiredBy entries, task owners, agents' current
// tasks and reservations. Load already checked versions and revisions.
func Validate(st *State, now time.Time) []Finding {
	v := &validator{st: st, now: now}
	v.duplicates()
	for i := range st.Registry.Tasks {
		v.task(&st.Registry.Tasks[i])
	}
	for i := range st.Agents.Agents {
		v.agent(&st.Agents.Agents[i])
	}
	v.reservations()
	v.dependencyGraph()
	return v.findings
}

func (v *validator) add(level, subject, format string, args ...any) {
	v.findings = append(v.findings, Finding{level, subject, fmt.Sprintf(format, args...)})
}

func (v *validator) duplicates() {
	ids := make([]string, 0, len(v.st.Registry.Tasks))
	for _, t := range v.st.Registry.Tasks {
		ids = append(ids, t.ID)
	}
	v.unique(RegistryFile, "task", ids)
	ids = ids[:0]
	for _, a := range v.st.Agents.Agents {
		ids = append(ids, a.ID)
	}
	v.unique(AgentsFile, "agent", ids)
	ids = ids[:0]
	for _, d := range v.st.Deps.Dependencies {
		ids = append(ids, d.ID)
	}
	v.unique(DepsFile, "dependency", ids)
}

func (v *validator) unique(file, kind string, ids []string) {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			v.add(LevelError, file, "a %s has an empty id", kind)
			continue
		}
		if seen[id] {
			v.add(LevelError, id, "duplicate %s id in %s", kind, file)
		}
		seen[id] = true
	}
}

func (v *validator) task(t *Task) {
	if !taskStatuses[t.Status] {
		v.add(LevelError, t.ID, "unknown status %q", t.Status)
	}
	for _, dep := range t.Dependencies {
		v.taskDependency(t, dep)
	}
	v.knownAgent(t.ID, "assignedTo", deref(t.AssignedTo))
	v.knownAgent(t.ID, "handoffFrom", t.HandoffFrom)
	for _, s := range []struct{ field, value string }{
		{"claimedAt", deref(t.ClaimedAt)}, {"expiresAt", deref(t.ExpiresAt)},
		{"progressAt", t.ProgressAt}, {"completedAt", t.CompletedAt},
	} {
		v.stamp(t.ID, s.field, s.value)
	}
	if isHeld(t) {
		v.heldTask(t)
	}
}

func (v *validator) taskDependency(t *Task, dep string) {
	switch {
	case dep == t.ID:
		v.add(LevelError, t.ID, "depends on itself")
	case v.st.Task(dep) == nil:
		v.add(LevelError, t.ID, "dependency %s is not defined in %s", dep, RegistryFile)
	}
}

func (v *validator) knownAgent(subject, field, id string) {
	if id != "" && v.st.Agent(id) == nil {
		v.add(LevelError, subject, "%s %s is not a registered agent", field, id)
	}
}

func (v *validator) stamp(subject, field, value string) {
	if value == "" {
		return
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		v.add(LevelError, subject, "%s %q is not an RFC 3339 time", field, value)
	}
}

// heldTask checks a claimed or blocked task's owner and expiry.
func (v *validator) heldTask(t *Task) {
	owner := deref(t.AssignedTo)
	if owner == "" {
		v.add(LevelError, t.ID, "is %s but has no assignedTo", t.Status)
		return
	}
	if t.ExpiresAt == nil {
		v.add(LevelError, t.ID, "is %s but has no expiresAt", t.Status)
		return
	}
	if expired, err := ClaimExpired(t, v.now); err == nil && expired {
		v.add(LevelWarning, t.ID, "claim by %s expired at %s; run reconcile", owner, *t.ExpiresAt)
	}
	if a := v.st.Agent(owner); a != nil && deref(a.CurrentTask) != t.ID {
		v.add(LevelWarning, t.ID, "claimed by %s, but that agent's currentTask is %q", owner, deref(a.CurrentTask))
	}
}

func (v *validator) agent(a *Agent) {
	if !agentStatuses[a.Status] {
		v.add(LevelError, a.ID, "unknown status %q", a.Status)
	}
	v.knownAgent(a.ID, "handoffFrom", a.HandoffFrom)
	v.stamp(a.ID, "lastHeartbeat", deref(a.LastHeartbeat))
	cur := deref(a.CurrentTask)
	if cur == "" {
		return
	}
	t := v.st.Task(cur)
	if t == nil {
		v.add(LevelError, a.ID, "currentTask %s is not defined in %s", cur, RegistryFile)
		return
	}
	if !isHeld(t) || deref(t.AssignedTo) != a.ID {
		v.add(LevelWarning, a.ID, "currentTask %s is not claimed by this agent", cur)
	}
}

func (v *validator) reservations() {
	keys := make([]string, 0, len(v.st.Files.CurrentOwners))
	for key := range v.st.Files.CurrentOwners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v.reservation(key, v.st.Files.CurrentOwners[key])
	}
}

func (v *validator) reservation(key string, r Reservation) {
	v.knownAgent(key, "reservation agent", r.Agent)
	t := v.st.Task(r.Task)
	if t == nil {
		v.add(LevelError, key, "reserved for %s, which is not defined in %s", r.Task, RegistryFile)
	} else if !isHeld(t) || deref(t.AssignedTo) != r.Agent {
		v.add(LevelWarning, key, "reserved by %s for %s, which that agent no longer holds; run reconcile", r.Agent, r.Task)
	}
	v.stamp(key, "reservedAt", r.ReservedAt)
	expired, err := stampExpired("reservation "+key, r.ExpiresAt, v.now)
	if err != nil {
		v.add(LevelError, key, "expiresAt %q is not an RFC 3339 time", r.ExpiresAt)
		return
	}
	if expired {
		v.add(LevelWarning, key, "reservation by %s expired at %s; run reconcile", r.Agent, r.ExpiresAt)
	}
}

func (v *validator) dependencyGraph() {
	for _, d := range v.st.Deps.Dependencies {
		for _, task := range d.RequiredBy {
			if v.st.Task(task) == nil {
				v.add(LevelError, d.ID, "requiredBy %s is not defined in %s", task, RegistryFile)
			}
		}
	}
}
