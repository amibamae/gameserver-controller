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
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	authorizer := &testAuthorizer{issueAllowed: true}
	service, manager := newTestService(t, authorizer, func() time.Time { return now })
	request := testIssueRequest(24 * time.Hour)
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
	assertIssuedKeyLifetime(t, manager, issued, request.Audience, now)
}

func TestServiceRejectsUnauthorizedIssueAndRevoke(t *testing.T) {
	authorizer := &testAuthorizer{}
	service, _ := newTestService(t, authorizer, time.Now)
	request := testIssueRequest(time.Hour)
	if _, err := service.Issue(context.Background(), "uid-1001", request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized issue was not rejected generically: %v", err)
	}
	if err := service.Revoke(context.Background(), "uid-1001", "0123456789abcdef0123456789abcdef"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unauthorized revocation was not rejected generically: %v", err)
	}
}

func TestServiceRejectsMissingRequesterAndInvalidLifetime(t *testing.T) {
	authorizer := &testAuthorizer{issueAllowed: true}
	service, _ := newTestService(t, authorizer, time.Now)
	request := testIssueRequest(time.Hour)
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
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	authorizer := &testAuthorizer{issueAllowed: true, revokeAllowed: true}
	service, manager := newTestService(t, authorizer, func() time.Time { return now })
	issued, err := service.Issue(context.Background(), "uid-1001", testIssueRequest(time.Hour))
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

func assertIssuedKeyLifetime(t *testing.T, manager *authkey.Manager, issued authkey.IssuedKey, audience string, now time.Time) {
	t.Helper()
	if _, err := manager.Authenticate(issued.Secret, audience, now.Add(time.Minute)); err != nil {
		t.Fatalf("issued key did not authenticate: %v", err)
	}
	if _, err := manager.Authenticate(issued.Secret, audience, issued.ExpiresAt); !errors.Is(err, authkey.ErrUnauthenticated) {
		t.Fatalf("issued key remained valid after expiry: %v", err)
	}
}

func testIssueRequest(lifetime time.Duration) IssueRequest {
	return IssueRequest{
		ActorID:  "automation",
		Audience: "controller-one",
		Scopes:   []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "test-server"}},
		Lifetime: lifetime,
	}
}

func newTestService(t *testing.T, authorizer *testAuthorizer, clock func() time.Time) (*Service, *authkey.Manager) {
	t.Helper()
	db, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	manager, err := authkey.NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(manager, authorizer, clock)
	if err != nil {
		t.Fatal(err)
	}
	return service, manager
}
