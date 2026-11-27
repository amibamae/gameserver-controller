package idlescheduler

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/scalingpolicy"
)

var created = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

func at(minutes float64) time.Time {
	return created.Add(time.Duration(minutes * float64(time.Minute)))
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type fakeInventory struct {
	servers []Server
	err     error
}

func (i *fakeInventory) ManagedServers(context.Context) ([]Server, error) {
	return i.servers, i.err
}

type fakeRequester struct {
	mu       sync.Mutex
	requests []ShutdownRequest
	err      error
}

func (r *fakeRequester) RequestShutdown(_ context.Context, request ShutdownRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	return r.err
}

func (r *fakeRequester) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// idleServer has been empty since minute 10 with the default 58–59 window.
func idleServer() Server {
	return Server{
		ID:           "srv-1",
		CreatedAt:    created,
		Idle:         IdleEvidence{Observed: true, IdleSince: at(10)},
		IdleShutdown: 30 * time.Minute,
	}
}

type harness struct {
	clock     *fakeClock
	inventory *fakeInventory
	requester *fakeRequester
	scheduler *Scheduler
}

func newHarness(t *testing.T, now time.Time, servers ...Server) *harness {
	t.Helper()
	h := &harness{clock: &fakeClock{now: now}, inventory: &fakeInventory{servers: servers}, requester: &fakeRequester{}}
	scheduler, err := New(h.clock, h.inventory, h.requester)
	if err != nil {
		t.Fatal(err)
	}
	h.scheduler = scheduler
	return h
}

func (h *harness) tickOne(t *testing.T) Outcome {
	t.Helper()
	report := h.scheduler.Tick(context.Background())
	if report.Err != nil {
		t.Fatalf("tick error: %v", report.Err)
	}
	if len(report.Outcomes) != 1 {
		t.Fatalf("outcomes = %+v, want one", report.Outcomes)
	}
	return report.Outcomes[0]
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestInsideWindowRequestsTheShutdownSequence(t *testing.T) {
	h := newHarness(t, at(58.5), idleServer())
	got := h.tickOne(t)
	want := Outcome{ServerID: "srv-1", ShutdownRequested: true, Reason: "in_decision_window"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outcome = %+v, want %+v", got, want)
	}
	wantRequest := ShutdownRequest{
		ServerID:       "srv-1",
		IdempotencyKey: "idle-shutdown:srv-1:1794045600000000000:1794046200000000000",
		RequestedAt:    at(58.5),
		Steps:          []Step{StepStop, StepVerifiedBackup, StepDestroy},
		Reason:         "in_decision_window",
	}
	if !reflect.DeepEqual(h.requester.requests, []ShutdownRequest{wantRequest}) {
		t.Fatalf("requests = %+v, want %+v", h.requester.requests, wantRequest)
	}
	if !h.scheduler.InFlight("srv-1") {
		t.Fatal("srv-1 is not in flight after an accepted request")
	}
}

func TestPolicyDecisionsThatHoldTheServer(t *testing.T) {
	tests := []struct {
		name      string
		now       time.Time
		idleSince time.Time
		reason    string
		notBefore time.Time
	}{
		{"outside the window", at(45), at(5), "waiting_for_decision_window", at(58)},
		{"after the window closes", at(59), at(5), "waiting_for_decision_window", at(118)},
		{"idle_shutdown not reached", at(58.5), at(40), "not_idle_long_enough", at(118)},
		{"server has players", at(58.5), time.Time{}, "server_has_players", time.Time{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := idleServer()
			server.Idle.IdleSince = test.idleSince
			h := newHarness(t, test.now, server)
			got := h.tickOne(t)
			want := Outcome{ServerID: "srv-1", Reason: test.reason, NotBefore: test.notBefore}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("outcome = %+v, want %+v", got, want)
			}
			if n := h.requester.count(); n != 0 {
				t.Fatalf("requests = %d, want 0", n)
			}
		})
	}
}

func TestCheapServerIgnoresTheWindow(t *testing.T) {
	server := idleServer()
	server.Scaling = scalingpolicy.Settings{HourlyCost: "0.001", IgnoreBillingBelowHourlyCost: "0.005"}
	h := newHarness(t, at(45), server)
	got := h.tickOne(t)
	if !got.ShutdownRequested || got.Reason != "billing_window_not_required" {
		t.Fatalf("outcome = %+v, want a request for billing_window_not_required", got)
	}
}

func TestFailsClosedOnBadEvidence(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*Server)
		reason string
		err    string
	}{
		{"missing server ID", func(s *Server) { s.ID = "" }, ReasonMissingServerID, ""},
		{"unknown createdAt", func(s *Server) { s.CreatedAt = time.Time{} }, ReasonUnknownCreatedAt, ""},
		{"createdAt after now", func(s *Server) { s.CreatedAt = at(60) }, ReasonCreatedAtAfterNow, ""},
		{"missing idle evidence", func(s *Server) { s.Idle = IdleEvidence{IdleSince: at(10)} }, ReasonMissingIdleEvidence, ""},
		{"idleSince after now", func(s *Server) { s.Idle.IdleSince = at(59) }, ReasonIdleSinceAfterNow, ""},
		{"zero idle_shutdown", func(s *Server) { s.IdleShutdown = 0 }, ReasonInvalidIdleShutdown, ""},
		{"negative idle_shutdown", func(s *Server) { s.IdleShutdown = -time.Minute }, ReasonInvalidIdleShutdown, ""},
		{
			"invalid scaling policy",
			func(s *Server) { s.Scaling = scalingpolicy.Settings{DecisionWindowEnd: "61m"} },
			ReasonInvalidScalingPolicy,
			"idle scheduler: server srv-1: scaling decision window must end after it starts and within the billing increment",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := idleServer()
			test.edit(&server)
			h := newHarness(t, at(58.5), server)
			got := h.tickOne(t)
			if got.ShutdownRequested || got.Reason != test.reason || errText(got.Err) != test.err {
				t.Fatalf("outcome = %+v, want reason %q and error %q without a request", got, test.reason, test.err)
			}
			if n := h.requester.count(); n != 0 {
				t.Fatalf("requests = %d, want 0", n)
			}
		})
	}
}

func TestDuplicateServerIDsFailClosed(t *testing.T) {
	h := newHarness(t, at(58.5), idleServer(), idleServer())
	report := h.scheduler.Tick(context.Background())
	for _, outcome := range report.Outcomes {
		if outcome.ShutdownRequested || outcome.Reason != ReasonDuplicateServerID {
			t.Fatalf("outcome = %+v, want %s", outcome, ReasonDuplicateServerID)
		}
	}
	if len(report.Outcomes) != 2 || h.requester.count() != 0 {
		t.Fatalf("outcomes = %+v, requests = %d", report.Outcomes, h.requester.count())
	}
}

func TestInventoryFailureEvaluatesNothing(t *testing.T) {
	h := newHarness(t, at(58.5), idleServer())
	h.inventory.err = errors.New("store unavailable")
	report := h.scheduler.Tick(context.Background())
	if errText(report.Err) != "idle scheduler: list managed servers: store unavailable" {
		t.Fatalf("report error = %v", report.Err)
	}
	if len(report.Outcomes) != 0 || h.requester.count() != 0 || !report.At.Equal(at(58.5)) {
		t.Fatalf("report = %+v, requests = %d", report, h.requester.count())
	}
}

func TestFailedRequestIsNotInFlightAndRetriesWithTheSameKey(t *testing.T) {
	h := newHarness(t, at(58.2), idleServer())
	h.requester.err = errors.New("workflow engine unavailable")
	got := h.tickOne(t)
	if got.ShutdownRequested || got.Reason != ReasonRequestFailed {
		t.Fatalf("outcome = %+v, want %s", got, ReasonRequestFailed)
	}
	if errText(got.Err) != "idle scheduler: request shutdown of srv-1: workflow engine unavailable" {
		t.Fatalf("error = %v", got.Err)
	}
	if h.scheduler.InFlight("srv-1") {
		t.Fatal("a failed request must not be recorded as in flight")
	}
	h.requester.err = nil
	h.clock.set(at(58.7))
	if got := h.tickOne(t); !got.ShutdownRequested {
		t.Fatalf("retry outcome = %+v, want a request", got)
	}
	first, second := h.requester.requests[0], h.requester.requests[1]
	if first.IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("retry key %q differs from %q", second.IdempotencyKey, first.IdempotencyKey)
	}
}

func TestRepeatedTicksAreIdempotent(t *testing.T) {
	h := newHarness(t, at(58.1), idleServer())
	h.tickOne(t)
	for _, minute := range []float64{58.4, 58.9, 118.5} {
		h.clock.set(at(minute))
		got := h.tickOne(t)
		want := Outcome{ServerID: "srv-1", Reason: ReasonShutdownInFlight}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("minute %v: outcome = %+v, want %+v", minute, got, want)
		}
	}
	if n := h.requester.count(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	h.scheduler.Release("srv-1")
	if h.scheduler.InFlight("srv-1") {
		t.Fatal("srv-1 still in flight after Release")
	}
	if got := h.tickOne(t); !got.ShutdownRequested {
		t.Fatalf("outcome after Release = %+v, want a request", got)
	}
}

func TestConcurrentTicksRequestOnce(t *testing.T) {
	h := newHarness(t, at(58.5), idleServer())
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.scheduler.Tick(context.Background())
		}()
	}
	wg.Wait()
	if n := h.requester.count(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestShutdownSequenceReturnsAFreshSlice(t *testing.T) {
	steps := ShutdownSequence()
	steps[0] = StepDestroy
	if ShutdownSequence()[0] != StepStop {
		t.Fatal("ShutdownSequence shares its backing array")
	}
}

func TestClockFunc(t *testing.T) {
	if got := ClockFunc(func() time.Time { return created }).Now(); !got.Equal(created) {
		t.Fatalf("Now() = %v, want %v", got, created)
	}
}

func TestNewRequiresEveryDependency(t *testing.T) {
	clock, inventory, requester := &fakeClock{}, &fakeInventory{}, &fakeRequester{}
	tests := []struct {
		name string
		call func() (*Scheduler, error)
		want string
	}{
		{"clock", func() (*Scheduler, error) { return New(nil, inventory, requester) }, "idle scheduler: clock is required"},
		{"inventory", func() (*Scheduler, error) { return New(clock, nil, requester) }, "idle scheduler: inventory is required"},
		{"requester", func() (*Scheduler, error) { return New(clock, inventory, nil) }, "idle scheduler: shutdown requester is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheduler, err := test.call()
			if scheduler != nil || errText(err) != test.want {
				t.Fatalf("New() = %v, %v; want nil, %q", scheduler, err, test.want)
			}
		})
	}
}
