// Package managementauth binds controller API-key authentication and exact
// scope checks to HTTP handlers. It does not start a listener or select routes.
package managementauth

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
)

type Authenticator interface {
	Authenticate(token, audience string, now time.Time) (authkey.Principal, error)
}

type contextKey struct{}

var exactName = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// NewAuthentication constructs request authentication using one fixed
// controller audience. The audience is never read from a request.
func NewAuthentication(manager Authenticator, audience string, clock func() time.Time) (func(http.Handler) http.Handler, error) {
	if manager == nil || strings.TrimSpace(audience) == "" {
		return nil, errors.New("API-key authenticator and fixed controller audience are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if next == nil {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			values := r.Header.Values("Authorization")
			if len(values) != 1 {
				unauthorized(w)
				return
			}
			scheme, token, hasToken := strings.Cut(values[0], " ")
			if !hasToken || !strings.EqualFold(scheme, "Bearer") {
				unauthorized(w)
				return
			}
			if token == "" || strings.ContainsAny(token, " \t\r\n") {
				unauthorized(w)
				return
			}
			principal, err := manager.Authenticate(token, audience, clock().UTC())
			if err != nil || principal.ActorID == "" || principal.KeyID == "" {
				unauthorized(w)
				return
			}
			ctx := context.WithValue(r.Context(), contextKey{}, clonePrincipal(principal))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}

// NewScopeAuthorization constructs a route-level exact scope check. Route
// handlers choose the requirement from trusted route/configuration data, not
// from client-supplied actor, permission, environment, or resource fields.
func NewScopeAuthorization(required authkey.Scope) (func(http.Handler) http.Handler, error) {
	if !exactName.MatchString(required.Permission) || !exactName.MatchString(required.Environment) || !exactName.MatchString(required.Resource) {
		return nil, errors.New("an exact permission, environment, and resource scope are required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if next == nil {
				http.Error(w, "service unavailable", http.StatusServiceUnavailable)
				return
			}
			principal, ok := PrincipalFromContext(r.Context())
			if !ok {
				unauthorized(w)
				return
			}
			for _, scope := range principal.Scopes {
				if scope == required {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}, nil
}

// PrincipalFromContext returns a copy of the authenticated principal. It does
// not inspect client request metadata.
func PrincipalFromContext(ctx context.Context) (authkey.Principal, bool) {
	if ctx == nil {
		return authkey.Principal{}, false
	}
	principal, ok := ctx.Value(contextKey{}).(authkey.Principal)
	if !ok || principal.ActorID == "" || principal.KeyID == "" {
		return authkey.Principal{}, false
	}
	return clonePrincipal(principal), true
}

func clonePrincipal(principal authkey.Principal) authkey.Principal {
	principal.Scopes = append([]authkey.Scope(nil), principal.Scopes...)
	return principal
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
