// Package integrationmonitor watches integration availability (today: Velocity
// proxy bridges) and turns outages and recoveries into operator notification
// events, per protocol/adapters/integrations.md section 7.
package integrationmonitor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Result is the outcome of one availability probe.
type Result string

const (
	Up               Result = "up"
	Unreachable      Result = "unreachable"
	IdentityMismatch Result = "identity_mismatch"
)

// Event types, as in the notification-event schema.
const (
	EventUnreachable      = "integration.proxy.unreachable"
	EventIdentityMismatch = "integration.proxy.identity_mismatch"
	EventRecovered        = "integration.proxy.recovered"
)

// Policy sets probe spacing and the outage threshold.
type Policy struct {
	// CheckInterval spaces probes while the integration is up.
	CheckInterval time.Duration
	// FailureThreshold is how many consecutive failures declare an outage.
	FailureThreshold int
	// InitialBackoff is the delay after the first failure; it doubles per
	// further failure up to MaxBackoff.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// DefaultPolicy is used when the configuration sets no values.
var DefaultPolicy = Policy{CheckInterval: time.Minute, FailureThreshold: 2, InitialBackoff: 30 * time.Second, MaxBackoff: 5 * time.Minute}

// Validate reports a policy the monitor cannot run.
func (p Policy) Validate() error {
	switch {
	case p.CheckInterval < time.Second || p.CheckInterval > time.Hour:
		return errors.New("check_interval must be between 1s and 1h")
	case p.FailureThreshold < 1 || p.FailureThreshold > 100:
		return errors.New("failure_threshold must be between 1 and 100")
	case p.InitialBackoff < time.Second || p.InitialBackoff > time.Hour:
		return errors.New("initial_backoff must be between 1s and 1h")
	case p.MaxBackoff < p.InitialBackoff || p.MaxBackoff > 24*time.Hour:
		return errors.New("max_backoff must be at least initial_backoff and at most 24h")
	}
	return nil
}

// Subject identifies the monitored proxy instance.
type Subject struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// Event is one notification-event. It holds only these fields; nothing about
// players and no raw error text.
type Event struct {
	EventID             string  `json:"event_id"`
	EventType           string  `json:"event_type"`
	Severity            string  `json:"severity"`
	OccurredAt          string  `json:"occurred_at"`
	EnvironmentID       string  `json:"environment_id"`
	AdapterID           string  `json:"adapter_id"`
	Subject             Subject `json:"subject"`
	OutageStartedAt     string  `json:"outage_started_at"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	Summary             string  `json:"summary"`
}

// Tracker is the outage state machine for one integration. It is not safe for
// concurrent use.
type Tracker struct {
	policy   Policy
	subject  Subject
	failures int
	start    time.Time
	declared Result // "" when no outage is declared
}

// NewTracker returns a tracker in the "no outage" state.
func NewTracker(policy Policy, subject Subject) *Tracker {
	return &Tracker{policy: policy, subject: subject}
}

// Observe records one probe and returns the events to send and the delay
// before the next probe.
//
//   - A failure starts or extends a streak. The streak becomes an outage at
//     FailureThreshold consecutive failures, which emits one event for its
//     reason; a later change of reason during the outage emits another.
//   - While failing, the next probe waits InitialBackoff·2^(n-1), capped at
//     MaxBackoff, where n is the streak length.
//   - A success ends the streak. It emits a recovery event only if an outage
//     was declared; a shorter streak ends silently, which suppresses flaps.
func (t *Tracker) Observe(result Result, at time.Time) ([]Event, time.Duration) {
	if result == Up {
		var events []Event
		if t.declared != "" {
			events = append(events, t.event(EventRecovered, at))
		}
		t.failures, t.declared, t.start = 0, "", time.Time{}
		return events, t.policy.CheckInterval
	}
	if t.failures == 0 {
		t.start = at
	}
	if t.failures < 1_000_000 {
		t.failures++
	}
	var events []Event
	if (t.declared == "" && t.failures >= t.policy.FailureThreshold) || (t.declared != "" && t.declared != result) {
		t.declared = result
		events = append(events, t.event(eventFor(result), at))
	}
	return events, Backoff(t.policy, t.failures)
}

// Backoff is the delay after the n-th consecutive failure.
func Backoff(policy Policy, failures int) time.Duration {
	delay := policy.InitialBackoff
	for i := 1; i < failures && delay < policy.MaxBackoff; i++ {
		delay *= 2
	}
	return min(delay, policy.MaxBackoff)
}

func eventFor(result Result) string {
	if result == IdentityMismatch {
		return EventIdentityMismatch
	}
	return EventUnreachable
}

// Timestamp formats a time as the protocol's UTC RFC 3339 form.
func Timestamp(at time.Time) string { return at.UTC().Format(time.RFC3339Nano) }

// EventID is stable for one event: the adapter, type, outage start, and the
// time the event occurred. Delivery retries reuse the same event.
func EventID(adapterID, eventType, outageStartedAt, occurredAt string) string {
	sum := sha256.Sum256([]byte(adapterID + "\x00" + eventType + "\x00" + outageStartedAt + "\x00" + occurredAt))
	return "evt-" + hex.EncodeToString(sum[:16])
}

func (t *Tracker) event(eventType string, at time.Time) Event {
	occurred, started := Timestamp(at), Timestamp(t.start)
	severity := "critical"
	if eventType == EventRecovered {
		severity = "info"
	}
	return Event{
		EventID: EventID(t.subject.AdapterID, eventType, started, occurred), EventType: eventType, Severity: severity,
		OccurredAt: occurred, EnvironmentID: t.subject.EnvironmentID, AdapterID: t.subject.AdapterID, Subject: t.subject,
		OutageStartedAt: started, ConsecutiveFailures: max(t.failures, 1), Summary: Summary(eventType, t.subject, started),
	}
}

// Summary renders the fixed operator message for an event. It names only the
// adapter, proxy instance, environment, and outage start, and is cut to the
// schema's 280 characters.
func Summary(eventType string, subject Subject, outageStartedAt string) string {
	who := fmt.Sprintf("Proxy %s (%s, %s)", subject.AdapterID, subject.ExternalID, subject.EnvironmentID)
	var text string
	switch eventType {
	case EventIdentityMismatch:
		text = who + " answers with the wrong identity since " + outageStartedAt + "; proxy calls are refused."
	case EventRecovered:
		text = who + " is reachable again after an outage that started " + outageStartedAt + "."
	default:
		text = who + " is unreachable since " + outageStartedAt + "."
	}
	if utf8.RuneCountInString(text) <= 280 {
		return text
	}
	runes := []rune(text)
	return string(runes[:279]) + "…"
}
