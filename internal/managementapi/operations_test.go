package managementapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type testAuthenticator struct {
	principal authkey.Principal
	err       error
}

func (a testAuthenticator) Authenticate(token, audience string, _ time.Time) (authkey.Principal, error) {
	if token != "valid-token" || audience != "controller-test" {
		return authkey.Principal{}, authkey.ErrUnauthenticated
	}
	return a.principal, a.err
}

type testOperationRepository struct {
	operation store.StoredOperation
	err       error
	calls     int
}

func (r *testOperationRepository) GetOperation(id string) (store.StoredOperation, error) {
	r.calls++
	if id != r.operation.Record.OperationID {
		return store.StoredOperation{}, store.ErrNotFound
	}
	return r.operation, r.err
}

func TestGetOperationRequiresAuthenticationBeforeStorageRead(t *testing.T) {
	repository := testOperationRepositoryWithRecord()
	handler, err := NewHandler(repository, testAuthenticator{}, "controller-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/operations/op-1", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || repository.calls != 0 {
		t.Fatalf("unauthenticated request reached storage: status=%d calls=%d", response.Code, repository.calls)
	}
}

func TestGetOperationRequiresExactServerAndEnvironmentScope(t *testing.T) {
	for _, scopes := range [][]authkey.Scope{
		{{Permission: "server.inspect", Environment: "other", Resource: "server-1"}},
		{{Permission: "server.inspect", Environment: "staging", Resource: "other-server"}},
		{{Permission: "server.destroy", Environment: "staging", Resource: "server-1"}},
		{{Permission: "server.inspect", Environment: "*", Resource: "server-1"}},
	} {
		repository := testOperationRepositoryWithRecord()
		handler := handlerFor(t, repository, authkey.Principal{KeyID: "key-1", ActorID: "automation", Scopes: scopes})
		response := authenticatedGet(handler, "/v1/operations/op-1")
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("unauthorized lookup disclosed operation: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestGetOperationReturnsAuthorizedStoredResource(t *testing.T) {
	repository := testOperationRepositoryWithRecord()
	principal := authkey.Principal{KeyID: "key-1", ActorID: "automation", Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "server-1"}}}
	handler := handlerFor(t, repository, principal)
	response := authenticatedGet(handler, "/v1/operations/op-1")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), `"operation_id":"op-1"`) {
		t.Fatalf("response missing no-store or operation resource: headers=%v body=%s", response.Header(), response.Body.String())
	}
}

func TestGetOperationHidesRepositoryFailures(t *testing.T) {
	repository := testOperationRepositoryWithRecord()
	repository.err = errors.New("sensitive database diagnostic")
	handler := handlerFor(t, repository, authkey.Principal{KeyID: "key-1", ActorID: "automation", Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "server-1"}}})
	response := authenticatedGet(handler, "/v1/operations/op-1")
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "sensitive") {
		t.Fatalf("repository error was disclosed: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNewHandlerRequiresRepositoryAndAudience(t *testing.T) {
	auth := testAuthenticator{principal: authkey.Principal{KeyID: "key-1", ActorID: "automation"}}
	if _, err := NewHandler(nil, auth, "controller-test", nil); err == nil {
		t.Fatal("accepted missing repository")
	}
	if _, err := NewHandler(&testOperationRepository{}, auth, " ", nil); err == nil {
		t.Fatal("accepted blank audience")
	}
}

func handlerFor(t *testing.T, repository OperationRepository, principal authkey.Principal) http.Handler {
	t.Helper()
	handler, err := NewHandler(repository, testAuthenticator{principal: principal}, "controller-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func authenticatedGet(handler http.Handler, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer valid-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func testOperationRepositoryWithRecord() *testOperationRepository {
	return &testOperationRepository{operation: store.StoredOperation{Record: store.OperationRecord{
		OperationID: "op-1", OperationType: "server.backup", ProtocolVersion: "0.21.0-draft",
		Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		Status: "succeeded", ActorID: "automation",
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.backup", Parameters: []byte(`{"server_id":"server-1"}`)},
		CreatedAt:       time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 11, 9, 12, 1, 0, 0, time.UTC),
		Steps:           []store.OperationStep{},
	}}}
}
