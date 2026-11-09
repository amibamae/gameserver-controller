package managementauth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestAuthenticationEstablishesTrustedActorAndExactScope(t *testing.T) {
	manager, db, issued, now := newTestKey(t)
	defer db.Close()
	requirement := authkey.Scope{Permission: "server.inspect", Environment: "staging", Resource: "server-a"}
	allowScope, err := NewScopeAuthorization(requirement)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	endpoint := allowScope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("authenticated principal missing from request context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if principal.ActorID != "automation-test" {
			t.Errorf("client actor metadata replaced trusted actor: %q", principal.ActorID)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	clock := func() time.Time { return now.Add(time.Minute) }
	authenticate, err := NewAuthentication(manager, "controller-test", clock)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticate(endpoint)
	request := httptest.NewRequest(http.MethodGet, "/servers/server-a?access_token=ignored", nil)
	request.Header.Set("Authorization", "bearer "+issued.Secret)
	request.Header.Set("X-Actor-ID", "attacker-selected-actor")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("valid scoped key rejected: status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestAuthenticationRejectsInvalidCredentialsGenerically(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		request    func(*http.Request, string)
		clockDelta time.Duration
		revoke     bool
	}{
		{name: "missing authorization"},
		{name: "wrong scheme", request: func(r *http.Request, token string) { r.Header.Set("Authorization", "Basic "+token) }},
		{name: "duplicate authorization", request: func(r *http.Request, token string) {
			r.Header.Add("Authorization", "Bearer "+token)
			r.Header.Add("Authorization", "Bearer "+token)
		}},
		{name: "malformed bearer", request: func(r *http.Request, _ string) { r.Header.Set("Authorization", "Bearer bad token") }},
		{name: "invalid key", request: func(r *http.Request, _ string) { r.Header.Set("Authorization", "Bearer gsc1_invalid_invalid") }},
		{name: "wrong audience", configured: "another-controller", request: func(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) }},
		{name: "expired key", request: func(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) }, clockDelta: time.Hour},
		{name: "revoked key", request: func(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) }, revoke: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, db, issued, now := newTestKey(t)
			defer db.Close()
			if test.revoke {
				if err := manager.Revoke(issued.ID, "operator", now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			audience := test.configured
			if audience == "" {
				audience = "controller-test"
			}
			authenticate, err := NewAuthentication(manager, audience, func() time.Time { return now.Add(test.clockDelta) })
			if err != nil {
				t.Fatal(err)
			}
			called := false
			handler := authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			request := httptest.NewRequest(http.MethodGet, "/resource", nil)
			if test.request != nil {
				test.request(request, issued.Secret)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized || called {
				t.Fatalf("invalid credential reached handler: status=%d called=%v", response.Code, called)
			}
			if !strings.Contains(response.Header().Get("WWW-Authenticate"), "Bearer") || strings.Contains(response.Body.String(), issued.Secret) {
				t.Fatalf("authentication response leaked token or omitted challenge: headers=%v body=%q", response.Header(), response.Body.String())
			}
			if response.Header().Get("Content-Type") != "application/problem+json" || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), `"status":401`) {
				t.Fatalf("authentication response is not a non-cacheable problem response: headers=%v body=%q", response.Header(), response.Body.String())
			}
		})
	}
}

func TestScopeAuthorizationRejectsCrossEnvironmentAndResource(t *testing.T) {
	manager, db, issued, now := newTestKey(t)
	defer db.Close()
	authenticate, err := NewAuthentication(manager, "controller-test", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []authkey.Scope{
		{Permission: "server.inspect", Environment: "production", Resource: "server-a"},
		{Permission: "server.inspect", Environment: "staging", Resource: "server-b"},
		{Permission: "server.start", Environment: "staging", Resource: "server-a"},
	} {
		deny, err := NewScopeAuthorization(requirement)
		if err != nil {
			t.Fatal(err)
		}
		called := false
		handler := authenticate(deny(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
		request := httptest.NewRequest(http.MethodPost, "/operation", nil)
		request.Header.Set("Authorization", "Bearer "+issued.Secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || called {
			t.Fatalf("mismatched scope was not denied: scope=%#v status=%d called=%v", requirement, response.Code, called)
		}
	}
}

func TestScopeAuthorizationRequiresAnExactConfiguredScope(t *testing.T) {
	for _, requirement := range []authkey.Scope{
		{},
		{Permission: "*", Environment: "staging", Resource: "server-a"},
		{Permission: "server.inspect", Environment: "*", Resource: "server-a"},
		{Permission: "server.inspect", Environment: "staging", Resource: "*"},
	} {
		if _, err := NewScopeAuthorization(requirement); err == nil {
			t.Fatalf("accepted non-exact route scope: %#v", requirement)
		}
	}
}

func newTestKey(t *testing.T) (*authkey.Manager, *store.BoltStore, authkey.IssuedKey, time.Time) {
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
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	issued, err := manager.Issue(authkey.IssueRequest{
		ActorID: "automation-test", Audience: "controller-test",
		Scopes:    []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "server-a"}},
		CreatedBy: "operator", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return manager, db, issued, now
}
