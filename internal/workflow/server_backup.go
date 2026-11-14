package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
	"time"
)

const (
	backupInspectInstanceStep = "inspect-instance"
	backupInspectRuntimeStep  = "inspect-runtime"
	backupDescribeDataStep    = "describe-data"
	backupSaveGameStep        = "save-game-data"
	backupStopRuntimeStep     = "stop-runtime"
	backupPrepareStep         = "prepare-backup"
	backupValidateStep        = "validate-backup"
	backupStartRuntimeStep    = "start-runtime"
	backupVerifyRuntimeStep   = "verify-runtime"
	backupVerifyStoppedStep   = "verify-stopped"
	backupReadinessStep       = "check-readiness"
	backupManifestVersion     = "2"
	backupGracePeriodSeconds  = int64(120)
)

// ServerBackupDataSet is the trusted release-catalog declaration for one
// persistent data set. The transfer limit is controller policy, not adapter
// output, and is used to bound the capture stream before its size is known.
type ServerBackupDataSet struct {
	DataSetID                 string   `json:"data_set_id"`
	RelativePaths             []string `json:"relative_paths"`
	Required                  bool     `json:"required"`
	SupportedConsistencyModes []string `json:"supported_consistency_modes"`
	DataFormat                string   `json:"data_format,omitempty"`
	MaximumBytes              int64    `json:"maximum_bytes"`
}

// ServerBackupProfile contains immutable trusted release and storage metadata.
// The resolver must return the same profile for a profile ID throughout an
// accepted operation's lifetime.
type ServerBackupProfile struct {
	ProfileID               string
	GameReleaseID           string
	GameName                string
	GameVersion             string
	WorldFormat             string
	ConsistencyMode         string
	DataSets                []ServerBackupDataSet
	ValidationChecks        []ReadinessRequirement
	StorageAdapterID        string
	StorageProfileID        string
	StorageContainer        string
	UnitRef                 json.RawMessage
	ReadinessCheckSetID     string
	RequiredReadinessChecks []ReadinessRequirement
}

// ServerBackupProfileResolver selects immutable release, validation, and
// storage policy using a controller-approved profile ID.
type ServerBackupProfileResolver interface {
	ResolveServerBackup(context.Context, store.LogicalServer, string) (ServerBackupProfile, error)
}

// ServerBackupTransferBroker issues the transport-bound handles and verifies
// completed streams independently. Its staging area must be operation/step/
// attempt scoped and must never persist bearer references.
type ServerBackupTransferBroker interface {
	IssueStepStreamTransfers(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation, string) ([]adapter.StreamTransfer, error)
	VerifyStepStreams(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation) error
}

type serverBackupTransferCleaner interface {
	CleanupOperation(string) error
}

type ServerBackupStreamExpectation struct {
	DataSetID    string
	SizeBytes    int64
	MaximumBytes int64
	SHA256       string
}

// ServerBackupWorkflow plans and advances an offline, exact-generation backup.
// Network-routed servers are rejected until proxy drain is part of this flow.
type ServerBackupWorkflow struct {
	servers      store.LifecycleOperationStore
	adapters     adapterRuntime
	schemas      schemaRuntime
	profiles     ServerBackupProfileResolver
	transfers    ServerBackupTransferBroker
	readinessTTL time.Duration
}

func NewServerBackupWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerBackupProfileResolver, transfers ServerBackupTransferBroker, readinessTTL time.Duration) (*ServerBackupWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil || transfers == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, trusted backup-profile resolver, and stream broker are required")
	}
	if readinessTTL <= 0 || readinessTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerBackupWorkflow{
		servers: servers, adapters: adapters, schemas: schemas, profiles: profiles,
		transfers: transfers, readinessTTL: readinessTTL,
	}, nil
}

func asStringMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok && result != nil {
		return result
	}
	return map[string]any{}
}

func asAnySlice(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringValueMap(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func integerFromAny(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
