package integrationmonitor

import (
	"context"
	"sync"
	"time"
)

// Target is one monitored integration.
type Target struct {
	Subject Subject
	// Probe checks availability. It must not change integration state.
	Probe func(context.Context) Result
	// Initial is the result already observed at startup, if any. It is
	// recorded before the first scheduled probe.
	Initial Result
}

// Monitor probes each target on its own schedule and publishes the events its
// tracker emits.
type Monitor struct {
	policy  Policy
	targets []Target
	publish func(Event)
	now     func() time.Time
	sleep   func(context.Context, time.Duration) bool
	wg      sync.WaitGroup
}

// New returns a monitor. publish receives every event in order per target.
func New(policy Policy, targets []Target, publish func(Event)) *Monitor {
	return &Monitor{policy: policy, targets: targets, publish: publish, now: time.Now, sleep: sleepContext}
}

// Start launches one probe loop per target until ctx ends.
func (m *Monitor) Start(ctx context.Context) {
	for _, target := range m.targets {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.run(ctx, target)
		}()
	}
}

// Wait blocks until every probe loop has stopped.
func (m *Monitor) Wait() { m.wg.Wait() }

func (m *Monitor) run(ctx context.Context, target Target) {
	tracker := NewTracker(m.policy, target.Subject)
	delay := m.policy.CheckInterval
	if target.Initial != "" {
		delay = m.observe(tracker, target.Initial)
	}
	for m.sleep(ctx, delay) {
		result := target.Probe(ctx)
		// A probe cut short by shutdown says nothing about the integration.
		if ctx.Err() != nil {
			return
		}
		delay = m.observe(tracker, result)
	}
}

func (m *Monitor) observe(tracker *Tracker, result Result) time.Duration {
	events, delay := tracker.Observe(result, m.now())
	for _, event := range events {
		m.publish(event)
	}
	return delay
}
