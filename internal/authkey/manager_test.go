package authkey

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestIssueAuthenticateExpiryAndRevocation(t *testing.T) {
	db, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	scope := Scope{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}
	issued, err := manager.Issue(IssueRequest{
		ActorID: "automation-test", Audience: "controller-test", Scopes: []Scope{scope},
		CreatedBy: "operator", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(issued.Secret, tokenPrefix) || strings.Contains(issued.Secret, " ") {
		t.Fatalf("unexpected bearer token format")
	}
	encoded, err := json.Marshal(issued)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), issued.Secret) || strings.Contains(fmt.Sprintf("%+v", issued), issued.Secret) {
		t.Fatal("issued bearer token leaked through default formatting")
	}

	principal, err := manager.Authenticate(issued.Secret, "controller-test", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("authenticate issued key: %v", err)
	}
	if principal.ActorID != "automation-test" || principal.KeyID != issued.ID || len(principal.Scopes) != 1 || principal.Scopes[0] != scope {
		t.Fatalf("unexpected principal: %#v", principal)
	}
	if _, err := manager.Authenticate(issued.Secret, "another-controller", now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong audience authenticated: %v", err)
	}
	if _, err := manager.Authenticate(issued.Secret+"x", "controller-test", now); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("modified secret authenticated: %v", err)
	}
	if _, err := manager.Authenticate(issued.Secret, "controller-test", now.Add(time.Hour)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired key authenticated: %v", err)
	}

	record, err := db.GetManagementAPIKey(issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, secretBytes, ok := parseToken(issued.Secret)
	if !ok {
		t.Fatal("could not parse generated token")
	}
	digest := sha256.Sum256(secretBytes)
	zero(secretBytes)
	// The durable verifier is a one-way digest, never the bearer token itself.
	if len(record.Verifier) != sha256.Size || string(record.Verifier) != string(digest[:]) {
		t.Fatalf("invalid persisted verifier")
	}

	if err := manager.Revoke(issued.ID, "operator", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(issued.Secret, "controller-test", now.Add(3*time.Minute)); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked key authenticated: %v", err)
	}
	if err := manager.Revoke(issued.ID, "operator", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("repeat revocation should be idempotent: %v", err)
	}
}

func TestIssueRejectsWildcardAndInvalidScope(t *testing.T) {
	db, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager, _ := NewManager(db)
	now := time.Now().UTC()
	_, err = manager.Issue(IssueRequest{
		ActorID: "automation-test", Audience: "controller-test", CreatedBy: "operator",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Scopes: []Scope{{Permission: "*", Environment: "staging", Resource: "test-server"}},
	})
	if !errors.Is(err, store.ErrAPIKeyInvalid) {
		t.Fatalf("wildcard permission accepted: %v", err)
	}
}
