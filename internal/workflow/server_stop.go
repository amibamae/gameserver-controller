package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	stopInspectInstanceStep = "inspect-instance"
	stopInspectUnitStep     = "inspect-runtime"
	stopSaveGameStep        = "save-game-data"
	stopRuntimeStep         = "stop-runtime"
)

const defaultGracefulStopTimeout = 2 * time.Minute

// ServerStopProfile contains trusted release and execution references selected
// from controller-owned deployment metadata.
type ServerStopProfile struct {
	GameReleaseID string
	UnitRef       json.RawMessage
}

// ServerStopProfileResolver must resolve immutable trusted metadata. Client
// parameters cannot choose the game release or execution unit.
type ServerStopProfileResolver interface {
	ResolveServerStop(context.Context, store.LogicalServer) (ServerStopProfile, error)
}

type serverStopParameters struct {
	DrainTimeoutMS    *int64          `json:"drain_timeout_ms,omitempty"`
	Drain             *bool           `json:"drain,omitempty"`
	GracefulTimeoutMS *int64          `json:"graceful_timeout_ms,omitempty"`
	Force             bool            `json:"force,omitempty"`
	ApprovalID        string          `json:"approval_id,omitempty"`
	ExpectedInstance  json.RawMessage `json:"expected_instance_ref,omitempty"`
}

type ServerStopWorkflow struct {
	servers  store.LifecycleOperationStore
	adapters adapterRuntime
	schemas  schemaRuntime
	profiles ServerStopProfileResolver
}

func NewServerStopWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerStopProfileResolver) (*ServerStopWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, and trusted stop-profile resolver are required")
	}
	return &ServerStopWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles}, nil
}

// planSchemas returns the schemas when w holds every dependency planning
// needs, and nil otherwise, which checkPlanRequest refuses.
func (w *ServerStopWorkflow) planSchemas() schemaRuntime {
	if w == nil || w.profiles == nil || !lifecycleRuntimeReady(w.servers, w.adapters, w.schemas) {
		return nil
	}
	return w.schemas
}

// drainRequested reports whether the request asks for proxy drain, which is
// the default.
func (p serverStopParameters) drainRequested() bool {
	return p.Drain == nil || *p.Drain
}

func gracefulStopPeriod(timeoutMS *int64) (time.Duration, error) {
	if timeoutMS == nil {
		return defaultGracefulStopTimeout, nil
	}
	if *timeoutMS < 1 || *timeoutMS > int64(time.Hour/time.Millisecond) {
		return 0, errors.New("graceful_timeout_ms must be between 1 and 3600000 for the systemd stop adapter")
	}
	seconds := (*timeoutMS + 999) / 1000
	return time.Duration(seconds) * time.Second, nil
}

func stopProxyDrainOutcome(networkProfile string, requested bool) string {
	if !requested {
		return "not_requested"
	}
	if networkProfile == "" {
		return "not_configured"
	}
	return "unsupported"
}

type stopUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type gameSaveObservation struct {
	Target struct {
		ServerID      string          `json:"server_id"`
		EnvironmentID string          `json:"environment_id"`
		InstanceRef   json.RawMessage `json:"instance_ref"`
	} `json:"target"`
	ReleaseID  string `json:"release_id"`
	Outcome    string `json:"outcome"`
	ObservedAt string `json:"observed_at"`
	SavedAt    string `json:"saved_at"`
}

type executionUnitStopResult struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}
