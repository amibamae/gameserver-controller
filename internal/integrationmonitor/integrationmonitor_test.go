package integrationmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

func readVector(t *testing.T, name string, into any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, into); err != nil {
		t.Fatal(err)
	}
}

type vectorPolicy struct {
	CheckInterval    int `json:"check_interval_seconds"`
	FailureThreshold int `json:"failure_threshold"`
	InitialBackoff   int `json:"initial_backoff_seconds"`
	MaxBackoff       int `json:"max_backoff_seconds"`
}

func (p vectorPolicy) policy() Policy {
	return Policy{CheckInterval: time.Duration(p.CheckInterval) * time.Second, FailureThreshold: p.FailureThreshold,
		InitialBackoff: time.Duration(p.InitialBackoff) * time.Second, MaxBackoff: time.Duration(p.MaxBackoff) * time.Second}
}

// TestTrackerMatchesTheOutageVector runs the published section 7 vector
// through the controller's tracker.
func TestTrackerMatchesTheOutageVector(t *testing.T) {
	var vector struct {
		Input struct {
			Subject       Subject      `json:"subject"`
			DefaultPolicy vectorPolicy `json:"default_policy"`
			Evaluations   []struct {
				CaseID string        `json:"case_id"`
				Policy *vectorPolicy `json:"policy"`
				Probes []struct {
					ObservedAt time.Time `json:"observed_at"`
					Result     Result    `json:"result"`
				} `json:"probes"`
			} `json:"evaluations"`
		} `json:"input"`
		Expected struct {
			Evaluations []struct {
				CaseID string `json:"case_id"`
				Events []struct {
					EventID             string `json:"event_id"`
					EventType           string `json:"event_type"`
					Severity            string `json:"severity"`
					OccurredAt          string `json:"occurred_at"`
					OutageStartedAt     string `json:"outage_started_at"`
					ConsecutiveFailures int    `json:"consecutive_failures"`
				} `json:"events"`
				Delays []int `json:"next_probe_delays_seconds"`
			} `json:"evaluations"`
		} `json:"expected"`
	}
	readVector(t, "notification-proxy-outage-transitions.json", &vector)
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i, evaluation := range vector.Input.Evaluations {
		t.Run(evaluation.CaseID, func(t *testing.T) {
			policy := vector.Input.DefaultPolicy
			if evaluation.Policy != nil {
				policy = *evaluation.Policy
			}
			tracker := NewTracker(policy.policy(), vector.Input.Subject)
			var events []Event
			var delays []int
			for _, probe := range evaluation.Probes {
				emitted, delay := tracker.Observe(probe.Result, probe.ObservedAt)
				events = append(events, emitted...)
				delays = append(delays, int(delay/time.Second))
			}
			want := vector.Expected.Evaluations[i]
			if !slices.Equal(delays, want.Delays) {
				t.Errorf("delays = %v, want %v", delays, want.Delays)
			}
			if len(events) != len(want.Events) {
				t.Fatalf("events = %+v, want %+v", events, want.Events)
			}
			for j, event := range events {
				w := want.Events[j]
				if event.EventID != w.EventID || event.EventType != w.EventType || event.Severity != w.Severity || event.OccurredAt != w.OccurredAt ||
					event.OutageStartedAt != w.OutageStartedAt || event.ConsecutiveFailures != w.ConsecutiveFailures {
					t.Errorf("event %d = %+v, want %+v", j, event, w)
				}
				encoded, _ := json.Marshal(event)
				if err := schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":integrations:notification-event", encoded); err != nil {
					t.Errorf("event %d is not schema-valid: %v", j, err)
				}
			}
		})
	}
}

func TestRetryRulesMatchTheSendVector(t *testing.T) {
	var vector struct {
		Input struct {
			Evaluations []struct {
				Attempt int `json:"attempt"`
			} `json:"evaluations"`
		} `json:"input"`
		Expected struct {
			Evaluations []struct {
				CaseID    string `json:"case_id"`
				Effective string `json:"effective_outcome"`
				Retry     bool   `json:"retry_permitted"`
				Delay     int    `json:"retry_delay_seconds"`
			} `json:"evaluations"`
		} `json:"expected"`
	}
	readVector(t, "notification-send-outcome-and-privacy.json", &vector)
	for i, want := range vector.Expected.Evaluations {
		if want.Effective == "" {
			continue // not sent
		}
		attempt := vector.Input.Evaluations[i].Attempt
		if got := RetryPermitted(want.Effective, attempt); got != want.Retry {
			t.Errorf("%s: RetryPermitted = %v, want %v", want.CaseID, got, want.Retry)
		}
		if want.Retry && RetryDelay(attempt) != time.Duration(want.Delay)*time.Second {
			t.Errorf("%s: RetryDelay = %v, want %ds", want.CaseID, RetryDelay(attempt), want.Delay)
		}
	}
}

func TestSummaryIsCutToTheSchemaLimit(t *testing.T) {
	long := Subject{AdapterID: "integration.velocity", EnvironmentID: "production", ExternalID: string(make([]byte, 400))}
	if got := []rune(Summary(EventUnreachable, long, "2026-11-12T18:00:00Z")); len(got) != 280 {
		t.Fatalf("summary length = %d", len(got))
	}
}

// fakeInvoker records sends and answers with scripted outcomes.
type fakeInvoker struct {
	mu       sync.Mutex
	outcomes map[string][]string // adapter -> outcomes in order; "error" fails the call
	sent     map[string][]string // adapter -> event IDs
}

func (f *fakeInvoker) ProtocolVersion() string { return "0.21.0-draft" }

func (f *fakeInvoker) Invoke(_ context.Context, adapterID string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	var input struct {
		Event Event `json:"event"`
	}
	_ = json.Unmarshal(request.Params.Input, &input)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent[adapterID] = append(f.sent[adapterID], input.Event.EventID)
	outcome := Delivered
	if queue := f.outcomes[adapterID]; len(queue) > 0 {
		outcome, f.outcomes[adapterID] = queue[0], queue[1:]
	}
	if outcome == "error" {
		return adapter.InvokeResponse{}, &adapter.InvocationError{Cause: errors.New("timeout")}
	}
	if len(request.Params.RequestContext.SecretRefs) != 1 || request.Params.RequestContext.EnvironmentID != input.Event.EnvironmentID {
		return adapter.InvokeResponse{}, errors.New("missing secret ref or wrong environment")
	}
	output, _ := json.Marshal(map[string]any{"event_id": input.Event.EventID, "outcome": outcome, "observed_at": time.Now().UTC()})
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: time.Now()}}, nil
}

func (f *fakeInvoker) sentTo(adapterID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent[adapterID]...)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDispatcherRoutesRetriesAndLogs(t *testing.T) {
	invoker := &fakeInvoker{sent: map[string][]string{}, outcomes: map[string][]string{
		"notify.retry":    {"error", Unknown, Delivered},
		"notify.rejected": {Rejected},
	}}
	ref := []adapter.SecretReference{{SecretRef: "systemd-credential://x", Purpose: "test"}}
	destinations := []Destination{
		{AdapterID: "notify.retry", EnvironmentID: "production", SecretRefs: ref},
		{AdapterID: "notify.rejected", EnvironmentID: "production", SecretRefs: ref},
		{AdapterID: "notify.recovery-only", EnvironmentID: "production", Events: []string{EventRecovered}, SecretRefs: ref},
		{AdapterID: "notify.staging", EnvironmentID: "staging", SecretRefs: ref},
	}
	var logMu sync.Mutex
	var logged []string
	dispatcher := NewDispatcher(invoker, destinations, func(message string, fields map[string]any) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, message)
		for _, value := range fields {
			if text, ok := value.(string); ok && text == "systemd-credential://x" {
				t.Errorf("log line contains a secret reference")
			}
		}
	})
	var slept []time.Duration
	var sleepMu sync.Mutex
	dispatcher.sleep = func(_ context.Context, delay time.Duration) bool {
		sleepMu.Lock()
		slept = append(slept, delay)
		sleepMu.Unlock()
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher.Start(ctx)
	tracker := NewTracker(Policy{CheckInterval: time.Minute, FailureThreshold: 1, InitialBackoff: time.Second, MaxBackoff: time.Second},
		Subject{AdapterID: "integration.velocity", EnvironmentID: "production", ResourceKind: "proxy-instance", ExternalID: "edge"})
	events, _ := tracker.Observe(Unreachable, time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC))
	dispatcher.Publish(events[0])
	waitFor(t, func() bool {
		return len(invoker.sentTo("notify.retry")) == 3 && len(invoker.sentTo("notify.rejected")) == 1
	})
	cancel()
	dispatcher.Wait()

	if got := invoker.sentTo("notify.retry"); got[0] != events[0].EventID || got[2] != events[0].EventID {
		t.Errorf("retries must reuse the event ID: %v", got)
	}
	if len(invoker.sentTo("notify.recovery-only")) != 0 || len(invoker.sentTo("notify.staging")) != 0 {
		t.Error("an event went to a destination that filters it out or is in another environment")
	}
	if !slices.Equal(slept, []time.Duration{30 * time.Second, time.Minute}) {
		t.Errorf("retry delays = %v", slept)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !slices.Contains(logged, "integration event") || !slices.Contains(logged, "notification delivered") || !slices.Contains(logged, "notification not delivered") {
		t.Errorf("log lines = %v", logged)
	}
}

func TestMonitorProbesOnScheduleAndStopsCleanly(t *testing.T) {
	results := []Result{Unreachable, Unreachable, Up}
	var mu sync.Mutex
	var published []Event
	var delays []time.Duration
	probe := func(context.Context) Result {
		mu.Lock()
		defer mu.Unlock()
		if len(results) == 0 {
			return Up
		}
		next := results[0]
		results = results[1:]
		return next
	}
	monitor := New(Policy{CheckInterval: time.Minute, FailureThreshold: 2, InitialBackoff: 30 * time.Second, MaxBackoff: 5 * time.Minute},
		[]Target{{Subject: Subject{AdapterID: "integration.velocity", EnvironmentID: "production", ResourceKind: "proxy-instance", ExternalID: "edge"},
			Probe: probe, Initial: Unreachable}},
		func(event Event) {
			mu.Lock()
			published = append(published, event)
			mu.Unlock()
		})
	ctx, cancel := context.WithCancel(context.Background())
	monitor.sleep = func(ctx context.Context, delay time.Duration) bool {
		mu.Lock()
		delays = append(delays, delay)
		done := len(delays) > 4
		mu.Unlock()
		if done {
			cancel()
			return false
		}
		return ctx.Err() == nil
	}
	monitor.Start(ctx)
	monitor.Wait()
	mu.Lock()
	defer mu.Unlock()
	// Startup failure, then two more failures, then recovery.
	if len(published) != 2 || published[0].EventType != EventUnreachable || published[1].EventType != EventRecovered {
		t.Fatalf("published = %+v", published)
	}
	if !slices.Equal(delays[:4], []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, time.Minute}) {
		t.Errorf("delays = %v", delays)
	}
}

func TestPolicyValidation(t *testing.T) {
	if err := DefaultPolicy.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := DefaultPolicy
	bad.MaxBackoff = time.Second
	if bad.Validate() == nil {
		t.Fatal("max_backoff below initial_backoff was accepted")
	}
}
