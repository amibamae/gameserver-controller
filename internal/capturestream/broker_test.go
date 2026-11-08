package capturestream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Broker, Scope, Reference) {
	t.Helper()
	b, err := New(2)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	scope := Scope{OperationID: "op", StepID: "step", AdapterID: "game.paper", ServerID: "server", EnvironmentID: "test", InstanceAdapterID: "hosting.fake", InstanceID: "vm", DataGenerationID: "generation", DataSetID: "world", CaptureID: "capture"}
	ref, err := b.Issue(scope, 3, now.Add(time.Minute), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return b, scope, ref
}
func TestCaptureMeasuresBytesAndRejectsReplay(t *testing.T) {
	b, scope, ref := fixture(t)
	var sink bytes.Buffer
	wrong := scope
	wrong.InstanceID = "replacement"
	if _, err := b.Claim(context.Background(), ref.TransferID, wrong, &sink); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	w, err := b.Claim(context.Background(), ref.TransferID, scope, &sink)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	r, err := w.Complete()
	if err != nil {
		t.Fatal(err)
	}
	if r.SizeBytes != 3 || r.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || sink.String() != "abc" {
		t.Fatalf("invalid receipt: %#v", r)
	}
	if _, err := b.Claim(context.Background(), ref.TransferID, scope, &sink); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("handle replayed")
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatal("writer reused")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, ref), ref.TransferID) {
			t.Fatal("formatted reference leaked token")
		}
	}
}
func TestOnlyOneConcurrentClaimSucceeds(t *testing.T) {
	b, scope, ref := fixture(t)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, err := b.Claim(context.Background(), ref.TransferID, scope, io.Discard)
			if err == nil {
				count.Add(1)
				w.Abort()
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatalf("successful claims=%d", count.Load())
	}
}
func TestFailedCapturesNeverProduceReceipts(t *testing.T) {
	for _, scenario := range []string{"limit", "expired", "canceled", "short-write", "sink-error", "abort"} {
		t.Run(scenario, func(t *testing.T) {
			b, scope, ref := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sink io.Writer = io.Discard
			if scenario == "short-write" {
				sink = writerFunc(func(p []byte) (int, error) { return len(p) - 1, nil })
			}
			if scenario == "sink-error" {
				sink = writerFunc(func(p []byte) (int, error) { return 0, errors.New(ref.TransferID) })
			}
			w, err := b.Claim(ctx, ref.TransferID, scope, sink)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "limit":
				_, err = w.Write([]byte("abcd"))
			case "expired":
				b.now = func() time.Time { return ref.ExpiresAt }
				w.now = b.now
				_, err = w.Write([]byte("a"))
			case "canceled":
				cancel()
				_, err = w.Write([]byte("a"))
			case "abort":
				w.Abort()
			default:
				_, err = w.Write([]byte("abc"))
			}
			if scenario != "abort" && err == nil {
				t.Fatal("failure accepted")
			}
			if err != nil && strings.Contains(err.Error(), ref.TransferID) {
				t.Fatal("error leaked token")
			}
			if r, err := w.Complete(); err == nil || r != (Receipt{}) {
				t.Fatalf("failed stream produced evidence: %#v, %v", r, err)
			}
		})
	}
}
func TestExpiryDuringSinkWriteInvalidatesCapture(t *testing.T) {
	b, scope, ref := fixture(t)
	w, err := b.Claim(context.Background(), ref.TransferID, scope, writerFunc(func(p []byte) (int, error) { b.now = func() time.Time { return ref.ExpiresAt }; return len(p), nil }))
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return b.now() }
	if _, err := w.Write([]byte("abc")); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := w.Complete(); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}
func TestIssueLimitsAndExpiry(t *testing.T) {
	b, scope, ref := fixture(t)
	now := b.now()
	if _, err := b.Issue(scope, 3, now.Add(6*time.Minute), now.Add(time.Hour)); err == nil {
		t.Fatal("excess lifetime accepted")
	}
	if _, err := b.Issue(scope, 3, now.Add(time.Minute), now.Add(time.Second)); err == nil {
		t.Fatal("deadline exceeded")
	}
	if _, err := b.Issue(scope, 3, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Issue(scope, 3, now.Add(time.Minute), now.Add(time.Minute)); err == nil {
		t.Fatal("pending limit bypassed")
	}
	b.now = func() time.Time { return ref.ExpiresAt }
	if _, err := b.Claim(context.Background(), ref.TransferID, scope, io.Discard); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := b.Issue(scope, 3, b.now().Add(time.Minute), b.now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
