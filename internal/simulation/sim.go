// Package simulation is the core of the deterministic simulation harness:
// a virtual clock, a fault-injection plan, a safety-invariant checker and
// seeded randomness. Per-backend simulators build on it; it never touches the
// network, the wall clock or the global random source.
package simulation

import (
	"math/rand/v2"
	"time"
)

// randStream is the fixed second PCG word, so the seed alone picks the run.
const randStream = 0x9e3779b97f4a7c15

// Config sets up a simulation.
type Config struct {
	// Seed selects the random sequence; the same seed gives the same run.
	Seed uint64
	// Start is the virtual time the clock starts at.
	Start time.Time
}

// Sim ties a clock, a fault plan, invariants and a seeded random source into
// one run. Invariants are checked after every Step, after every timer that
// fires and at the end of every Advance. A Sim is not safe for concurrent use.
type Sim struct {
	clock      *Clock
	faults     *FaultPlan
	invariants Invariants
	rand       *rand.Rand
}

// NewRand returns a random source fully determined by seed.
func NewRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, randStream))
}

// New returns a simulation configured by cfg.
func New(cfg Config) *Sim {
	s := &Sim{clock: NewClock(cfg.Start), faults: NewFaultPlan(), rand: NewRand(cfg.Seed)}
	s.clock.onStep = s.invariants.Check
	return s
}

// Clock returns the simulation's virtual clock.
func (s *Sim) Clock() *Clock { return s.clock }

// Faults returns the simulation's fault plan.
func (s *Sim) Faults() *FaultPlan { return s.faults }

// Rand returns the simulation's seeded random source.
func (s *Sim) Rand() *rand.Rand { return s.rand }

// Invariant registers a named safety invariant.
func (s *Sim) Invariant(name string, check func() error) error {
	return s.invariants.Register(name, check)
}

// Advance moves the virtual clock forward by d; see Clock.Advance.
func (s *Sim) Advance(d time.Duration) error {
	return s.clock.Advance(d)
}

// Step runs one named simulated action and then checks every invariant. An
// invariant failure takes precedence over the action's own error, which is
// returned unchanged otherwise so callers can match injected faults.
func (s *Sim) Step(name string, action func() error) error {
	actionErr := action()
	if err := s.invariants.Check(name); err != nil {
		return err
	}
	return actionErr
}
