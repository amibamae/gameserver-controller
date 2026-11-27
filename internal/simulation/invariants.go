package simulation

import (
	"errors"
	"fmt"
)

// InvariantError reports the first safety invariant that failed and the
// simulated step after which it failed.
type InvariantError struct {
	Invariant string
	Step      string
	Err       error
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("simulation: invariant %q failed after step %q: %v", e.Invariant, e.Step, e.Err)
}

// Unwrap returns the error the invariant's check returned.
func (e *InvariantError) Unwrap() error {
	return e.Err
}

type invariant struct {
	name  string
	check func() error
}

// Invariants is an ordered set of named safety invariants.
type Invariants struct {
	list  []invariant
	names map[string]bool
}

// Register adds a named invariant. Invariants run in registration order.
func (v *Invariants) Register(name string, check func() error) error {
	if name == "" {
		return errors.New("simulation: invariant name is empty")
	}
	if check == nil {
		return fmt.Errorf("simulation: invariant %q has no check", name)
	}
	if v.names[name] {
		return fmt.Errorf("simulation: invariant %q is already registered", name)
	}
	if v.names == nil {
		v.names = map[string]bool{}
	}
	v.names[name] = true
	v.list = append(v.list, invariant{name: name, check: check})
	return nil
}

// Check runs every invariant in registration order and returns an
// *InvariantError for the first one that fails.
func (v *Invariants) Check(step string) error {
	for _, inv := range v.list {
		if err := inv.check(); err != nil {
			return &InvariantError{Invariant: inv.name, Step: step, Err: err}
		}
	}
	return nil
}
