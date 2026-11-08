package capturestream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestTrackedCapturePinsSinkAndExposesIndependentEvidence(t *testing.T) {
	b, scope, _ := fixture(t)
	var staging, substitute bytes.Buffer
	ref, ticket, err := b.IssueTracked(scope, 3, b.now().Add(time.Minute), b.now().Add(time.Minute), &staging)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Evidence(); !errors.Is(err, ErrIncomplete) {
		t.Fatal("unclaimed capture had evidence")
	}
	writer, err := b.Claim(context.Background(), ref.TransferID, scope, &substitute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := ticket.Evidence(); !errors.Is(err, ErrIncomplete) {
		t.Fatal("open stream had evidence")
	}
	if _, err := writer.Complete(); err != nil {
		t.Fatal(err)
	}
	receipt, err := ticket.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Scope != scope || receipt.SizeBytes != 3 || receipt.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("invalid evidence: %#v", receipt)
	}
	if staging.String() != "abc" || substitute.Len() != 0 {
		t.Fatal("adapter substituted staging destination")
	}
	ticket.Abort()
	if _, err := ticket.Evidence(); !errors.Is(err, ErrIncomplete) {
		t.Fatal("aborted ticket retained evidence")
	}
}
func TestTicketAbortRevokesPendingAndRacesSafelyWithClaim(t *testing.T) {
	for i := 0; i < 20; i++ {
		b, scope, _ := fixture(t)
		ref, ticket, err := b.IssueTracked(scope, 3, b.now().Add(time.Minute), b.now().Add(time.Minute), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			writer, err := b.Claim(context.Background(), ref.TransferID, scope, nil)
			if err == nil {
				_, _ = writer.Write([]byte("abc"))
				_, _ = writer.Complete()
			}
		}()
		go func() { defer wg.Done(); ticket.Abort() }()
		wg.Wait()
		if _, err := ticket.Evidence(); !errors.Is(err, ErrIncomplete) {
			t.Fatal("aborted capture had usable evidence")
		}
		if _, err := b.Claim(context.Background(), ref.TransferID, scope, io.Discard); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("aborted handle was replayed")
		}
	}
}
func TestFailedTrackedStreamHasNoEvidence(t *testing.T) {
	b, scope, _ := fixture(t)
	ref, ticket, err := b.IssueTracked(scope, 1, b.now().Add(time.Minute), b.now().Add(time.Minute), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := b.Claim(context.Background(), ref.TransferID, scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("too large")); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	_, _ = writer.Complete()
	if _, err := ticket.Evidence(); !errors.Is(err, ErrIncomplete) {
		t.Fatal("failed capture had evidence")
	}
}
