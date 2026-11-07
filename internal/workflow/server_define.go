package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// ServerDefineDefinition completes the metadata-only server.define operation.
// The logical server itself is created atomically by operation acceptance.
type ServerDefineDefinition struct {
	servers store.LogicalServerRepository
}

func NewServerDefineDefinition(servers store.LogicalServerRepository) (*ServerDefineDefinition, error) {
	if servers == nil {
		return nil, errors.New("logical server repository is required")
	}
	return &ServerDefineDefinition{servers: servers}, nil
}

func (d *ServerDefineDefinition) CheckPreconditions(_ context.Context, operation store.OperationRecord, _ store.OperationStep) error {
	_, err := d.createdRecord(operation)
	return err
}

func (d *ServerDefineDefinition) Reconcile(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error) {
	return Resolution{State: ResolutionNeedsOperator}, nil
}

func (d *ServerDefineDefinition) BuildSuccessResult(_ context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	server, err := d.createdRecord(operation)
	if err != nil {
		return nil, err
	}
	result := struct {
		OutcomeSummary       string   `json:"outcome_summary"`
		OperationType        string   `json:"operation_type"`
		AffectedResources    []string `json:"affected_resources"`
		CompletedSteps       []any    `json:"completed_steps"`
		FailedSteps          []any    `json:"failed_steps"`
		Warnings             []string `json:"warnings"`
		UnresolvedConditions []any    `json:"unresolved_conditions"`
		AuditEventIDs        []string `json:"audit_event_ids"`
		Partial              bool     `json:"partial"`
		OperationData        struct {
			ServerID       string                    `json:"server_id"`
			EnvironmentID  string                    `json:"environment_id"`
			LifecycleState protocolstate.ServerState `json:"lifecycle_state"`
		} `json:"operation_data"`
	}{
		OutcomeSummary: "Logical server definition was recorded.", OperationType: "server.define",
		AffectedResources: []string{}, CompletedSteps: []any{}, FailedSteps: []any{},
		Warnings: []string{}, UnresolvedConditions: []any{}, AuditEventIDs: []string{},
		OperationData: struct {
			ServerID       string                    `json:"server_id"`
			EnvironmentID  string                    `json:"environment_id"`
			LifecycleState protocolstate.ServerState `json:"lifecycle_state"`
		}{ServerID: server.Record.ServerID, EnvironmentID: server.Record.EnvironmentID, LifecycleState: server.Record.LifecycleState},
	}
	return json.Marshal(result)
}

func (d *ServerDefineDefinition) BuildFailureResult(_ context.Context, operation store.OperationRecord, _ store.OperationStep, _ json.RawMessage) (json.RawMessage, error) {
	result := map[string]any{
		"outcome_summary": "Logical server definition did not complete.",
		"operation_type":  "server.define", "affected_resources": []any{},
		"completed_steps": []any{}, "failed_steps": []any{}, "warnings": []string{},
		"unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": true,
	}
	if operation.Target.ServerID != "" {
		result["operation_data"] = map[string]any{"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID, "lifecycle_state": string(protocolstate.ServerUnprovisioned)}
	}
	return json.Marshal(result)
}

func (d *ServerDefineDefinition) createdRecord(operation store.OperationRecord) (store.StoredLogicalServer, error) {
	if operation.OperationType != "server.define" || len(operation.Steps) != 0 {
		return store.StoredLogicalServer{}, errors.New("server.define must be a metadata-only operation")
	}
	server, err := d.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.StoredLogicalServer{}, fmt.Errorf("load defined logical server: %w", err)
	}
	if server.Record.LastOperationID != operation.OperationID ||
		server.Record.LifecycleState != protocolstate.ServerUnprovisioned ||
		!bytes.Equal(server.Record.Definition, operation.RequestSnapshot.Parameters) {
		return store.StoredLogicalServer{}, errors.New("logical server does not match the accepted definition operation")
	}
	return server, nil
}
