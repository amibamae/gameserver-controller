package managementapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/acceptance"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/controllerruntime"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/workflow"
)

// tokenAuthenticator maps bearer tokens to principals.
type tokenAuthenticator map[string]authkey.Principal

func (a tokenAuthenticator) Authenticate(token, audience string, _ time.Time) (authkey.Principal, error) {
	principal, ok := a[token]
	if !ok || audience != "controller-test" {
		return authkey.Principal{}, authkey.ErrUnauthenticated
	}
	return principal, nil
}

// testController runs the real acceptance service against a real bbolt
// store. Unlike the runtime, it can be told which operation types are
// runnable and which servers satisfy their network profile, so the published
// decision vector can be run in full.
type testController struct {
	store      *store.BoltStore
	schemas    *schemaregistry.Registry
	keyring    *acceptance.Keyring
	registered []string
	network    map[string]bool
}

func (c *testController) SupportsOperation(t string) bool { return slices.Contains(c.registered, t) }

func (c *testController) CheckNetwork(serverID string) error {
	if !c.network[serverID] {
		return &controllerruntime.NetworkError{ServerID: serverID, Missing: []controllerruntime.CapabilityRequirement{{}}}
	}
	return nil
}

func (c *testController) Descriptors() []adapter.Descriptor {
	return []adapter.Descriptor{{AdapterID: "storage.test", Category: adapter.CategoryStorage}}
}

func (c *testController) Accept(ctx context.Context, principal authkey.Principal, key string, record store.OperationRecord) (store.StoredOperation, bool, error) {
	validator, err := acceptance.NewSchemaRequestValidator(c.schemas)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	planner := acceptance.OperationPlanFunc(func(_ context.Context, record store.OperationRecord) ([]store.OperationStep, error) {
		if record.OperationType == "server.define" {
			return nil, nil
		}
		return []store.OperationStep{{StepID: "step-1", StepType: "adapter.invoke", Status: protocolstate.StepPending,
			AdapterID: "execution.test", CapabilityName: "execution.unit.start", CapabilityVersion: "1", Input: json.RawMessage(`{}`)}}, nil
	})
	service, err := acceptance.NewService(c.store, validator, testAuthorizer{principal: principal, controller: c}, acceptance.LogicalServerLockPolicy{}, planner, c.keyring)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	record.ProtocolVersion = c.schemas.ProtocolVersion()
	return service.Accept(ctx, principal.ActorID, key, record)
}

type testAuthorizer struct {
	principal  authkey.Principal
	controller *testController
}

func (a testAuthorizer) AuthorizeTarget(_ context.Context, _ string, record store.OperationRecord) error {
	if !controllerruntime.HasScope(a.principal, record.OperationType, record.Target.EnvironmentID, record.Target.ServerID) {
		return controllerruntime.ErrNotAuthorized
	}
	return nil
}

func (a testAuthorizer) AuthorizeOperation(ctx context.Context, actor string, record store.OperationRecord) error {
	return a.AuthorizeTarget(ctx, actor, record)
}

func definitionFor(serverID, environmentID, displayName string) string {
	return `{"server_id":"` + serverID + `","environment_id":"` + environmentID + `","display_name":"` + displayName + `","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`
}

type apiFixture struct {
	handler    http.Handler
	controller *testController
	tokens     tokenAuthenticator
	engine     *workflow.Engine
}

type refuseResume struct{}

func (refuseResume) AuthorizeResume(context.Context, string, store.OperationRecord) error {
	return errors.New("resume is not used in these tests")
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	database, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := acceptance.NewKeyring("v1", map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	f := &apiFixture{controller: &testController{store: database, schemas: schemas, keyring: keyring}, tokens: tokenAuthenticator{}}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	definitions := workflow.NewDefinitions()
	define, err := workflow.NewServerDefineDefinition(database)
	if err != nil || definitions.Register("server.define", define) != nil {
		t.Fatal("register server.define", err)
	}
	if f.engine, err = workflow.NewEngine(database, registry, schemas, definitions, refuseResume{}, time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	f.handler, err = NewAPIHandler(Config{Store: database, Controller: f.controller, Authenticator: f.tokens, Audience: "controller-test", ProtocolVersion: schemas.ProtocolVersion()})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *apiFixture) as(token string, scopes ...authkey.Scope) string {
	f.tokens[token] = authkey.Principal{KeyID: token, ActorID: "actor-" + strings.SplitN(token, "/", 2)[0], Scopes: scopes}
	return token
}

func (f *apiFixture) do(method, path, token, key, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	return response
}

func (f *apiFixture) define(t *testing.T, serverID, environmentID string) {
	t.Helper()
	token := f.as("setup/"+serverID, authkey.Scope{Permission: "server.define", Environment: environmentID, Resource: serverID})
	body := `{"operation_type":"server.define","parameters":` + definitionFor(serverID, environmentID, "Paper") + `}`
	response := f.do(http.MethodPost, "/v1/servers", token, "setup-"+serverID, body)
	var operation store.OperationRecord
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &operation) != nil {
		t.Fatalf("setup define %s = %d %s", serverID, response.Code, response.Body)
	}
	// Run the definition so it releases the server lock, as the runner would.
	if finished, err := f.engine.Run(context.Background(), operation.OperationID); err != nil || finished.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("setup define %s ran to %q, %v", serverID, finished.Record.Status, err)
	}
}

func scopesOf(raw []any) []authkey.Scope {
	scopes := []authkey.Scope{}
	for _, value := range raw {
		s := value.(map[string]any)
		scopes = append(scopes, authkey.Scope{Permission: s["permission"].(string), Environment: s["environment_id"].(string), Resource: s["resource_id"].(string)})
	}
	return scopes
}

func protocolCode(t *testing.T, response *httptest.ResponseRecorder) any {
	t.Helper()
	var problem struct {
		ProtocolError *struct {
			Code string `json:"code"`
		} `json:"protocol_error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("response is not JSON: %s", response.Body)
	}
	if problem.ProtocolError == nil {
		return nil
	}
	return problem.ProtocolError.Code
}

// TestSubmitDecisionsMatchThePublishedVector runs every case of
// management-operations-submit-decision.json through the HTTP routes.
func TestSubmitDecisionsMatchThePublishedVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "management-operations-submit-decision.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Input struct {
			Servers []struct {
				ServerID      string `json:"server_id"`
				EnvironmentID string `json:"environment_id"`
			} `json:"servers"`
			Registered  []string         `json:"registered_operation_types"`
			Network     map[string]bool  `json:"network_profile_satisfied"`
			Evaluations []map[string]any `json:"evaluations"`
		} `json:"input"`
		Expected struct {
			Evaluations []map[string]any `json:"evaluations"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	for i, c := range vector.Input.Evaluations {
		want := vector.Expected.Evaluations[i]
		caseID := c["case_id"].(string)
		t.Run(caseID, func(t *testing.T) {
			f := newAPIFixture(t)
			f.controller.registered, f.controller.network = vector.Input.Registered, vector.Input.Network
			for _, server := range vector.Input.Servers {
				f.define(t, server.ServerID, server.EnvironmentID)
			}
			serverID, operationType, key := c["server_id"].(string), c["operation_type"].(string), c["idempotency_key"].(string)
			var path, body, otherBody string
			if c["route"] == "define" {
				environment := c["environment_id"].(string)
				path = "/v1/servers"
				body = `{"operation_type":"` + operationType + `","parameters":` + definitionFor(serverID, environment, "Paper") + `}`
				otherBody = `{"operation_type":"` + operationType + `","parameters":` + definitionFor(serverID, environment, "Other") + `}`
			} else {
				path = "/v1/servers/" + serverID + "/operations"
				body = `{"operation_type":"` + operationType + `","parameters":{}}`
				otherBody = `{"operation_type":"` + operationType + `","parameters":{"expected_instance_ref":{"adapter_id":"hosting.vultr","environment_id":"staging","resource_kind":"instance","external_id":"i-1"}}}`
			}
			token := f.as("case", scopesOf(c["actor_scopes"].([]any))...)
			priorID := ""
			if prior, ok := c["prior_use"].(string); ok {
				priorBody := body
				if prior == "different_request" {
					priorBody = otherBody
				}
				response := f.do(http.MethodPost, path, token, key, priorBody)
				if response.Code != http.StatusAccepted {
					t.Fatalf("prior request = %d %s", response.Code, response.Body)
				}
				var operation store.OperationRecord
				_ = json.Unmarshal(response.Body.Bytes(), &operation)
				priorID = operation.OperationID
			}
			response := f.do(http.MethodPost, path, token, key, body)
			if float64(response.Code) != want["status"].(float64) {
				t.Fatalf("status = %d, want %v: %s", response.Code, want["status"], response.Body)
			}
			if response.Code == http.StatusAccepted {
				var operation store.OperationRecord
				if err := json.Unmarshal(response.Body.Bytes(), &operation); err != nil || operation.OperationID == "" {
					t.Fatalf("accepted body = %s", response.Body)
				}
				if got := response.Header().Get("Location"); got != "/v1/operations/"+operation.OperationID {
					t.Fatalf("Location = %q", got)
				}
				if replayed := operation.OperationID == priorID; replayed != want["replayed"].(bool) {
					t.Fatalf("replayed = %v, want %v", replayed, want["replayed"])
				}
				return
			}
			if got := protocolCode(t, response); got != want["error_code"] {
				t.Fatalf("error code = %v, want %v", got, want["error_code"])
			}
		})
	}
}

func TestServerReadsShowOnlyInspectableServers(t *testing.T) {
	f := newAPIFixture(t)
	for _, id := range []string{"a-1", "a-2", "a-3", "b-1"} {
		f.define(t, id, "staging")
	}
	f.define(t, "p-1", "production")
	scopes := []authkey.Scope{}
	for _, id := range []string{"a-1", "a-2", "a-3"} {
		scopes = append(scopes, authkey.Scope{Permission: "server.inspect", Environment: "staging", Resource: id})
	}
	scopes = append(scopes, authkey.Scope{Permission: "server.inspect", Environment: "production", Resource: "p-1"})
	reader := f.as("reader", scopes...)

	list := func(query, token string) (int, []string, string) {
		response := f.do(http.MethodGet, "/v1/servers"+query, token, "", "")
		var page struct {
			Items []struct {
				ServerID string `json:"server_id"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &page)
		ids := []string{}
		for _, item := range page.Items {
			ids = append(ids, item.ServerID)
		}
		return response.Code, ids, page.NextCursor
	}
	code, ids, next := list("?environment_id=staging&limit=2", reader)
	if code != http.StatusOK || strings.Join(ids, ",") != "a-1,a-2" || next == "" {
		t.Fatalf("first page = %d %v %q", code, ids, next)
	}
	code, ids, last := list("?environment_id=staging&limit=2&cursor="+next, reader)
	if code != http.StatusOK || strings.Join(ids, ",") != "a-3" || last != "" {
		t.Fatalf("second page = %d %v %q", code, ids, last)
	}
	if code, ids, _ := list("", reader); code != http.StatusOK || strings.Join(ids, ",") != "a-1,a-2,a-3,p-1" {
		t.Fatalf("unfiltered list = %d %v", code, ids)
	}
	for name, query := range map[string]string{
		"cursor for another filter": "?cursor=" + next,
		"tampered cursor":           "?environment_id=staging&cursor=" + next[:len(next)-2] + "AA",
		"limit too large":           "?limit=201",
		"limit zero":                "?limit=0",
	} {
		if code, _, _ := list(query, reader); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	other := f.as("other", scopes...)
	if code, _, _ := list("?environment_id=staging&cursor="+next, other); code != http.StatusBadRequest {
		t.Errorf("a cursor reused by another actor = %d, want 400", code)
	}

	response := f.do(http.MethodGet, "/v1/servers/a-1", reader, "", "")
	var server map[string]any
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &server) != nil || server["environment_id"] != "staging" || server["lifecycle_state"] == nil {
		t.Fatalf("GET a-1 = %d %s", response.Code, response.Body)
	}
	if _, leaked := server["lifecycle_events"]; leaked {
		t.Fatal("internal lifecycle history was returned")
	}
	view, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:server", view); err != nil {
		t.Fatalf("server view does not match the protocol schema: %v", err)
	}
	for _, path := range []string{"/v1/servers/b-1", "/v1/servers/missing"} {
		if response := f.do(http.MethodGet, path, reader, "", ""); response.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, response.Code)
		}
	}
}

func TestCapabilitiesAndUnimplementedRoutes(t *testing.T) {
	f := newAPIFixture(t)
	token := f.as("anyone")
	response := f.do(http.MethodGet, "/v1/capabilities", token, "", "")
	var body struct {
		ProtocolVersions []string             `json:"protocol_versions"`
		Capabilities     []adapter.Descriptor `json:"capabilities"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.ProtocolVersions) != 1 || len(body.Capabilities) != 1 {
		t.Fatalf("capabilities = %d %s", response.Code, response.Body)
	}
	for _, route := range [][2]string{{http.MethodPost, "/v1/operations/op-1/cancel"}, {http.MethodGet, "/v1/operations/op-1/events"}} {
		response := f.do(route[0], route[1], token, "", "")
		if response.Code != http.StatusNotImplemented || protocolCode(t, response) != "core.api.not_implemented" {
			t.Errorf("%s %s = %d %s", route[0], route[1], response.Code, response.Body)
		}
	}
	if response := f.do(http.MethodGet, "/v1/capabilities", "unknown", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated capabilities = %d", response.Code)
	}
}

func TestDefineRejectsMalformedBodies(t *testing.T) {
	f := newAPIFixture(t)
	token := f.as("definer", authkey.Scope{Permission: "server.define", Environment: "staging", Resource: "paper-new"})
	valid := definitionFor("paper-new", "staging", "Paper")
	for name, body := range map[string]string{
		"duplicate member":   `{"operation_type":"server.define","operation_type":"server.define","parameters":` + valid + `}`,
		"unknown member":     `{"operation_type":"server.define","parameters":` + valid + `,"extra":1}`,
		"invalid definition": `{"operation_type":"server.define","parameters":{"server_id":"paper-new","environment_id":"staging"}}`,
		"parameters array":   `{"operation_type":"server.define","parameters":[]}`,
		"oversized":          `{"operation_type":"server.define","parameters":` + valid + `,"pad":"` + strings.Repeat("x", maxRequestBytes) + `"}`,
	} {
		response := f.do(http.MethodPost, "/v1/servers", token, "k-"+name, body)
		if response.Code != http.StatusBadRequest || protocolCode(t, response) != "core.validation.invalid_request" {
			t.Errorf("%s = %d %s", name, response.Code, response.Body)
		}
	}
	if response := f.do(http.MethodPost, "/v1/servers", token, "bad key", `{"operation_type":"server.define","parameters":`+valid+`}`); response.Code != http.StatusBadRequest {
		t.Errorf("an Idempotency-Key with a space = %d, want 400", response.Code)
	}
}
