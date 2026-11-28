package managementapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func TestLostDefineResponseReplaysWithoutNewOperation(t *testing.T) {
	f := newAPIFixture(t)
	scope := authkey.Scope{Permission: "server.define", Environment: "staging", Resource: "lost-response"}
	token := f.as("operator/initial", scope)
	body := `{"operation_type":"server.define","parameters":` + definitionFor("lost-response", "staging", "Paper") + `}`
	first := f.do(http.MethodPost, "/v1/servers", token, "lost-response-key", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("initial acceptance = %d %s", first.Code, first.Body)
	}
	// The client lost the response and has no operation reference. The test
	// retains it only as an oracle for checking identity after replay.
	location := first.Header().Get("Location")
	rotated := f.as("operator/rotated", scope)
	replay := f.do(http.MethodPost, "/v1/servers", rotated, "lost-response-key", body)
	if replay.Code != http.StatusAccepted || location == "" || replay.Header().Get("Location") != location {
		t.Fatalf("replay = %d %s; want original operation", replay.Code, replay.Body)
	}
	operations, err := f.controller.store.ListRecoverableOperations()
	if err != nil || len(operations) != 1 {
		t.Fatalf("recoverable operations = %d, %v; want exactly one", len(operations), err)
	}
	finished, err := f.engine.Run(context.Background(), operations[0].Record.OperationID)
	if err != nil || finished.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("original operation = %q, %v", finished.Record.Status, err)
	}
	again := f.do(http.MethodPost, "/v1/servers", rotated, "lost-response-key", body)
	if again.Code != http.StatusAccepted || again.Header().Get("Location") != location {
		t.Fatalf("terminal replay = %d %s", again.Code, again.Body)
	}
}

func TestLostDefineResponseDoesNotBypassRevokedScope(t *testing.T) {
	f := newAPIFixture(t)
	scope := authkey.Scope{Permission: "server.define", Environment: "staging", Resource: "revoked"}
	token := f.as("operator/initial", scope)
	body := `{"operation_type":"server.define","parameters":` + definitionFor("revoked", "staging", "Paper") + `}`
	first := f.do(http.MethodPost, "/v1/servers", token, "revoked-key", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("initial acceptance = %d %s", first.Code, first.Body)
	}
	revoked := f.as("operator/revoked")
	replay := f.do(http.MethodPost, "/v1/servers", revoked, "revoked-key", body)
	if replay.Code != http.StatusForbidden || protocolCode(t, replay) != "core.authorization.denied" {
		t.Fatalf("replay with revoked scope = %d %s", replay.Code, replay.Body)
	}
	operations, err := f.controller.store.ListRecoverableOperations()
	if err != nil || len(operations) != 1 {
		t.Fatalf("recoverable operations = %d, %v; want original only", len(operations), err)
	}
}
