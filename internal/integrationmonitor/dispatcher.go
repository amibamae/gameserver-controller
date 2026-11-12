package integrationmonitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// SendCapability is the notification capability every notifier advertises.
const SendCapability = "integration.notification.send"

// Delivery outcomes, as in the notification-send-output schema.
const (
	Delivered = "delivered"
	Rejected  = "rejected"
	Unknown   = "outcome_unknown"
)

// MaxAttempts bounds delivery of one event to one destination.
const MaxAttempts = 5

// queueLimit bounds undelivered events per destination; the oldest is dropped.
const queueLimit = 100

// RetryDelay is the wait before attempt n+1 after attempt n failed with an
// unknown outcome: 30s, 1m, 2m, 4m.
func RetryDelay(attempt int) time.Duration {
	return Backoff(Policy{InitialBackoff: 30 * time.Second, MaxBackoff: time.Hour}, attempt)
}

// RetryPermitted reports whether another attempt may follow attempt n with
// this outcome. Delivered and rejected are final.
func RetryPermitted(outcome string, attempt int) bool {
	return outcome == Unknown && attempt < MaxAttempts
}

// Destination is one configured notifier adapter.
type Destination struct {
	AdapterID     string
	EnvironmentID string
	// Events limits the event types sent here; empty means all.
	Events []string
	// SecretRefs authorize the adapter's credential for this call.
	SecretRefs []adapter.SecretReference
}

func (d Destination) wants(event Event) bool {
	return d.EnvironmentID == event.EnvironmentID && (len(d.Events) == 0 || slices.Contains(d.Events, event.EventType))
}

// Invoker is the subset of adapter.Registry the dispatcher uses.
type Invoker interface {
	Invoke(context.Context, string, adapter.InvokeRequest) (adapter.InvokeResponse, error)
	ProtocolVersion() string
}

// Logger writes one controller log line. It receives only event fields and
// delivery status, never credentials or raw provider errors.
type Logger func(message string, fields map[string]any)

// Dispatcher delivers events to destinations in the event's environment, one
// worker per destination, and writes every event to the controller log.
type Dispatcher struct {
	invoker      Invoker
	destinations []Destination
	log          Logger
	timeout      time.Duration
	sleep        func(context.Context, time.Duration) bool

	mu      sync.Mutex
	queues  map[string][]Event
	wake    map[string]chan struct{}
	started bool
	wg      sync.WaitGroup
}

// NewDispatcher returns a dispatcher. Start must be called before events are
// delivered; events published earlier wait in the queues.
func NewDispatcher(invoker Invoker, destinations []Destination, log Logger) *Dispatcher {
	d := &Dispatcher{invoker: invoker, destinations: destinations, log: log, timeout: 30 * time.Second, sleep: sleepContext,
		queues: map[string][]Event{}, wake: map[string]chan struct{}{}}
	for _, destination := range destinations {
		d.wake[destination.AdapterID] = make(chan struct{}, 1)
	}
	return d
}

// Publish logs the event and queues it for every destination that wants it.
func (d *Dispatcher) Publish(event Event) {
	d.log("integration event", map[string]any{"event_id": event.EventID, "event_type": event.EventType, "severity": event.Severity,
		"adapter_id": event.AdapterID, "environment_id": event.EnvironmentID, "occurred_at": event.OccurredAt, "summary": event.Summary})
	for _, destination := range d.destinations {
		if !destination.wants(event) {
			continue
		}
		d.mu.Lock()
		queue := append(d.queues[destination.AdapterID], event)
		if len(queue) > queueLimit {
			d.log("notification dropped: queue full", map[string]any{"adapter_id": destination.AdapterID, "event_id": queue[0].EventID})
			queue = queue[1:]
		}
		d.queues[destination.AdapterID] = queue
		d.mu.Unlock()
		select {
		case d.wake[destination.AdapterID] <- struct{}{}:
		default:
		}
	}
}

// Start runs one delivery worker per destination until ctx ends.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started {
		return
	}
	d.started = true
	for _, destination := range d.destinations {
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.worker(ctx, destination)
		}()
	}
}

// Wait blocks until every worker has stopped.
func (d *Dispatcher) Wait() { d.wg.Wait() }

func (d *Dispatcher) next(adapterID string) (Event, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	queue := d.queues[adapterID]
	if len(queue) == 0 {
		return Event{}, false
	}
	d.queues[adapterID] = queue[1:]
	return queue[0], true
}

func (d *Dispatcher) worker(ctx context.Context, destination Destination) {
	for {
		event, ok := d.next(destination.AdapterID)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-d.wake[destination.AdapterID]:
				continue
			}
		}
		d.deliver(ctx, destination, event)
		if ctx.Err() != nil {
			return
		}
	}
}

// deliver makes up to MaxAttempts attempts. Only outcome_unknown, or an
// error that leaves the outcome unknown, is retried; the same event_id lets
// the receiver drop a duplicate.
func (d *Dispatcher) deliver(ctx context.Context, destination Destination, event Event) {
	fields := map[string]any{"adapter_id": destination.AdapterID, "event_id": event.EventID}
	for attempt := 1; ; attempt++ {
		outcome := d.attempt(ctx, destination, event)
		fields["attempt"], fields["outcome"] = attempt, outcome
		switch {
		case outcome == Delivered:
			d.log("notification delivered", fields)
			return
		case !RetryPermitted(outcome, attempt):
			d.log("notification not delivered", fields)
			return
		}
		if !d.sleep(ctx, RetryDelay(attempt)) {
			return
		}
	}
}

func (d *Dispatcher) attempt(ctx context.Context, destination Destination, event Event) string {
	input, err := json.Marshal(map[string]any{"event": event})
	if err != nil {
		return Rejected
	}
	requestID := newRequestID()
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: d.invoker.ProtocolVersion(), Capability: SendCapability,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: event.EventID, EnvironmentID: event.EnvironmentID,
			Deadline: time.Now().Add(d.timeout), SecretRefs: destination.SecretRefs},
		Input: input,
	}}
	response, err := d.invoker.Invoke(ctx, destination.AdapterID, request)
	if err != nil {
		var invocation *adapter.InvocationError
		if errors.As(err, &invocation) {
			// The adapter was called; it may have delivered.
			return Unknown
		}
		// Refused before the adapter was called (bad input, unknown adapter).
		return Rejected
	}
	var output struct {
		EventID string `json:"event_id"`
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(response.Result.Output, &output) != nil || output.EventID != event.EventID {
		return Unknown
	}
	return output.Outcome
}

func newRequestID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return "notify-" + hex.EncodeToString(raw[:])
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
