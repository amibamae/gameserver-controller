package keyadmin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type testAuthorizer struct {
	issueAllowed  bool
	revokeAllowed bool
	requester     string
	requested     IssueRequest
	keyID         string
}

func (a *testAuthorizer) AuthorizeIssue(_ context.Context, requester string, request IssueRequest) error {
	a.requester, a.requested = requester, request
	if !a.issueAllowed {
		return errors.New("denied")
	}
	return nil
}

func (a *testAuthorizer) AuthorizeRevoke(_ context.Context, requester, keyID string) error {
	a.requester, a.keyID = requester, keyID
	if !a.revokeAllowed {
		return errors.New("denied")
	}
	return nil
}

func TestServiceIssueUsesTrustedRequesterAndBoundedLifetime(t *testing.T) {
	manager, db := newManager(t)
	defer db.Close()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	authorizer := &testAuthorizer{issueAllowed: true}
	service, err := NewService(manager, authorizer, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := IssueRequest{
		ActorID: "automation", Audience: "controller-one",
		Scopes:   []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}},
		Lifetime: 24 * time.Hour,
	}
	issued, err := service.Issue(context.Background(), "uid-1001", request)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" || issued.CreatedAt != now || !issued.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected issued key metadata: %#v", issued)
	}
	if authorizer.requester != "uid-1001" || authorizer.requested.ActorID != request.ActorID || authorizer.requested.Scopes[0] != request.Scopes[0] {
		t.Fatalf("authorizer did not receive requester and requested scopes: %#v", authorizer)
	}
	if _, err := manager.Authenticate(issued.Secret, request.Audience, now.Add(time.Minute)); err != nil {
		t.Fatalf("issued key did not authenticate: %v", err)
	}
	if _, err := manager.Authenticate(issued.Secret, request.Audience, issued.ExpiresAt); !errors.Is(err, authkey.ErrUnauthenticated) {
		t.Fatalf("issued key remained valid after expiry: %v", err)
	}
}

func TestServiceRejectsUnauthorizedIssueAndRevoke(t *testing.T) {
	manager, db := newManager(t)
	defer db.Close()
	authorizer := &testAuthorizer{}
	service, err := NewService(manager, authorizer, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := IssueRequest{ActorID: "automation", Audience: "controller-one", Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}}, Lifetime: time.Hour}
	if _, err := service.Issue(context.Background(), "uid-1001", request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized issue was not rejected generically: %v", err)
	}
	if err := service.Revoke(context.Background(), "uid-1001", "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized revocation was not rejected generically: %v", err)
	}
}

func TestServiceRejectsMissingRequesterAndInvalidLifetime(t *testing.T) {
	manager, db := newManager(t)
	defer db.Close()
	authorizer := &testAuthorizer{issueAllowed: true}
	service, err := NewService(manager, authorizer, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	request := IssueRequest{ActorID: "automation", Audience: "controller-one", Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}}, Lifetime: time.Hour}
	if _, err := service.Issue(context.Background(), "", request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing requester was not denied: %v", err)
	}
	for _, lifetime := range []time.Duration{0, time.Second, MaxKeyLifetime + time.Second} {
		request.Lifetime = lifetime
		if _, err := service.Issue(context.Background(), "uid-1001", request); err == nil {
			t.Fatalf("accepted invalid lifetime %s", lifetime)
		}
	}
}

func TestServiceRevokeUsesTrustedRequester(t *testing.T) {
	manager, db := newManager(t)
	defer db.Close()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	authorizer := &testAuthorizer{issueAllowed: true, revokeAllowed: true}
	service, err := NewService(manager, authorizer, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Issue(context.Background(), "uid-1001", IssueRequest{ActorID: "automation", Audience: "controller-one", Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}}, Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(context.Background(), "uid-1001", issued.ID); err != nil {
		t.Fatal(err)
	}
	if authorizer.requester != "uid-1001" || authorizer.keyID != issued.ID {
		t.Fatalf("revocation authorization did not receive trusted identifiers: %#v", authorizer)
	}
	if _, err := manager.Authenticate(issued.Secret, "controller-one", now.Add(time.Minute)); !errors.Is(err, authkey.ErrUnauthenticated) {
		t.Fatalf("revoked key remained valid: %v", err)
	}
}

func newManager(t *testing.T) (*authkey.Manager, *store.BoltStore) {
	t.Helper()
	db, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := authkey.NewManager(db)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return manager, db
}
