// Package capturestream brokers transient, single-use backup capture streams.
// Callers supply a private staging sink and publish it only after verification.
package capturestream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"sync"
	"time"
)

var (
	ErrUnauthorized = errors.New("capture stream is unavailable or outside the authorized scope")
	ErrExpired      = errors.New("capture stream expired or was canceled")
	ErrLimit        = errors.New("capture stream exceeds its byte limit")
	ErrClosed       = errors.New("capture stream is closed")
	ErrSink         = errors.New("capture staging sink failed")
)

// Scope binds the bearer handle to one durable capture and exact instance.
// None of these fields may come from an unauthenticated stream request.
type Scope struct {
	OperationID, StepID, AdapterID, ServerID, EnvironmentID               string
	InstanceAdapterID, InstanceID, DataGenerationID, DataSetID, CaptureID string
}

// Reference is invocation-only metadata. Never persist or log its bearer ID.
type Reference struct {
	TransferID   string    `json:"transfer_id"`
	Direction    string    `json:"direction"`
	MaximumBytes int64     `json:"maximum_bytes"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (Reference) String() string   { return "[transient capture stream]" }
func (Reference) GoString() string { return "[transient capture stream]" }

type permit struct {
	scope     Scope
	reference Reference
}
type Broker struct {
	mu      sync.Mutex
	pending map[string]permit
	limit   int
	now     func() time.Time
}

func New(maxPending int) (*Broker, error) {
	if maxPending < 1 || maxPending > 4096 {
		return nil, errors.New("pending capture stream limit must be 1..4096")
	}
	return &Broker{pending: make(map[string]permit), limit: maxPending, now: time.Now}, nil
}

// Issue bounds the lifetime by both five minutes and the adapter deadline.
func (b *Broker) Issue(scope Scope, maximumBytes int64, expiresAt, deadline time.Time) (Reference, error) {
	if b == nil {
		return Reference{}, ErrUnauthorized
	}
	for _, value := range []string{scope.OperationID, scope.StepID, scope.AdapterID, scope.ServerID, scope.EnvironmentID, scope.InstanceAdapterID, scope.InstanceID, scope.DataGenerationID, scope.DataSetID, scope.CaptureID} {
		if value == "" || len(value) > 500 {
			return Reference{}, ErrUnauthorized
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if maximumBytes < 1 || maximumBytes > 9007199254740991 || !expiresAt.After(now) || expiresAt.After(deadline) || expiresAt.Sub(now) > 5*time.Minute {
		return Reference{}, ErrUnauthorized
	}
	for id, p := range b.pending {
		if !p.reference.ExpiresAt.After(now) {
			delete(b.pending, id)
		}
	}
	if len(b.pending) >= b.limit {
		return Reference{}, errors.New("pending capture stream limit reached")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Reference{}, errors.New("issue capture stream failed")
	}
	id := hex.EncodeToString(random[:])
	if _, exists := b.pending[id]; exists {
		return Reference{}, errors.New("issue capture stream failed")
	}
	ref := Reference{TransferID: id, Direction: "adapter_to_controller", MaximumBytes: maximumBytes, ExpiresAt: expiresAt.UTC()}
	b.pending[id] = permit{scope: scope, reference: ref}
	return ref, nil
}

// Claim consumes a handle atomically before returning a writer. Failed or
// canceled captures cannot replay it. A scope mismatch does not consume it.
// The trusted caller owns the sink, including its cancellation and cleanup.
func (b *Broker) Claim(ctx context.Context, id string, scope Scope, sink io.Writer) (*Writer, error) {
	if b == nil || ctx == nil || sink == nil {
		return nil, ErrUnauthorized
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending[id]
	if !ok || p.scope != scope {
		return nil, ErrUnauthorized
	}
	if !p.reference.ExpiresAt.After(b.now()) {
		delete(b.pending, id)
		return nil, ErrExpired
	}
	delete(b.pending, id)
	if ctx.Err() != nil {
		return nil, ErrExpired
	}
	return &Writer{ctx: ctx, sink: sink, digest: sha256.New(), maximum: p.reference.MaximumBytes, expires: p.reference.ExpiresAt, now: b.now}, nil
}

// Receipt contains only independently measured evidence, never the handle.
type Receipt struct {
	SizeBytes int64
	SHA256    string
}
type Writer struct {
	mu            sync.Mutex
	ctx           context.Context
	sink          io.Writer
	digest        hash.Hash
	maximum, size int64
	expires       time.Time
	now           func() time.Time
	failure       error
	closed        bool
}

func (w *Writer) live() error {
	if w.failure != nil {
		return w.failure
	}
	if w.ctx.Err() != nil || !w.expires.After(w.now()) {
		w.failure = ErrExpired
		return w.failure
	}
	return nil
}
func (w *Writer) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, ErrClosed
	}
	if err := w.live(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.maximum-w.size {
		w.failure = ErrLimit
		return 0, w.failure
	}
	n, err := w.sink.Write(data)
	if n < 0 || n > len(data) {
		w.failure = ErrSink
		return 0, w.failure
	}
	if n > 0 {
		_, _ = w.digest.Write(data[:n])
		w.size += int64(n)
	}
	if err != nil || n != len(data) {
		w.failure = ErrSink
		return n, w.failure
	}
	if err := w.live(); err != nil {
		return n, err
	}
	return n, nil
}

// Complete must follow the transport's end-of-stream signal. A receipt alone
// does not validate the adapter's claimed size/digest or authorize storage.
func (w *Writer) Complete() (Receipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return Receipt{}, ErrClosed
	}
	w.closed = true
	if err := w.live(); err != nil {
		return Receipt{}, err
	}
	return Receipt{SizeBytes: w.size, SHA256: hex.EncodeToString(w.digest.Sum(nil))}, nil
}

// Abort permanently prevents evidence from being issued for a partial stream.
func (w *Writer) Abort() { w.mu.Lock(); defer w.mu.Unlock(); w.closed = true; w.failure = ErrClosed }
