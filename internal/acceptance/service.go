package acceptance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gowebpki/jcs"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var (
	ErrUnauthorized        = errors.New("operation was not authorized")
	ErrFingerprintMismatch = errors.New("supplied request fingerprint does not match validated parameters")
	ErrCallerSuppliedPlan  = errors.New("adapter execution plans are controller-owned")
	ErrInvalidPlan         = errors.New("controller produced an invalid operation plan")
)

type OperationStore interface {
	AcceptOperation(record store.OperationRecord, proof store.IdempotencyProof, lockScopes []string) (store.StoredOperation, bool, error)
}

type RequestValidator interface {
	ValidateOperationRequest(context.Context, store.OperationRecord) error
}

type Authorizer interface {
	// AuthorizeTarget checks actor access to the operation and target without
	// resolved steps. It runs before the planner can inspect target state.
	AuthorizeTarget(context.Context, string, store.OperationRecord) error
	AuthorizeOperation(context.Context, string, store.OperationRecord) error
}

type LockPolicy interface {
	LockScopes(store.OperationRecord) ([]string, error)
}

// OperationPlanner resolves a validated request into immutable adapter steps
// using trusted configuration and catalog data. Client-supplied steps are
// never accepted as a plan.
type OperationPlanner interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
}

type OperationPlanFunc func(context.Context, store.OperationRecord) ([]store.OperationStep, error)

func (f OperationPlanFunc) BuildPlan(ctx context.Context, record store.OperationRecord) ([]store.OperationStep, error) {
	if f == nil {
		return nil, errors.New("operation planner function is nil")
	}
	return f(ctx, record)
}

type Service struct {
	store      OperationStore
	validator  RequestValidator
	authorizer Authorizer
	locks      LockPolicy
	planner    OperationPlanner
	keyring    *Keyring
}

func NewService(operationStore OperationStore, validator RequestValidator, authorizer Authorizer, locks LockPolicy, planner OperationPlanner, keyring *Keyring) (*Service, error) {
	if operationStore == nil || validator == nil || authorizer == nil || locks == nil || planner == nil || keyring == nil {
		return nil, errors.New("operation store, request validator, authorizer, lock policy, operation planner, and idempotency keyring are required")
	}
	return &Service{store: operationStore, validator: validator, authorizer: authorizer, locks: locks, planner: planner, keyring: keyring}, nil
}

// Accept validates and authorizes the target before asking the injected
// controller planner to build its immutable step plan. It authorizes that exact
// plan, binds idempotency to the
// authenticated actor and canonical request, then atomically stores the
// operation and resource reservations before any worker can dispatch it.
func (s *Service) Accept(ctx context.Context, authenticatedActorID, idempotencyKey string, record store.OperationRecord) (store.StoredOperation, bool, error) {
	if ctx == nil {
		return store.StoredOperation{}, false, errors.New("context is required")
	}
	if authenticatedActorID == "" {
		return store.StoredOperation{}, false, ErrUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return store.StoredOperation{}, false, err
	}
	if idempotencyKey == "" {
		return store.StoredOperation{}, false, errors.New("Idempotency-Key is required")
	}
	if len(record.Steps) != 0 {
		return store.StoredOperation{}, false, ErrCallerSuppliedPlan
	}
	operationID, err := NewOperationID()
	if err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("create operation ID: %w", err)
	}
	record.OperationID = operationID
	record.ActorID = authenticatedActorID
	record.IdempotencyKey = idempotencyKey
	record.Target, err = canonicalTarget(record.Target)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	if err := s.validator.ValidateOperationRequest(ctx, record); err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("validate operation request: %w", err)
	}
	fingerprint, err := RequestFingerprint(record.RequestSnapshot.Parameters)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	if record.RequestFingerprint != "" && record.RequestFingerprint != fingerprint {
		return store.StoredOperation{}, false, ErrFingerprintMismatch
	}
	record.RequestFingerprint = fingerprint
	if err := s.authorizer.AuthorizeTarget(ctx, authenticatedActorID, record); err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("authorize target: %w", err)
	}
	steps, err := s.planner.BuildPlan(ctx, record)
	if err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("build trusted operation plan: %w", err)
	}
	if record.OperationType == "server.define" && len(steps) != 0 {
		return store.StoredOperation{}, false, fmt.Errorf("%w: server.define must not dispatch adapter steps", ErrInvalidPlan)
	}
	if strings.HasPrefix(record.OperationType, "server.") && record.OperationType != "server.define" && len(steps) == 0 {
		return store.StoredOperation{}, false, fmt.Errorf("%w: mutating server operation has no steps", ErrInvalidPlan)
	}
	record.Steps = append([]store.OperationStep(nil), steps...)
	if err := s.authorizer.AuthorizeOperation(ctx, authenticatedActorID, record); err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("authorize operation: %w", err)
	}
	binding, err := requestBinding(record)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	proof, err := s.keyring.Proof(authenticatedActorID, idempotencyKey, binding)
	if err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("prepare idempotency verifier: %w", err)
	}
	scopes, err := s.locks.LockScopes(record)
	if err != nil {
		return store.StoredOperation{}, false, fmt.Errorf("resolve resource locks: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return store.StoredOperation{}, false, err
	}
	accepted, replayed, err := s.store.AcceptOperation(record, proof, scopes)
	if err != nil {
		return store.StoredOperation{}, false, err
	}
	return accepted, replayed, nil
}

func NewOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func requestBinding(record store.OperationRecord) ([]byte, error) {
	value := struct {
		OperationType      string                  `json:"operation_type"`
		Target             store.OperationTarget   `json:"target"`
		RequestFingerprint string                  `json:"request_fingerprint"`
		SecretRefs         []store.SecretReference `json:"secret_refs,omitempty"`
	}{
		OperationType:      record.OperationType,
		Target:             record.Target,
		RequestFingerprint: record.RequestFingerprint,
		SecretRefs:         record.RequestSnapshot.SecretRefs,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode idempotency request binding: %w", err)
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return nil, fmt.Errorf("canonicalize idempotency request binding: %w", err)
	}
	return canonical, nil
}

func canonicalTarget(target store.OperationTarget) (store.OperationTarget, error) {
	encoded, err := json.Marshal(target)
	if err != nil {
		return store.OperationTarget{}, fmt.Errorf("encode operation target: %w", err)
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return store.OperationTarget{}, fmt.Errorf("canonicalize operation target: %w", err)
	}
	var normalized store.OperationTarget
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return store.OperationTarget{}, fmt.Errorf("decode canonical operation target: %w", err)
	}
	return normalized, nil
}

// LogicalServerLockPolicy serializes all standard server lifecycle operations
// on the same environment-scoped server record.
type LogicalServerLockPolicy struct{}

func (LogicalServerLockPolicy) LockScopes(record store.OperationRecord) ([]string, error) {
	if strings.HasPrefix(record.OperationType, "server.") {
		return []string{LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)}, nil
	}
	return nil, nil
}

func LogicalServerLockScope(environmentID, serverID string) string {
	return store.LogicalServerLockScope(environmentID, serverID)
}
