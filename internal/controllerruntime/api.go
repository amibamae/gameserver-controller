package controllerruntime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/amibamae/gameserver-controller/internal/acceptance"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// Errors the management API maps to protocol problems.
var (
	// ErrUnsupportedOperation: no runnable workflow for the operation type
	// (core.operation.unsupported_type).
	ErrUnsupportedOperation = errors.New("the controller has no runnable workflow for this operation type")
	// ErrNotAuthorized: the actor lacks the exact scope (core.authorization.denied).
	ErrNotAuthorized = errors.New("the actor is not authorized for this operation")
	// ErrNoIdempotencyKeys: the controller was built without idempotency keys.
	ErrNoIdempotencyKeys = errors.New("controller.idempotency_keys are not configured")
)

// PermissionFor is the scope permission an operation type needs. Every
// server operation type uses its own name, for example server.start.
func PermissionFor(operationType string) string { return operationType }

// SupportsOperation reports whether this runtime can run operations of the
// given type to completion.
func (r *Runtime) SupportsOperation(operationType string) bool {
	return slices.Contains(RegisteredOperationTypes, operationType)
}

// Descriptors returns the registered adapter descriptors, sorted by ID.
func (r *Runtime) Descriptors() []adapter.Descriptor {
	descriptors := make([]adapter.Descriptor, 0, len(r.adapters))
	for _, id := range r.AdapterIDs() {
		if descriptor, err := r.Registry.Descriptor(id); err == nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	slices.SortFunc(descriptors, func(a, b adapter.Descriptor) int {
		switch {
		case a.AdapterID < b.AdapterID:
			return -1
		case a.AdapterID > b.AdapterID:
			return 1
		}
		return 0
	})
	return descriptors
}

// Accept validates, authorizes, plans, and durably records one operation for
// an authenticated principal, then queues it for the runner. replayed is
// true when the idempotency key matched an earlier identical request.
func (r *Runtime) Accept(ctx context.Context, principal authkey.Principal, idempotencyKey string, record store.OperationRecord) (store.StoredOperation, bool, error) {
	if r.keyring == nil {
		return store.StoredOperation{}, false, ErrNoIdempotencyKeys
	}
	validator, err := acceptance.NewSchemaRequestValidator(r.Schemas)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	service, err := acceptance.NewService(r.Store, validator, scopeAuthorizer{principal: principal, runtime: r},
		acceptance.LogicalServerLockPolicy{}, acceptance.OperationPlanFunc(r.plan), r.keyring)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	record.ProtocolVersion = r.Schemas.ProtocolVersion()
	accepted, replayed, err := service.Accept(ctx, principal.ActorID, idempotencyKey, record)
	if err != nil {
		return accepted, replayed, err
	}
	if r.runner != nil && !protocolstate.IsTerminalOperation(accepted.Record.Status) {
		r.runner.submit(accepted.Record.OperationID)
	}
	return accepted, replayed, nil
}

// plan builds the trusted step plan. server.define has no adapter steps.
// Other operation types are refused until their workflows are registered,
// after the network profile check of integrations section 6.
func (r *Runtime) plan(_ context.Context, record store.OperationRecord) ([]store.OperationStep, error) {
	if record.OperationType != "server.define" {
		if err := r.CheckNetwork(record.Target.ServerID); err != nil {
			return nil, err
		}
	}
	if !r.SupportsOperation(record.OperationType) {
		return nil, ErrUnsupportedOperation
	}
	return nil, nil
}

// scopeAuthorizer checks the principal's exact scopes, as in
// protocol/management-api/authorization.md.
type scopeAuthorizer struct {
	principal authkey.Principal
	runtime   *Runtime
}

func (a scopeAuthorizer) AuthorizeTarget(_ context.Context, actorID string, record store.OperationRecord) error {
	if actorID != a.principal.ActorID || !HasScope(a.principal, PermissionFor(record.OperationType), record.Target.EnvironmentID, record.Target.ServerID) {
		return ErrNotAuthorized
	}
	return nil
}

func (a scopeAuthorizer) AuthorizeOperation(ctx context.Context, actorID string, record store.OperationRecord) error {
	if err := a.AuthorizeTarget(ctx, actorID, record); err != nil {
		return err
	}
	if !a.runtime.SupportsOperation(record.OperationType) {
		return ErrUnsupportedOperation
	}
	return nil
}

// HasScope reports whether the principal holds exactly this scope. Scopes
// never match by prefix or wildcard.
func HasScope(principal authkey.Principal, permission, environmentID, resourceID string) bool {
	return slices.Contains(principal.Scopes, authkey.Scope{Permission: permission, Environment: environmentID, Resource: resourceID})
}

// buildKeyring reads the configured idempotency keys from systemd
// credentials. Without configured keys it returns nil and the management API
// cannot accept operations.
func buildKeyring(keys config.IdempotencyKeys, credentialDirectory string) (*acceptance.Keyring, error) {
	if len(keys.Versions) == 0 {
		return nil, nil
	}
	resolver, err := credential.NewResolver(credentialDirectory)
	if err != nil {
		return nil, fmt.Errorf("controller.idempotency_keys need systemd credentials: %w", err)
	}
	material := map[string][]byte{}
	defer func() {
		for _, key := range material {
			clear(key)
		}
	}()
	for version, reference := range keys.Versions {
		if err := resolver.WithCredential(reference, func(secret []byte) error {
			material[version] = append([]byte(nil), secret...)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("idempotency key %s is unavailable", version)
		}
	}
	keyring, err := acceptance.NewKeyring(keys.Active, material)
	if err != nil {
		return nil, fmt.Errorf("controller.idempotency_keys: %w", err)
	}
	return keyring, nil
}
