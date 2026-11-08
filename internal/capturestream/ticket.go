package capturestream

import (
	"errors"
	"sync"
)

var ErrIncomplete = errors.New("capture stream has no completed evidence")

// Ticket stays with the controller. It is never passed to the adapter or
// persisted; the adapter receives only the transient Reference.
type Ticket struct {
	mu     sync.Mutex
	writer *Writer
	broker *Broker
	id     string
}

func (*Ticket) String() string   { return "[private capture ticket]" }
func (*Ticket) GoString() string { return "[private capture ticket]" }

// Evidence reads broker-measured completion independently of adapter output.
// The scope must be matched to the durable step before using this receipt.
func (t *Ticket) Evidence() (Receipt, error) {
	if t == nil {
		return Receipt{}, ErrIncomplete
	}
	t.mu.Lock()
	w := t.writer
	t.mu.Unlock()
	if w == nil {
		return Receipt{}, ErrIncomplete
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.completed || w.failure != nil {
		return Receipt{}, ErrIncomplete
	}
	return w.receipt, nil
}

// Abort revokes an unclaimed handle or invalidates a claimed stream. It does
// not interrupt blocked sink I/O or remove staged bytes; the caller owns both.
func (t *Ticket) Abort() {
	if t == nil || t.broker == nil {
		return
	}
	t.broker.mu.Lock()
	delete(t.broker.pending, t.id)
	t.mu.Lock()
	w := t.writer
	t.mu.Unlock()
	t.broker.mu.Unlock()
	if w != nil {
		w.Abort()
	}
}
