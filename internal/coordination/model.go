// Package coordination keeps the multi-agent coordination state in
// COORDINATION/: the task registry, the agent registry, file reservations
// and the dependency graph, with an append-only audit log.
//
// Every change goes through Store.Apply, which reads a snapshot, applies a
// mutation to it and commits the result only if the state is still at the
// revision the snapshot was read at (optimistic concurrency). The commit
// runs under a lock file and replaces each state file atomically, so two
// agents on one machine cannot both win the same claim.
package coordination

import (
	"encoding/json"
	"time"
)

// StateVersion is the only state file format version this package reads.
const StateVersion = "1.0"

// ClaimTTL is how long a claim or reservation lives without a heartbeat.
const ClaimTTL = 24 * time.Hour

// Task statuses.
const (
	StatusAvailable = "available"
	StatusClaimed   = "claimed"
	StatusBlocked   = "blocked"
	StatusCompleted = "completed"
)

// Agent statuses.
const (
	AgentIdle      = "idle"
	AgentAvailable = "available"
	AgentActive    = "active"
)

// State file names inside the coordination directory.
const (
	RegistryFile = "registry.json"
	AgentsFile   = ".agents.json"
	FilesFile    = ".files.json"
	DepsFile     = ".deps.json"
	AuditFile    = "audit.md"
	LockFile     = ".lock"
)

// Task is one entry of registry.json.
type Task struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Phase              string   `json:"phase"`
	Status             string   `json:"status"`
	Dependencies       []string `json:"dependencies"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	EstimatedScope     string   `json:"estimatedScope"`
	RequiredFiles      []string `json:"requiredFiles"`
	AssignedTo         *string  `json:"assignedTo"`
	ClaimedAt          *string  `json:"claimedAt"`
	ExpiresAt          *string  `json:"expiresAt"`
	HandoffFrom        string   `json:"handoffFrom,omitempty"`
	Notes              string   `json:"notes"`
	Progress           string   `json:"progress,omitempty"`
	ProgressAt         string   `json:"progressAt,omitempty"`
	BlockedReason      string   `json:"blockedReason,omitempty"`
	ReleasedBy         string   `json:"releasedBy,omitempty"`
	CompletedBy        string   `json:"completedBy,omitempty"`
	CompletedAt        string   `json:"completedAt,omitempty"`
	PRNumber           string   `json:"prNumber,omitempty"`
}

// Registry is registry.json.
type Registry struct {
	Version  string `json:"version"`
	Revision int64  `json:"revision"`
	Tasks    []Task `json:"tasks"`
}

// Agent is one entry of .agents.json.
type Agent struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Capabilities  []string `json:"capabilities"`
	Status        string   `json:"status"`
	LastHeartbeat *string  `json:"lastHeartbeat"`
	CurrentTask   *string  `json:"currentTask"`
	Worktree      *string  `json:"worktree"`
	HandoffFrom   string   `json:"handoffFrom,omitempty"`
	SessionID     string   `json:"sessionId,omitempty"`
	Version       string   `json:"version,omitempty"`
	RegisteredAt  string   `json:"registeredAt,omitempty"`
}

// AgentRegistry is .agents.json.
type AgentRegistry struct {
	Version  string  `json:"version"`
	Revision int64   `json:"revision"`
	Agents   []Agent `json:"agents"`
}

// Reservation is one owned file pattern in .files.json currentOwners.
type Reservation struct {
	Agent      string `json:"agent"`
	Task       string `json:"task"`
	ReservedAt string `json:"reservedAt"`
	ExpiresAt  string `json:"expiresAt"`
}

// FileState is .files.json. Reservations holds the named shared areas,
// which the CLI keeps as they are; CurrentOwners holds live reservations
// keyed by normalized file pattern.
type FileState struct {
	Version       string                 `json:"version"`
	Revision      int64                  `json:"revision"`
	Reservations  map[string][]string    `json:"reservations"`
	CurrentOwners map[string]Reservation `json:"currentOwners"`
	PendingClaims []json.RawMessage      `json:"pendingClaims"`
}

// Dependency is one entry of .deps.json.
type Dependency struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	RequiredBy  []string `json:"requiredBy"`
	Description string   `json:"description"`
}

// DependencyGraph is .deps.json. The CLI reads it but never writes it.
type DependencyGraph struct {
	Version      string       `json:"version"`
	Dependencies []Dependency `json:"dependencies"`
}

// State is one consistent snapshot of the coordination directory.
type State struct {
	Registry Registry
	Agents   AgentRegistry
	Files    FileState
	Deps     DependencyGraph
}

// Revision is the state revision the snapshot was read at.
func (s *State) Revision() int64 { return s.Registry.Revision }

func (s *State) setRevision(rev int64) {
	s.Registry.Revision = rev
	s.Agents.Revision = rev
	s.Files.Revision = rev
}

// Event is one state change, written to the audit log.
type Event struct {
	Actor   string
	Action  string
	Subject string
	Detail  string
}

// Outcome is what a mutation did: the audited events and any notes (such
// as dependency warnings) for the caller to show.
type Outcome struct {
	Events []Event
	Notes  []string
}

// Mutation changes a snapshot in place. It returns no events when there is
// nothing to change; Apply then commits nothing.
type Mutation func(st *State, now time.Time) (Outcome, error)

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func strPtr(s string) *string { return &s }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
