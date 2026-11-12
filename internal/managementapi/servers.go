package managementapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/acceptance"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/controllerruntime"
	"github.com/amibamae/gameserver-controller/internal/managementauth"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// Repository is the read side of the state store the routes use.
type Repository interface {
	OperationRepository
	ListLogicalServers() ([]store.StoredLogicalServer, error)
}

// Controller is the runtime the routes submit operations to. It is
// implemented by *controllerruntime.Runtime.
type Controller interface {
	Accept(ctx context.Context, principal authkey.Principal, idempotencyKey string, record store.OperationRecord) (store.StoredOperation, bool, error)
	SupportsOperation(operationType string) bool
	CheckNetwork(serverID string) error
	Descriptors() []adapter.Descriptor
}

// Config wires the full management API.
type Config struct {
	Store           Repository
	Controller      Controller
	Authenticator   managementauth.Authenticator
	Audience        string
	ProtocolVersion string
	Clock           func() time.Time
}

const maxRequestBytes = 1 << 20

var idempotencyKeyPattern = regexp.MustCompile(`^[!-~]{1,128}$`)

// NewAPIHandler serves every management API route behind authentication.
func NewAPIHandler(config Config) (http.Handler, error) {
	if config.Store == nil || config.Controller == nil || config.Authenticator == nil || strings.TrimSpace(config.Audience) == "" || config.ProtocolVersion == "" {
		return nil, errors.New("store, controller, API-key authenticator, fixed audience, and protocol version are required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	cursorKey := make([]byte, 32)
	if _, err := rand.Read(cursorKey); err != nil {
		return nil, errors.New("could not create the pagination cursor key")
	}
	api := &api{config: config, cursorKey: cursorKey}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/operations/{operation_id}", func(w http.ResponseWriter, r *http.Request) { getOperation(w, r, config.Store) })
	mux.HandleFunc("POST /v1/operations/{operation_id}/cancel", api.notImplemented)
	mux.HandleFunc("GET /v1/operations/{operation_id}/events", api.notImplemented)
	mux.HandleFunc("GET /v1/capabilities", api.capabilities)
	mux.HandleFunc("GET /v1/servers", api.listServers)
	mux.HandleFunc("POST /v1/servers", api.defineServer)
	mux.HandleFunc("GET /v1/servers/{server_id}", api.getServer)
	mux.HandleFunc("POST /v1/servers/{server_id}/operations", api.submitOperation)
	return managementauthMiddleware(config.Authenticator, config.Audience, config.Clock, mux)
}

type api struct {
	config    Config
	cursorKey []byte
}

func principalOf(w http.ResponseWriter, r *http.Request) (authkey.Principal, bool) {
	principal, ok := managementauth.PrincipalFromContext(r.Context())
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "Unauthorized")
	}
	return principal, ok
}

func (a *api) notImplemented(w http.ResponseWriter, r *http.Request) {
	if _, ok := principalOf(w, r); ok {
		writeProtocolProblem(w, http.StatusNotImplemented, "Not Implemented", problemNotImplemented)
	}
}

// serverView is the public server resource (lifecycle/server.schema.json).
// Internal history such as lifecycle events is not returned.
func serverView(server store.LogicalServer) map[string]any {
	view := map[string]any{
		"server_id": server.ServerID, "environment_id": server.EnvironmentID, "display_name": server.DisplayName,
		"lifecycle_state": server.LifecycleState, "data_generation_status": server.DataGenerationStatus,
		"hosting_profile": server.HostingProfile, "storage_profile": server.StorageProfile,
		"game_adapter_id": server.GameAdapterID, "release_channel": server.ReleaseChannel,
		"execution_adapter_id": server.ExecutionAdapterID, "execution_profile": server.ExecutionProfile,
		"last_operation_id": server.LastOperationID,
		"created_at":        server.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at":        server.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if server.DesiredState != "" {
		view["desired_state"] = server.DesiredState
	}
	if server.DataGenerationID != "" {
		view["data_generation_id"] = server.DataGenerationID
	}
	if len(server.InstanceRef) != 0 {
		view["instance_ref"] = server.InstanceRef
	}
	if server.NetworkProfile != "" {
		view["network_profile"] = server.NetworkProfile
	}
	return view
}

// findServer returns the server with this ID if the principal may inspect
// it. A missing server and an inaccessible one are the same to the caller.
func (a *api) findServer(principal authkey.Principal, serverID string) (store.LogicalServer, bool, error) {
	servers, err := a.config.Store.ListLogicalServers()
	if err != nil {
		return store.LogicalServer{}, false, err
	}
	for _, server := range servers {
		if server.Record.ServerID == serverID {
			if !controllerruntime.HasScope(principal, "server.inspect", server.Record.EnvironmentID, serverID) {
				return store.LogicalServer{}, false, nil
			}
			return server.Record, true, nil
		}
	}
	return store.LogicalServer{}, false, nil
}

func (a *api) getServer(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	server, found, err := a.findServer(principal, r.PathValue("server_id"))
	switch {
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error")
	case !found:
		writeProblem(w, http.StatusNotFound, "Not Found")
	default:
		writeJSON(w, http.StatusOK, serverView(server))
	}
}

// cursor is the position after which a server listing continues. It is
// bound to the actor and the environment filter, and signed with a key that
// exists only in this process.
type cursor struct {
	Actor       string `json:"a"`
	Environment string `json:"e"`
	After       string `json:"p"`
}

func (a *api) encodeCursor(c cursor) string {
	payload, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, a.cursorKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *api) decodeCursor(text string) (cursor, bool) {
	payloadText, tagText, found := strings.Cut(text, ".")
	payload, err1 := base64.RawURLEncoding.DecodeString(payloadText)
	tag, err2 := base64.RawURLEncoding.DecodeString(tagText)
	if !found || err1 != nil || err2 != nil {
		return cursor{}, false
	}
	mac := hmac.New(sha256.New, a.cursorKey)
	mac.Write(payload)
	var c cursor
	if !hmac.Equal(tag, mac.Sum(nil)) || json.Unmarshal(payload, &c) != nil {
		return cursor{}, false
	}
	return c, true
}

func (a *api) listServers(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	environment := query.Get("environment_id")
	limit := 50
	if text := query.Get("limit"); text != "" {
		value, err := strconv.Atoi(text)
		if err != nil || value < 1 || value > 200 {
			writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
			return
		}
		limit = value
	}
	after := ""
	if text := query.Get("cursor"); text != "" {
		c, valid := a.decodeCursor(text)
		if !valid || c.Actor != principal.ActorID || c.Environment != environment {
			writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
			return
		}
		after = c.After
	}
	servers, err := a.config.Store.ListLogicalServers()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	var visible []store.LogicalServer
	for _, server := range servers {
		record := server.Record
		if (environment == "" || record.EnvironmentID == environment) && record.ServerID > after &&
			controllerruntime.HasScope(principal, "server.inspect", record.EnvironmentID, record.ServerID) {
			visible = append(visible, record)
		}
	}
	sort.Slice(visible, func(i, j int) bool { return visible[i].ServerID < visible[j].ServerID })
	items := []any{}
	for i := 0; i < len(visible) && i < limit; i++ {
		items = append(items, serverView(visible[i]))
	}
	page := map[string]any{"items": items}
	if len(visible) > limit {
		page["next_cursor"] = a.encodeCursor(cursor{Actor: principal.ActorID, Environment: environment, After: visible[limit-1].ServerID})
	}
	writeJSON(w, http.StatusOK, page)
}

// operationRequest is the body of define and submit requests.
type operationRequest struct {
	OperationType string          `json:"operation_type"`
	Parameters    json.RawMessage `json:"parameters"`
	Deadline      *time.Time      `json:"deadline,omitempty"`
}

func readOperationRequest(r *http.Request) (operationRequest, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return operationRequest{}, false
	}
	var request operationRequest
	if schemaregistry.DecodeStrictJSON(body, &request) != nil || request.OperationType == "" || len(request.Parameters) == 0 || request.Parameters[0] != '{' {
		return operationRequest{}, false
	}
	return request, true
}

func idempotencyKey(r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	return key, idempotencyKeyPattern.MatchString(key)
}

func (a *api) defineServer(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	key, validKey := idempotencyKey(r)
	request, validBody := readOperationRequest(r)
	if !validKey || !validBody || request.OperationType != "server.define" {
		writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
		return
	}
	var target struct {
		ServerID      string `json:"server_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if json.Unmarshal(request.Parameters, &target) != nil || target.ServerID == "" || target.EnvironmentID == "" {
		writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
		return
	}
	if !controllerruntime.HasScope(principal, "server.define", target.EnvironmentID, target.ServerID) {
		writeProtocolProblem(w, http.StatusForbidden, "Forbidden", problemDenied)
		return
	}
	a.accept(w, r, principal, key, store.OperationRecord{
		OperationType:   "server.define",
		Target:          store.OperationTarget{ServerID: target.ServerID, EnvironmentID: target.EnvironmentID},
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.define", Parameters: request.Parameters},
	})
}

func (a *api) submitOperation(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalOf(w, r)
	if !ok {
		return
	}
	key, validKey := idempotencyKey(r)
	if !validKey {
		writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
		return
	}
	server, found, err := a.findServer(principal, r.PathValue("server_id"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !found {
		writeProblem(w, http.StatusNotFound, "Not Found")
		return
	}
	request, validBody := readOperationRequest(r)
	if !validBody || request.OperationType == "server.define" || !strings.HasPrefix(request.OperationType, "server.") {
		writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
		return
	}
	if !controllerruntime.HasScope(principal, controllerruntime.PermissionFor(request.OperationType), server.EnvironmentID, server.ServerID) {
		writeProtocolProblem(w, http.StatusForbidden, "Forbidden", problemDenied)
		return
	}
	if !a.config.Controller.SupportsOperation(request.OperationType) {
		writeProtocolProblem(w, http.StatusUnprocessableEntity, "Unprocessable Content", problemUnsupported)
		return
	}
	if a.config.Controller.CheckNetwork(server.ServerID) != nil {
		writeProtocolProblem(w, http.StatusUnprocessableEntity, "Unprocessable Content", problemNetwork)
		return
	}
	a.accept(w, r, principal, key, store.OperationRecord{
		OperationType:   request.OperationType,
		Target:          store.OperationTarget{ServerID: server.ServerID, EnvironmentID: server.EnvironmentID},
		RequestSnapshot: store.RequestSnapshot{OperationType: request.OperationType, Parameters: request.Parameters},
	})
}

// accept records the operation and maps every failure to its protocol code.
func (a *api) accept(w http.ResponseWriter, r *http.Request, principal authkey.Principal, key string, record store.OperationRecord) {
	accepted, _, err := a.config.Controller.Accept(r.Context(), principal, key, record)
	var network *controllerruntime.NetworkError
	switch {
	case err == nil:
		w.Header().Set("Location", "/v1/operations/"+accepted.Record.OperationID)
		writeJSON(w, http.StatusAccepted, accepted.Record)
	case errors.Is(err, acceptance.ErrInvalidRequest), errors.Is(err, acceptance.ErrCallerSuppliedPlan):
		writeProtocolProblem(w, http.StatusBadRequest, "Bad Request", problemInvalidRequest)
	case errors.Is(err, controllerruntime.ErrNotAuthorized):
		writeProtocolProblem(w, http.StatusForbidden, "Forbidden", problemDenied)
	case errors.Is(err, controllerruntime.ErrUnsupportedOperation):
		writeProtocolProblem(w, http.StatusUnprocessableEntity, "Unprocessable Content", problemUnsupported)
	case errors.As(err, &network), errors.Is(err, controllerruntime.ErrNoNetworkProfile):
		writeProtocolProblem(w, http.StatusUnprocessableEntity, "Unprocessable Content", problemNetwork)
	case errors.Is(err, store.ErrIdempotencyConflict):
		writeProtocolProblem(w, http.StatusConflict, "Conflict", problemIdempotency)
	case errors.Is(err, store.ErrIdempotencyExpired):
		writeProtocolProblem(w, http.StatusConflict, "Conflict", problemIdempotencyExpired)
	case errors.Is(err, store.ErrLogicalServerAlreadyExists), errors.Is(err, store.ErrResourceConflict):
		writeProtocolProblem(w, http.StatusConflict, "Conflict", problemConflict)
	case errors.Is(err, store.ErrLogicalServerNotFound):
		writeProblem(w, http.StatusNotFound, "Not Found")
	default:
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
