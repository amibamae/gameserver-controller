package simulation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
)

// FaultKind is the kind of failure a fault plan injects.
type FaultKind int

// The fault kinds a plan can inject.
const (
	FaultTimeout FaultKind = iota + 1
	FaultNetwork
	FaultAPI
)

// Sentinel errors that every injected fault of the matching kind wraps.
// ErrTimeout also wraps context.DeadlineExceeded, so code that checks for a
// deadline sees an injected timeout as one.
var (
	ErrTimeout = fmt.Errorf("simulation: injected timeout: %w", context.DeadlineExceeded)
	ErrNetwork = errors.New("simulation: injected network error")
	ErrAPI     = errors.New("simulation: injected API error")
)

// Fault describes one injected failure.
type Fault struct {
	Kind FaultKind
	// StatusCode is the HTTP-style status of an API fault, from 400 to 599.
	StatusCode int
	Message    string
}

// Timeout returns a timeout fault.
func Timeout() Fault { return Fault{Kind: FaultTimeout} }

// NetworkError returns a network fault with the given message.
func NetworkError(message string) Fault { return Fault{Kind: FaultNetwork, Message: message} }

// APIError returns an API fault with the given status code and message.
func APIError(statusCode int, message string) Fault {
	return Fault{Kind: FaultAPI, StatusCode: statusCode, Message: message}
}

func (f Fault) validate() error {
	switch f.Kind {
	case FaultTimeout, FaultNetwork:
		return nil
	case FaultAPI:
		if f.StatusCode < 400 || f.StatusCode > 599 {
			return fmt.Errorf("simulation: API fault status %d is outside 400-599", f.StatusCode)
		}
		return nil
	default:
		return fmt.Errorf("simulation: unknown fault kind %d", f.Kind)
	}
}

// FaultError is the error a fault plan returns for a scheduled attempt.
type FaultError struct {
	Call    string
	Attempt int
	Fault   Fault
}

func (e *FaultError) Error() string {
	prefix := fmt.Sprintf("simulation: %s attempt %d: ", e.Call, e.Attempt)
	switch e.Fault.Kind {
	case FaultTimeout:
		return prefix + "injected timeout"
	case FaultNetwork:
		return prefix + "injected network error: " + e.Fault.Message
	default:
		return fmt.Sprintf("%sinjected API error %d: %s", prefix, e.Fault.StatusCode, e.Fault.Message)
	}
}

// Unwrap returns the sentinel for the fault's kind.
func (e *FaultError) Unwrap() error {
	switch e.Fault.Kind {
	case FaultTimeout:
		return ErrTimeout
	case FaultNetwork:
		return ErrNetwork
	default:
		return ErrAPI
	}
}

// CallAttempt names one attempt of a named call. Attempts count from 1.
type CallAttempt struct {
	Call    string
	Attempt int
}

func (a CallAttempt) String() string {
	return fmt.Sprintf("%s#%d", a.Call, a.Attempt)
}

// FaultPlan schedules faults by call name and attempt number. A simulated
// backend calls Next at the start of each call; the plan counts the attempt
// and returns the fault scheduled for it, if any. A FaultPlan is not safe for
// concurrent use.
type FaultPlan struct {
	faults   map[CallAttempt]Fault
	attempts map[string]int
}

// NewFaultPlan returns an empty plan, under which every call succeeds.
func NewFaultPlan() *FaultPlan {
	return &FaultPlan{faults: map[CallAttempt]Fault{}, attempts: map[string]int{}}
}

// Schedule makes the given attempt of call fail with fault.
func (p *FaultPlan) Schedule(call string, attempt int, fault Fault) error {
	if call == "" {
		return errors.New("simulation: fault call name is empty")
	}
	if attempt < 1 {
		return fmt.Errorf("simulation: fault for %q attempt must be at least 1, got %d", call, attempt)
	}
	if err := fault.validate(); err != nil {
		return err
	}
	key := CallAttempt{Call: call, Attempt: attempt}
	if _, ok := p.faults[key]; ok {
		return fmt.Errorf("simulation: fault for %s is already scheduled", key)
	}
	p.faults[key] = fault
	return nil
}

// Next records one more attempt of call and returns the fault scheduled for
// that attempt as a *FaultError, or nil when none is scheduled. Each
// scheduled fault fires once.
func (p *FaultPlan) Next(call string) error {
	p.attempts[call]++
	key := CallAttempt{Call: call, Attempt: p.attempts[call]}
	fault, ok := p.faults[key]
	if !ok {
		return nil
	}
	delete(p.faults, key)
	return &FaultError{Call: key.Call, Attempt: key.Attempt, Fault: fault}
}

// Attempts reports how many times Next has been called for call.
func (p *FaultPlan) Attempts(call string) int {
	return p.attempts[call]
}

// Pending lists the scheduled faults that have not fired yet, sorted by call
// name and then attempt, so a test can assert the whole plan was exercised.
func (p *FaultPlan) Pending() []CallAttempt {
	pending := make([]CallAttempt, 0, len(p.faults))
	for key := range p.faults {
		pending = append(pending, key)
	}
	slices.SortFunc(pending, func(a, b CallAttempt) int {
		return cmp.Or(cmp.Compare(a.Call, b.Call), cmp.Compare(a.Attempt, b.Attempt))
	})
	return pending
}
