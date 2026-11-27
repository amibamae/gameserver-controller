// Package idlescheduler applies the billing-window scale-down policy in
// internal/scalingpolicy to the controller's managed servers.
//
// On every tick the scheduler reads each managed server's creation time and
// idle evidence from an injected Inventory, asks
// scalingpolicy.Policy.ScaleDown whether the server may be shut down now,
// and, when it may, asks an injected ShutdownRequester to run the
// stop → verified backup → destroy sequence. The scheduler never stops,
// backs up or destroys a server itself.
//
// It fails closed: an unknown creation time, missing idle evidence, an
// invalid policy or a failed request all mean no shutdown, and the tick's
// Report records the reason. At most one shutdown request per server is in
// flight; repeated ticks report it and request nothing more until Release.
package idlescheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/scalingpolicy"
)

// Clock supplies the current time. The runtime passes the wall clock;
// tests and simulations pass their own.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function, such as time.Now, to Clock.
type ClockFunc func() time.Time

// Now returns f().
func (f ClockFunc) Now() time.Time { return f() }

// IdleEvidence is what the game or proxy adapter last reported about a
// server's players.
type IdleEvidence struct {
	// Observed is false when no trustworthy player count is available.
	Observed bool
	// IdleSince is when the server last became empty. The zero time means
	// it has players.
	IdleSince time.Time
}

// Server is one managed server as the inventory reports it.
type Server struct {
	ID string
	// CreatedAt is when the provider created the instance; the zero time
	// means it is unknown.
	CreatedAt time.Time
	Idle      IdleEvidence
	// IdleShutdown is lifecycle.idle_shutdown, the minimum idle time.
	IdleShutdown time.Duration
	// Scaling is lifecycle.scaling; Resolve validates it on every tick.
	Scaling scalingpolicy.Settings
}

// Inventory lists the servers the scheduler manages.
type Inventory interface {
	ManagedServers(ctx context.Context) ([]Server, error)
}

// Step is one step of the shutdown sequence a ShutdownRequest asks for.
type Step string

// The shutdown steps, in the only order the scheduler requests them.
const (
	StepStop           Step = "stop"
	StepVerifiedBackup Step = "verified_backup"
	StepDestroy        Step = "destroy"
)

// ShutdownSequence returns the steps every ShutdownRequest carries: stop
// the server, take a verified backup, then destroy it. The workflow that
// runs it must not destroy unless the backup step verified.
func ShutdownSequence() []Step {
	return []Step{StepStop, StepVerifiedBackup, StepDestroy}
}

// ShutdownRequest asks the lifecycle workflows to shut one server down.
type ShutdownRequest struct {
	ServerID string
	// IdempotencyKey is the same for every request made during one idle
	// period of one instance, so a retry after an unclear failure can be
	// reconciled instead of starting a second shutdown.
	IdempotencyKey string
	RequestedAt    time.Time
	Steps          []Step
	// Reason is the scaling decision that allowed the shutdown.
	Reason string
}

// ShutdownRequester starts the shutdown sequence. A nil error means the
// request was accepted; it does not mean the server is gone.
type ShutdownRequester interface {
	RequestShutdown(ctx context.Context, request ShutdownRequest) error
}

// Outcome is what one tick did for one server.
type Outcome struct {
	ServerID          string
	ShutdownRequested bool
	// Reason is a Reason* constant or a scalingpolicy decision reason.
	Reason string
	// NotBefore is when the policy next allows a shutdown, when known.
	NotBefore time.Time
	Err       error
}

// Report is the result of one tick.
type Report struct {
	At       time.Time
	Outcomes []Outcome
	// Err is set when the inventory could not be read; no server was
	// evaluated.
	Err error
}

// Scheduler evaluates managed servers for idle shutdown. It is safe for
// concurrent use; ticks run one at a time.
type Scheduler struct {
	clock     Clock
	inventory Inventory
	requester ShutdownRequester

	mu       sync.Mutex
	inFlight map[string]string // server ID -> idempotency key
}

// New returns a Scheduler over the injected dependencies.
func New(clock Clock, inventory Inventory, requester ShutdownRequester) (*Scheduler, error) {
	if clock == nil {
		return nil, errors.New("idle scheduler: clock is required")
	}
	if inventory == nil {
		return nil, errors.New("idle scheduler: inventory is required")
	}
	if requester == nil {
		return nil, errors.New("idle scheduler: shutdown requester is required")
	}
	return &Scheduler{clock: clock, inventory: inventory, requester: requester, inFlight: map[string]string{}}, nil
}

// Tick evaluates every managed server once at the clock's current time.
func (s *Scheduler) Tick(ctx context.Context) Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	servers, err := s.inventory.ManagedServers(ctx)
	if err != nil {
		return Report{At: now, Err: fmt.Errorf("idle scheduler: list managed servers: %w", err)}
	}
	duplicates := duplicateIDs(servers)
	report := Report{At: now, Outcomes: make([]Outcome, 0, len(servers))}
	for _, server := range servers {
		report.Outcomes = append(report.Outcomes, s.evaluate(ctx, server, now, duplicates))
	}
	return report
}

// InFlight reports whether a shutdown request for serverID is outstanding.
func (s *Scheduler) InFlight(serverID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.inFlight[serverID]
	return ok
}

// Release clears the outstanding shutdown request for serverID once the
// workflow that ran it has finished, successfully or not. Until then the
// scheduler requests nothing more for that server.
func (s *Scheduler) Release(serverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, serverID)
}

func (s *Scheduler) evaluate(ctx context.Context, server Server, now time.Time, duplicates map[string]bool) Outcome {
	outcome := Outcome{ServerID: server.ID}
	if server.ID == "" {
		outcome.Reason = ReasonMissingServerID
		return outcome
	}
	if duplicates[server.ID] {
		outcome.Reason = ReasonDuplicateServerID
		return outcome
	}
	if _, busy := s.inFlight[server.ID]; busy {
		outcome.Reason = ReasonShutdownInFlight
		return outcome
	}
	policy, reason, err := admit(server, now)
	if reason != "" {
		outcome.Reason, outcome.Err = reason, err
		return outcome
	}
	decision := policy.ScaleDown(server.CreatedAt, now, server.Idle.IdleSince, server.IdleShutdown)
	if !decision.Allowed {
		outcome.Reason, outcome.NotBefore = decision.Reason, decision.NotBefore
		return outcome
	}
	return s.request(ctx, server, now, decision.Reason)
}

func (s *Scheduler) request(ctx context.Context, server Server, now time.Time, reason string) Outcome {
	request := ShutdownRequest{
		ServerID:       server.ID,
		IdempotencyKey: idempotencyKey(server),
		RequestedAt:    now,
		Steps:          ShutdownSequence(),
		Reason:         reason,
	}
	if err := s.requester.RequestShutdown(ctx, request); err != nil {
		return Outcome{ServerID: server.ID, Reason: ReasonRequestFailed, Err: fmt.Errorf("idle scheduler: request shutdown of %s: %w", server.ID, err)}
	}
	s.inFlight[server.ID] = request.IdempotencyKey
	return Outcome{ServerID: server.ID, ShutdownRequested: true, Reason: reason}
}

// idempotencyKey names one idle period of one instance.
func idempotencyKey(server Server) string {
	return fmt.Sprintf("idle-shutdown:%s:%d:%d", server.ID, server.CreatedAt.UnixNano(), server.Idle.IdleSince.UnixNano())
}

func duplicateIDs(servers []Server) map[string]bool {
	seen := make(map[string]int, len(servers))
	for _, server := range servers {
		seen[server.ID]++
	}
	duplicates := map[string]bool{}
	for id, count := range seen {
		if count > 1 {
			duplicates[id] = true
		}
	}
	return duplicates
}
