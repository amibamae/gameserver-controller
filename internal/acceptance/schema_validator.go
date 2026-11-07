package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// ProtocolSchemaRegistry is the compiled, immutable schema package used by
// request validation.
type ProtocolSchemaRegistry interface {
	ProtocolVersion() string
	HasSchema(string) bool
	ValidateJSON(string, []byte) error
}

// SchemaRequestValidator validates the external standard-operation request
// against the compiled protocol package before planning or authorization.
type SchemaRequestValidator struct {
	schemas ProtocolSchemaRegistry
}

func NewSchemaRequestValidator(schemas ProtocolSchemaRegistry) (*SchemaRequestValidator, error) {
	if schemas == nil || schemas.ProtocolVersion() == "" {
		return nil, errors.New("compiled protocol schema registry is required")
	}
	requestSchema := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:operation-request"
	secretSchema := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:secret-reference"
	if !schemas.HasSchema(requestSchema) || !schemas.HasSchema(secretSchema) {
		return nil, errors.New("operation request and secret reference schemas must be registered")
	}
	return &SchemaRequestValidator{schemas: schemas}, nil
}

func (v *SchemaRequestValidator) ValidateOperationRequest(ctx context.Context, record store.OperationRecord) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.schemas == nil {
		return errors.New("schema request validator is not initialized")
	}
	if record.ProtocolVersion != v.schemas.ProtocolVersion() {
		return fmt.Errorf("operation protocol version %q is not loaded", record.ProtocolVersion)
	}
	if record.RequestSnapshot.OperationType != record.OperationType {
		return errors.New("request snapshot operation_type does not match operation")
	}
	request := struct {
		OperationType string          `json:"operation_type"`
		Parameters    json.RawMessage `json:"parameters"`
		Deadline      string          `json:"deadline,omitempty"`
	}{OperationType: record.OperationType, Parameters: record.RequestSnapshot.Parameters}
	if record.Deadline != nil {
		request.Deadline = record.Deadline.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode operation request: %w", err)
	}
	requestSchema := "urn:gameserver-controller:" + v.schemas.ProtocolVersion() + ":operations:operation-request"
	if err := v.schemas.ValidateJSON(requestSchema, encoded); err != nil {
		return fmt.Errorf("operation request does not match the protocol schema: %w", err)
	}
	if err := validateRequestTarget(record); err != nil {
		return err
	}
	secretSchema := "urn:gameserver-controller:" + v.schemas.ProtocolVersion() + ":common:secret-reference"
	for index, ref := range record.RequestSnapshot.SecretRefs {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := json.Marshal(ref)
		if err != nil {
			return fmt.Errorf("encode secret reference %d: %w", index, err)
		}
		if err := v.schemas.ValidateJSON(secretSchema, value); err != nil {
			return fmt.Errorf("secret reference %d does not match the protocol schema: %w", index, err)
		}
	}
	return nil
}

func validateRequestTarget(record store.OperationRecord) error {
	if record.OperationType != "server.define" && record.OperationType != "server.provision" {
		return nil
	}
	var target struct {
		ServerID      string `json:"server_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal(record.RequestSnapshot.Parameters, &target); err != nil {
		return errors.New("operation parameters do not contain a valid server target")
	}
	if target.ServerID != record.Target.ServerID || target.EnvironmentID != record.Target.EnvironmentID {
		return errors.New("operation target does not match the validated parameters")
	}
	return nil
}
