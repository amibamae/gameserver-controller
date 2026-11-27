package store

import (
	"strings"
	"testing"
)

func TestIdempotencyValidationErrorOrder(t *testing.T) {
	tests := []struct {
		name  string
		proof IdempotencyProof
		want  string
	}{
		{"active version before candidates", proofOf("bad version", verifier("bad version", 1, 2)), "missing active key version or verifier candidates"},
		{"invalid candidate before repeated version", proofOf("v1", verifier("v1", 1, 2), IdempotencyVerifier{KeyVersion: "v1"}), "invalid idempotency verifier"},
		{"repeated version before later invalid candidate", proofOf("v1", verifier("v1", 1, 2), verifier("v1", 3, 4), IdempotencyVerifier{}), "duplicate idempotency key version"},
		{"missing active verifier after candidates", proofOf("v2", verifier("v1", 1, 2)), "active key verifier is missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateIdempotencyProof(test.proof)
			want := ErrInvalidRecord.Error() + ": " + test.want
			if err == nil || err.Error() != want {
				t.Fatalf("validateIdempotencyProof = %v, want %q", err, want)
			}
		})
	}
}

func TestIdempotencyPersistedIndexAndBinding(t *testing.T) {
	candidate := verifier("v1", 1, 2)
	want := "v1:" + strings.Repeat("01", 32)
	if got := string(verifierIndex(candidate)); got != want {
		t.Fatalf("verifierIndex = %q, want %q", got, want)
	}
	if !requestMatches(idempotencyEntry{BindingDigest: candidate.BindingDigest}, candidate) {
		t.Fatal("matching request binding was rejected")
	}
	if requestMatches(idempotencyEntry{BindingDigest: candidate.Digest}, candidate) {
		t.Fatal("verifier digest was accepted as the request binding")
	}
	if got := LogicalServerLockScope("staging", "server-1"); got != "server/7:staging/8:server-1" {
		t.Fatalf("lock scope = %q", got)
	}
}
