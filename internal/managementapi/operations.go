// Package managementapi binds authenticated HTTP requests to controller
// application services. It does not start a listener or create provider
// resources.
package managementapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/managementauth"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type OperationRepository interface {
	GetOperation(operationID string) (store.StoredOperation, error)
}

// NewHandler creates the authenticated HTTP binding for the endpoints that
// have application-service implementations. The audience is fixed by trusted
// controller configuration, never by a request header or query parameter.
func NewHandler(repository OperationRepository, authenticator managementauth.Authenticator, audience string, clock func() time.Time) (http.Handler, error) {
	if repository == nil || authenticator == nil || strings.TrimSpace(audience) == "" {
		return nil, errors.New("operation repository, API-key authenticator, and fixed audience are required")
	}
	if clock == nil {
		clock = time.Now
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) {
		getOperation(w, r, repository)
	})
	return managementauthMiddleware(authenticator, audience, clock, mux)
}

func getOperation(w http.ResponseWriter, r *http.Request, repository OperationRepository) {
	operationID := r.PathValue("operation_id")
	if operationID == "" || len(operationID) > 200 || strings.TrimSpace(operationID) != operationID || strings.ContainsAny(operationID, "\r\n\x00") {
		writeProblem(w, http.StatusNotFound, "Not Found")
		return
	}
	principal, ok := managementauth.PrincipalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	stored, err := repository.GetOperation(operationID)
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "Not Found")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !hasScope(principal, authkey.Scope{
		Permission: "server.inspect", Environment: stored.Record.Target.EnvironmentID,
		Resource: stored.Record.Target.ServerID,
	}) {
		// An inaccessible operation is indistinguishable from a missing one.
		writeProblem(w, http.StatusNotFound, "Not Found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(stored.Record)
}

func hasScope(principal authkey.Principal, required authkey.Scope) bool {
	for _, scope := range principal.Scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func writeProblem(w http.ResponseWriter, status int, title string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Status int    `json:"status"`
	}{Type: "about:blank", Title: title, Status: status})
}

// Keep middleware construction in one place so no API handler can accidentally
// expose a route without authentication.
func managementauthMiddleware(authenticator managementauth.Authenticator, audience string, clock func() time.Time, next http.Handler) (http.Handler, error) {
	wrap, err := managementauth.NewAuthentication(authenticator, audience, clock)
	if err != nil {
		return nil, err
	}
	return wrap(next), nil
}
