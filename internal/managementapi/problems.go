package managementapi

import (
	"encoding/json"
	"net/http"
)

// protocolError is the structured error carried in a Problem's protocol_error
// extension (protocol/rpc/errors.md).
type protocolError struct {
	Code      string `json:"code"`
	Category  string `json:"category"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Problems with their protocol codes. Messages never echo request values.
var (
	problemInvalidRequest     = protocolError{Code: "core.validation.invalid_request", Category: "validation", Message: "The request is invalid."}
	problemDenied             = protocolError{Code: "core.authorization.denied", Category: "authorization", Message: "The caller is not authorized for this operation."}
	problemConflict           = protocolError{Code: "core.operation.conflict", Category: "conflict", Message: "The request conflicts with an existing server or operation."}
	problemIdempotency        = protocolError{Code: "core.operation.idempotency_conflict", Category: "conflict", Message: "The Idempotency-Key was already used with a different request."}
	problemIdempotencyExpired = protocolError{Code: "core.operation.idempotency_key_expired", Category: "conflict", Message: "The Idempotency-Key was used for an operation that is no longer retained."}
	problemUnsupported        = protocolError{Code: "core.operation.unsupported_type", Category: "unsupported_capability", Message: "This controller cannot run that operation type yet."}
	problemNetwork            = protocolError{Code: "core.network.capability_missing", Category: "unsupported_capability", Message: "The server's network profile needs an integration capability that no adapter in its environment provides."}
	problemNotImplemented     = protocolError{Code: "core.api.not_implemented", Category: "unsupported_capability", Message: "This controller does not implement this endpoint yet."}
)

// writeProtocolProblem writes an RFC 9457 problem with the protocol error.
func writeProtocolProblem(w http.ResponseWriter, status int, title string, problem protocolError) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Type          string        `json:"type"`
		Title         string        `json:"title"`
		Status        int           `json:"status"`
		ProtocolError protocolError `json:"protocol_error"`
	}{Type: "about:blank", Title: title, Status: status, ProtocolError: problem})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
