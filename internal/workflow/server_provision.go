package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
	"io"
	"time"
)

const (
	provisionCreateStep         = "create-instance"
	provisionWaitStep           = "wait-instance-active"
	provisionBootstrapStep      = "deliver-bootstrap"
	provisionBootstrapInspect   = "inspect-bootstrap"
	provisionInstallStep        = "install-game"
	provisionCompatibilityStep  = "check-restore-compatibility"
	provisionRestoreStep        = "restore-backup"
	provisionRestoreInspectStep = "inspect-restore"
	provisionCreateUnitStep     = "create-unit"
	provisionStartUnitStep      = "start-unit"
	provisionInspectUnitStep    = "inspect-unit"
	provisionReadinessStep      = "check-readiness"
)

// ServerProvisionProfile contains immutable deployment choices resolved from
// controller-owned catalogs. It is deliberately separate from request fields:
// a caller may select from a profile, but cannot supply adapter step inputs.
type ServerProvisionProfile struct {
	Provider                       string
	CapacityGroup                  string
	CapacityLimit                  int
	CapacityGroupLimit             int
	RegionID                       string
	PlanID                         string
	OS                             json.RawMessage
	Bootstrap                      json.RawMessage
	GameReleaseID                  string
	ArtifactURI                    string
	ArtifactChecksum               json.RawMessage
	UnitRef                        json.RawMessage
	ReadinessCheckSetID            string
	RequiredChecks                 []ReadinessRequirement
	DataGenerationID               string
	BackupStorageProfileID         string
	BackupStorageAdapterID         string
	BackupStorageVersioningEnabled bool
	GameReleaseDescription         json.RawMessage
	RequiredCompatibilityCheckIDs  []string
	RequiredRestoreCheckIDs        []string
}

// ServerProvisionProfileResolver resolves an allow-listed deployment using
// the persisted server definition and schema-validated request. Implementations
// must return controller-owned immutable data, not copy the request into a plan.
type ServerProvisionProfileResolver interface {
	ResolveServerProvision(context.Context, store.LogicalServer, json.RawMessage) (ServerProvisionProfile, error)
}

// ServerProvisionBackupResolver supplies only controller-verified backup
// records and exact manifest-object streams from an allow-listed storage profile.
type ServerProvisionBackupResolver interface {
	ResolveServerProvisionBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error)
	OpenServerProvisionBackupObject(context.Context, store.LogicalServer, string, json.RawMessage) (io.ReadCloser, error)
}

type VerifiedProvisionBackup struct {
	Result            json.RawMessage
	IntegrityVerified bool
}

// ProvisionCapacityStore is implemented by the durable store. Capacity is
// reserved and create is claimed immediately before the provider side effect.
type ProvisionCapacityStore interface {
	ReserveCapacity(store.CapacityReservation, int, int) (store.CapacityReservation, bool, error)
	MarkCapacityCreateSent(string) (store.CapacityReservation, error)
	ObserveCapacity(string, store.CapacityProviderObservation) (store.CapacityReservation, error)
	GetCapacityReservation(string) (store.CapacityReservation, error)
	RecordCapacityReady(string, string, string, time.Time) (store.CapacityReservation, error)
}

type ServerProvisionWorkflow struct {
	servers      store.LifecycleOperationStore
	capacity     ProvisionCapacityStore
	adapters     adapterRuntime
	schemas      schemaRuntime
	profiles     ServerProvisionProfileResolver
	backups      ServerProvisionBackupResolver
	assertionTTL time.Duration
}

func NewServerProvisionWorkflow(servers store.LifecycleOperationStore, capacity ProvisionCapacityStore,
	adapters adapterRuntime, schemas schemaRuntime, profiles ServerProvisionProfileResolver, assertionTTL time.Duration, backupResolvers ...ServerProvisionBackupResolver) (*ServerProvisionWorkflow, error) {
	if servers == nil || capacity == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, capacity store, adapter registry, schemas, and trusted provision-profile resolver are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	if len(backupResolvers) > 1 {
		return nil, errors.New("at most one trusted server provision backup resolver may be configured")
	}
	var backups ServerProvisionBackupResolver
	if len(backupResolvers) == 1 {
		backups = backupResolvers[0]
	}
	return &ServerProvisionWorkflow{servers: servers, capacity: capacity, adapters: adapters, schemas: schemas, profiles: profiles, backups: backups, assertionTTL: assertionTTL}, nil
}

type provisionRequest struct {
	ServerID       string          `json:"server_id"`
	EnvironmentID  string          `json:"environment_id"`
	HostingAdapter string          `json:"hosting_adapter_id"`
	RegionID       string          `json:"region_id"`
	PlanID         string          `json:"plan_id"`
	OS             json.RawMessage `json:"os"`
	Bootstrap      json.RawMessage `json:"bootstrap"`
	Game           struct {
		AdapterID string `json:"adapter_id"`
		Release   string `json:"release_ref"`
	} `json:"game"`
	Execution struct {
		AdapterID string `json:"adapter_id"`
		Profile   string `json:"profile"`
	} `json:"execution"`
	SecretRefs     []store.SecretReference `json:"secret_refs,omitempty"`
	NetworkProfile string                  `json:"network_profile,omitempty"`
	FirewallPolicy string                  `json:"firewall_policy_ref,omitempty"`
	SSHKeyRefs     []string                `json:"ssh_key_refs,omitempty"`
	Labels         map[string]string       `json:"labels,omitempty"`
	RestoreBackup  string                  `json:"restore_backup_id,omitempty"`
}

type provisionCreateOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	State                string          `json:"state"`
	ObservedAt           string          `json:"observed_at"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref,omitempty"`
}

type provisionWaitOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref,omitempty"`
	OperationStatus      string          `json:"operation_status"`
	InstanceState        string          `json:"instance_state"`
	ObservedAt           string          `json:"observed_at"`
}

type provisionBootstrapOutput struct {
	InstanceRef           json.RawMessage `json:"instance_ref"`
	BootstrapExecutionRef json.RawMessage `json:"bootstrap_execution_ref"`
	Revision              string          `json:"revision"`
	Digest                string          `json:"digest"`
	State                 string          `json:"state"`
	ObservedAt            string          `json:"observed_at"`
}

type provisionBootstrapInspection struct {
	InstanceRef           json.RawMessage `json:"instance_ref"`
	BootstrapExecutionRef json.RawMessage `json:"bootstrap_execution_ref"`
	Revision              string          `json:"revision"`
	Digest                string          `json:"digest"`
	State                 string          `json:"state"`
	ObservedAt            string          `json:"observed_at"`
}

type provisionUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

// BuildPlan resolves and pins all adapter identities, capability versions,
// inputs, and cross-step resource bindings before any provider call.
type provisionRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// provisionRefNamed reports whether the reference names a kind and resource.
func provisionRefNamed(ref provisionRef) bool {
	return ref.ResourceKind != "" && ref.ExternalID != ""
}

// provisionRefIs reports whether the reference names a resource of the
// given adapter, environment and kind.
func provisionRefIs(ref provisionRef, adapterID, environmentID, kind string) bool {
	return ref.AdapterID == adapterID && ref.EnvironmentID == environmentID && ref.ResourceKind == kind
}

func provisionResourceKey(operationID, fingerprint string) string {
	digest := sha256.Sum256([]byte("server.provision\x00" + operationID + "\x00" + fingerprint))
	return "gsc-" + hex.EncodeToString(digest[:])
}

func (w *ServerProvisionWorkflow) BeforeAdapterDispatch(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if step.StepID != provisionCreateStep {
		return w.observeCreatedCapacity(operation)
	}
	var input struct {
		ResourceKey string          `json:"resource_key"`
		RegionID    string          `json:"region_id"`
		PlanID      string          `json:"plan_id"`
		OS          json.RawMessage `json:"os"`
	}
	if json.Unmarshal(step.Input, &input) != nil || input.ResourceKey == "" {
		return errors.New("persisted provider create step has invalid trusted input")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("persisted provision request cannot be decoded")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	profile, err := w.profiles.ResolveServerProvision(ctx, stored.Record, operation.RequestSnapshot.Parameters)
	if err != nil {
		return err
	}
	if err := validateProvisionProfile(stored.Record, request, profile); err != nil {
		return err
	}
	if input.ResourceKey != provisionResourceKey(operation.OperationID, operation.RequestFingerprint) {
		return errors.New("provider create resource key does not match the accepted operation")
	}
	reservation := store.CapacityReservation{ProviderAdapterID: request.HostingAdapter, OperationID: operation.OperationID,
		ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, CapacityGroup: profile.CapacityGroup,
		Provider: profile.Provider, ResourceKey: input.ResourceKey}
	if _, _, err := w.capacity.ReserveCapacity(reservation, profile.CapacityLimit, profile.CapacityGroupLimit); err != nil {
		return fmt.Errorf("reserve managed server capacity before provider create: %w", err)
	}
	if _, err := w.capacity.MarkCapacityCreateSent(operation.OperationID); err != nil {
		return fmt.Errorf("claim one-time provider create dispatch: %w", err)
	}
	return nil
}

// SecretReferencesForStep sends operation-scoped references only to the
// bootstrap delivery adapter, the sole provision step that may need external
// credentials. The operation record stores references, never values.
func (w *ServerProvisionWorkflow) SecretReferencesForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep) ([]adapter.SecretReference, error) {
	if ctx == nil || operation.OperationType != "server.provision" {
		return nil, errors.New("server.provision operation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if step.StepID != provisionBootstrapStep {
		return nil, nil
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || step.AdapterID != request.HostingAdapter || step.CapabilityName != "hosting.bootstrap.deliver" {
		return nil, errors.New("provision secret references are not bound to the pinned bootstrap delivery capability")
	}
	refs := request.SecretRefs
	if len(refs) > 0 && len(operation.RequestSnapshot.SecretRefs) > 0 {
		requestRefsJSON, _ := json.Marshal(refs)
		snapshotRefsJSON, _ := json.Marshal(operation.RequestSnapshot.SecretRefs)
		if !equalJSON(requestRefsJSON, snapshotRefsJSON) {
			return nil, errors.New("provision parameter and operation snapshot secret references differ")
		}
	}
	if len(refs) == 0 {
		refs = operation.RequestSnapshot.SecretRefs
	}
	result := make([]adapter.SecretReference, 0, len(refs))
	for _, ref := range refs {
		if ref.SecretRef == "" || ref.Purpose == "" {
			return nil, errors.New("persisted provision secret reference is incomplete")
		}
		result = append(result, adapter.SecretReference{SecretRef: ref.SecretRef, Purpose: ref.Purpose,
			PermissionScope: append([]string(nil), ref.PermissionScope...), VersionRef: ref.VersionRef})
	}
	return result, nil
}

func (w *ServerProvisionWorkflow) observeCreatedCapacity(operation store.OperationRecord) error {
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return nil
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("persisted provision request cannot be decoded")
	}
	reservation, err := w.capacity.GetCapacityReservation(operation.OperationID)
	if err != nil {
		return fmt.Errorf("load provider create capacity reservation: %w", err)
	}
	instance, ok := decodeProvisionRef(created.InstanceRef)
	if !ok || !provisionRefIs(instance, request.HostingAdapter, operation.Target.EnvironmentID, "instance") {
		return errors.New("provider create output does not identify the expected exact instance")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, created.ObservedAt)
	if err != nil {
		return errors.New("provider create output has an invalid observation time")
	}
	if reservation.State == store.CapacityCreateOutcomeUnknown {
		_, err = w.capacity.ObserveCapacity(operation.OperationID, store.CapacityProviderObservation{
			Provider: reservation.Provider, ResourceKey: reservation.ResourceKey,
			ProviderInstanceID: instance.ExternalID, State: "present", ObservedAt: observedAt,
			ProvisionedAt: observedAt,
		})
		if err != nil {
			return fmt.Errorf("record exact provider create observation: %w", err)
		}
	}
	return nil
}

func (w *ServerProvisionWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	if step.StepID == provisionCreateStep {
		var request provisionRequest
		if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) == nil && request.RestoreBackup != "" {
			if status := stepField(operationStep(operation, provisionCompatibilityStep).Output, "compatibility_status"); string(status) != `"compatible"` {
				return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.restore.incompatible", "safety_blocked",
					"The selected backup did not pass every required compatibility check; no provider instance was created.", operation.OperationID, false, "inspect_operation", false)}, nil
			}
		}
	}
	if step.StepID == provisionWaitStep && stepSucceeded(operation, provisionCreateStep) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	if step.StepID == provisionBootstrapStep && stepSucceeded(operation, provisionWaitStep) && !provisionWaitSucceeded(operation) {
		return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.safety.precondition_unmet", "safety_blocked",
			"The exact provider instance did not reach active state; no bootstrap or game changes were issued.", operation.OperationID, false, "inspect_operation", false)}, nil
	}
	return Resolution{State: ResolutionNeedsOperator}, nil
}

func (w *ServerProvisionWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.provision" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, false, errors.New("server.provision lifecycle projection target mismatch")
	}
	next := current.Record
	created, createErr := provisionCreateStepOutput(operation)
	switch current.Record.LifecycleState {
	case protocolstate.ServerUnprovisioned:
		if operation.Status != protocolstate.OperationPending {
			next.LifecycleState = protocolstate.ServerProvisioning
			return next, true, nil
		}
	case protocolstate.ServerProvisioning:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if createErr == nil {
			next.InstanceRef = append(json.RawMessage(nil), created.InstanceRef...)
			next.LifecycleState = protocolstate.ServerPreparing
			next.DataGenerationID = provisionDataGeneration(operation.OperationID)
			next.DataGenerationStatus = "unknown"
			return next, true, nil
		}
	case protocolstate.ServerPreparing:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if operationStep(operation, provisionStartUnitStep).Status != protocolstate.StepPending {
			if !provisionInstallSucceeded(operation) {
				return store.LogicalServer{}, false, errors.New("server.provision cannot start before the exact game release is installed")
			}
			next.LifecycleState = protocolstate.ServerStarting
			next.DataGenerationID = provisionDataGeneration(operation.OperationID)
			next.DataGenerationStatus = "known"
			return next, true, nil
		}
	case protocolstate.ServerStarting:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if operationStep(operation, provisionReadinessStep).Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerValidating
			return next, true, nil
		}
	case protocolstate.ServerValidating:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerDegraded
			return next, true, nil
		}
		if operation.Status == protocolstate.OperationSucceeded {
			if !hasValidProvisionResult(operation.Result, operation.Target, current.Record.InstanceRef, w.schemas) {
				return store.LogicalServer{}, false, errors.New("terminal server.provision result lacks exact instance and readiness evidence")
			}
			next.LifecycleState = protocolstate.ServerReady
			next.DesiredState = "running"
			return next, true, nil
		}
	}
	return current.Record, false, nil
}

func provisionDataGeneration(operationID string) string {
	digest := sha256.Sum256([]byte("server.provision.data-generation\x00" + operationID))
	return "gen-" + hex.EncodeToString(digest[:16])
}

var _ interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
	Definition
	LifecycleProjector
	DispatchGuard
	CompletionValidator
} = (*ServerProvisionWorkflow)(nil)
