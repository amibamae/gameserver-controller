package simulation

import (
	"container/heap"
	"errors"
	"fmt"
	"time"
)

var errReentrantAdvance = errors.New("simulation: Advance called from inside a timer callback")

// Clock is a deterministic virtual clock. Time moves only when Advance is
// called, and timers fire synchronously, in due-time order, on the calling
// goroutine. Timers due at the same instant fire in the order they were
// scheduled. A Clock is not safe for concurrent use.
type Clock struct {
	now     time.Time
	seq     uint64
	queue   timerQueue
	running bool
	// onStep, when set, runs after every fired timer and at the end of each
	// Advance; an error stops the Advance and is returned from it.
	onStep func(step string) error
}

// Timer is a one-shot timer or a ticker scheduled on a Clock.
type Timer struct {
	clock  *Clock
	name   string
	when   time.Time
	seq    uint64
	period time.Duration
	fn     func(now time.Time)
	index  int
}

// NewClock returns a clock that reads start until it is advanced.
func NewClock(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the current virtual time.
func (c *Clock) Now() time.Time {
	return c.now
}

// Pending reports how many timers and tickers are still scheduled.
func (c *Clock) Pending() int {
	return len(c.queue)
}

// AfterFunc schedules fn to run once, d after the current virtual time. A
// negative d is treated as zero, so fn runs on the next Advance.
func (c *Clock) AfterFunc(name string, d time.Duration, fn func(now time.Time)) (*Timer, error) {
	return c.newTimer(name, max(d, 0), 0, fn)
}

// NewTicker schedules fn to run every period, starting one period from now.
func (c *Clock) NewTicker(name string, period time.Duration, fn func(now time.Time)) (*Timer, error) {
	if period <= 0 {
		return nil, fmt.Errorf("simulation: ticker %q period must be positive, got %s", name, period)
	}
	return c.newTimer(name, period, period, fn)
}

func (c *Clock) newTimer(name string, delay, period time.Duration, fn func(now time.Time)) (*Timer, error) {
	if fn == nil {
		return nil, fmt.Errorf("simulation: timer %q has no callback", name)
	}
	t := &Timer{clock: c, name: name, period: period, fn: fn, index: -1}
	c.schedule(t, c.now.Add(delay))
	return t, nil
}

func (c *Clock) schedule(t *Timer, when time.Time) {
	c.seq++
	t.when = when
	t.seq = c.seq
	heap.Push(&c.queue, t)
}

// Advance moves virtual time forward by d, firing every timer that falls due
// on the way at its own due time. A ticker is re-armed before its callback
// runs, so the callback may stop it. Timers scheduled by a callback fire in
// the same Advance when they fall due within it.
func (c *Clock) Advance(d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("simulation: cannot advance by negative duration %s", d)
	}
	if c.running {
		return errReentrantAdvance
	}
	c.running = true
	defer func() { c.running = false }()
	target := c.now.Add(d)
	for c.dueBy(target) {
		if err := c.fireNext(); err != nil {
			return err
		}
	}
	c.now = target
	return c.step("advance to " + target.Format(time.RFC3339Nano))
}

func (c *Clock) dueBy(target time.Time) bool {
	return len(c.queue) > 0 && !c.queue[0].when.After(target)
}

func (c *Clock) fireNext() error {
	t, _ := heap.Pop(&c.queue).(*Timer) // the queue only ever holds *Timer
	c.now = t.when
	if t.period > 0 {
		c.schedule(t, t.when.Add(t.period))
	}
	t.fn(c.now)
	return c.step(t.name)
}

func (c *Clock) step(name string) error {
	if c.onStep == nil {
		return nil
	}
	return c.onStep(name)
}

// Name returns the name the timer was scheduled with.
func (t *Timer) Name() string {
	return t.name
}

// Stop cancels the timer. It reports whether the timer was still scheduled.
func (t *Timer) Stop() bool {
	if t.index < 0 {
		return false
	}
	heap.Remove(&t.clock.queue, t.index)
	return true
}

// timerQueue is a min-heap ordered by due time, then scheduling order.
type timerQueue []*Timer

func (q timerQueue) Len() int { return len(q) }

func (q timerQueue) Less(i, j int) bool {
	if q[i].when.Equal(q[j].when) {
		return q[i].seq < q[j].seq
	}
	return q[i].when.Before(q[j].when)
}

func (q timerQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

func (q *timerQueue) Push(x any) {
	t, _ := x.(*Timer) // schedule only pushes *Timer
	t.index = len(*q)
	*q = append(*q, t)
}

func (q *timerQueue) Pop() any {
	old := *q
	n := len(old) - 1
	t := old[n]
	old[n] = nil
	t.index = -1
	*q = old[:n]
	return t
}
