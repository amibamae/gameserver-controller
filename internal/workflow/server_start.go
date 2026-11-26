package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	startInspectInstanceStep = "inspect-instance"
	startRuntimeStep         = "start-runtime"
	startVerifyRuntimeStep   = "verify-runtime"
	startReadinessStep       = "check-readiness"
)

// ServerStartProfile contains trusted release-specific values that do not
// belong in a client request. A resolver selects it from the stored logical
// server definition and controller-owned catalogs.
type ServerStartProfile struct {
	GameReleaseID       string
	UnitRef             json.RawMessage
	ReadinessCheckSetID string
	RequiredChecks      []ReadinessRequirement
}

// ServerStartProfileResolver must resolve immutable, trusted deployment
// metadata. It must not derive adapter steps from client-supplied fields.
type ServerStartProfileResolver interface {
	ResolveServerStart(context.Context, store.LogicalServer) (ServerStartProfile, error)
}

type serverStartParameters struct {
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref,omitempty"`
}

type instanceInspection struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  string          `json:"observed_at"`
}

type executionUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type executionUnitStartResult struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

// ServerStartWorkflow is both the trusted planner and the durable definition
// for server.start. It uses the same injected object during acceptance and
// execution so profile selection and lifecycle policy share one source.
type ServerStartWorkflow struct {
	servers  store.LifecycleOperationStore
	adapters adapterRuntime
	schemas  schemaRuntime
	profiles ServerStartProfileResolver
	ttl      time.Duration
}

func NewServerStartWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerStartProfileResolver, assertionTTL time.Duration) (*ServerStartWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, and trusted start-profile resolver are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerStartWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, ttl: assertionTTL}, nil
}

var _ interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
	Definition
	LifecycleProjector
	interface {
		ValidateCompletion(context.Context, store.OperationRecord) error
	}
} = (*ServerStartWorkflow)(nil)

// planSchemas returns the schemas when w holds every dependency planning
// needs, and nil otherwise, which checkPlanRequest refuses.
func (w *ServerStartWorkflow) planSchemas() schemaRuntime {
	if w == nil || w.profiles == nil || !lifecycleRuntimeReady(w.servers, w.adapters, w.schemas) {
		return nil
	}
	return w.schemas
}
