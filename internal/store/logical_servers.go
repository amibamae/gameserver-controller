package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gowebpki/jcs"
	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

var (
	ErrLogicalServerNotFound       = errors.New("logical server not found")
	ErrLogicalServerAlreadyExists  = errors.New("logical server already exists")
	ErrLogicalServerTargetMismatch = errors.New("operation target does not match the current logical-server instance")
)

// LogicalServer is the durable controller-owned identity and immutable
// configuration for one server/environment pair. Runtime state is kept here
// separately from provider and execution observations.
type LogicalServer struct {
	ServerID             string                    `json:"server_id"`
	EnvironmentID        string                    `json:"environment_id"`
	DisplayName          string                    `json:"display_name"`
	HostingProfile       string                    `json:"hosting_profile"`
	StorageProfile       string                    `json:"storage_profile"`
	GameAdapterID        string                    `json:"game_adapter_id"`
	ReleaseChannel       string                    `json:"release_channel"`
	ExecutionAdapterID   string                    `json:"execution_adapter_id"`
	ExecutionProfile     string                    `json:"execution_profile"`
	NetworkProfile       string                    `json:"network_profile,omitempty"`
	Definition           json.RawMessage           `json:"definition"`
	LifecycleState       protocolstate.ServerState `json:"lifecycle_state"`
	DesiredState         string                    `json:"desired_state,omitempty"`
	DataGenerationID     string                    `json:"data_generation_id,omitempty"`
	DataGenerationStatus string                    `json:"data_generation_status"`
	InstanceRef          json.RawMessage           `json:"instance_ref,omitempty"`
	LastOperationID      string                    `json:"last_operation_id"`
	LifecycleEvents      []LogicalServerStateEvent `json:"lifecycle_events,omitempty"`
	CreatedAt            time.Time                 `json:"created_at"`
	UpdatedAt            time.Time                 `json:"updated_at"`
}

// LogicalServerStateEvent records a controller-authorized lifecycle change.
// EvidenceStepIDs refer to immutable outputs on the durable operation record.
type LogicalServerStateEvent struct {
	Sequence           uint64                    `json:"sequence"`
	OperationID        string                    `json:"operation_id"`
	ActorID            string                    `json:"actor_id"`
	OperationType      string                    `json:"operation_type"`
	FromState          protocolstate.ServerState `json:"from_state"`
	ToState            protocolstate.ServerState `json:"to_state"`
	Cause              string                    `json:"cause"`
	RequestFingerprint string                    `json:"request_fingerprint"`
	EvidenceStepIDs    []string                  `json:"evidence_step_ids"`
	OccurredAt         time.Time                 `json:"occurred_at"`
}

type StoredLogicalServer struct {
	Record   LogicalServer `json:"record"`
	Revision uint64        `json:"revision"`
}

// LogicalServerRepository exposes read access to durable server records
// without coupling operation workers to the server catalog.
type LogicalServerRepository interface {
	GetLogicalServer(environmentID, serverID string) (StoredLogicalServer, error)
	ListLogicalServers() ([]StoredLogicalServer, error)
}

// LifecycleOperationStore commits an operation update and its corresponding
// logical-server transition in one durable transaction.
type LifecycleOperationStore interface {
	OperationStore
	LogicalServerRepository
	UpdateOperationAndLogicalServer(expectedOperationRevision uint64, operation OperationRecord, expectedServerRevision uint64, server LogicalServer) (StoredOperation, StoredLogicalServer, error)
}

type serverDefinitionInput struct {
	ServerID       string `json:"server_id"`
	EnvironmentID  string `json:"environment_id"`
	DisplayName    string `json:"display_name"`
	HostingProfile string `json:"hosting_profile"`
	StorageProfile string `json:"storage_profile"`
	Game           struct {
		AdapterID      string `json:"adapter_id"`
		ReleaseChannel string `json:"release_channel"`
	} `json:"game"`
	Execution struct {
		AdapterID string `json:"adapter_id"`
		Profile   string `json:"profile"`
	} `json:"execution"`
	NetworkProfile string `json:"network_profile,omitempty"`
}

func newLogicalServerFromDefinition(operation OperationRecord) (LogicalServer, error) {
	var input serverDefinitionInput
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &input); err != nil {
		return LogicalServer{}, fmt.Errorf("%w: server.define parameters are invalid", ErrInvalidRecord)
	}
	if input.ServerID != operation.Target.ServerID || input.EnvironmentID != operation.Target.EnvironmentID {
		return LogicalServer{}, fmt.Errorf("%w: server.define target does not match its parameters", ErrInvalidRecord)
	}
	record := LogicalServer{
		ServerID: input.ServerID, EnvironmentID: input.EnvironmentID,
		DisplayName: input.DisplayName, HostingProfile: input.HostingProfile,
		StorageProfile: input.StorageProfile, GameAdapterID: input.Game.AdapterID,
		ReleaseChannel: input.Game.ReleaseChannel, ExecutionAdapterID: input.Execution.AdapterID,
		ExecutionProfile: input.Execution.Profile, NetworkProfile: input.NetworkProfile,
		Definition:     append(json.RawMessage(nil), operation.RequestSnapshot.Parameters...),
		LifecycleState: protocolstate.ServerUnprovisioned, DataGenerationStatus: "unknown",
		LastOperationID: operation.OperationID,
	}
	return record, nil
}

func validateLogicalServer(record LogicalServer) error {
	if err := validatePersistedLogicalServer(record); err != nil {
		return err
	}
	if record.LifecycleState != protocolstate.ServerUnprovisioned || record.DataGenerationStatus != "unknown" ||
		record.CreatedAt.IsZero() || !record.UpdatedAt.Equal(record.CreatedAt) {
		return fmt.Errorf("%w: invalid initial logical server record", ErrInvalidRecord)
	}
	return nil
}

func sameLogicalServerIdentity(current, next LogicalServer) bool {
	return current.ServerID == next.ServerID && current.EnvironmentID == next.EnvironmentID &&
		current.DisplayName == next.DisplayName && current.HostingProfile == next.HostingProfile &&
		current.StorageProfile == next.StorageProfile && current.GameAdapterID == next.GameAdapterID &&
		current.ReleaseChannel == next.ReleaseChannel && current.ExecutionAdapterID == next.ExecutionAdapterID &&
		current.ExecutionProfile == next.ExecutionProfile && current.NetworkProfile == next.NetworkProfile &&
		bytes.Equal(current.Definition, next.Definition) && current.CreatedAt.Equal(next.CreatedAt)
}

func equalCanonicalJSON(left, right json.RawMessage) bool {
	if !isJSONObject(left) || !isJSONObject(right) {
		return false
	}
	canonicalLeft, leftErr := jcs.Transform(left)
	canonicalRight, rightErr := jcs.Transform(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(canonicalLeft, canonicalRight)
}

func validatePersistedLogicalServer(record LogicalServer) error {
	if !logicalServerFieldsValid(record) {
		return fmt.Errorf("%w: invalid logical server record", ErrInvalidRecord)
	}
	if !knownServerState(record.LifecycleState) {
		return fmt.Errorf("%w: invalid logical server lifecycle state", ErrInvalidRecord)
	}
	if record.DesiredState != "" && record.DesiredState != "running" && record.DesiredState != "stopped" && record.DesiredState != "destroyed" {
		return fmt.Errorf("%w: invalid logical server desired state", ErrInvalidRecord)
	}
	if record.DataGenerationStatus != "known" && record.DataGenerationStatus != "unknown" {
		return fmt.Errorf("%w: invalid logical server data generation status", ErrInvalidRecord)
	}
	if record.DataGenerationStatus == "known" && !validIdentifier(record.DataGenerationID, 200) {
		return fmt.Errorf("%w: known data generation is missing its ID", ErrInvalidRecord)
	}
	if len(record.InstanceRef) != 0 && !isJSONObject(record.InstanceRef) {
		return fmt.Errorf("%w: invalid logical server instance reference", ErrInvalidRecord)
	}
	return validateLogicalServerStateEvents(record)
}

// logicalServerFieldsValid requires every identity and profile field to be a
// bounded identifier, the network profile name to be bounded, the definition
// to be a JSON object, and the record to be created no later than updated.
func logicalServerFieldsValid(record LogicalServer) bool {
	for _, field := range []struct {
		value   string
		maximum int
	}{
		{record.ServerID, 200}, {record.EnvironmentID, 200}, {record.DisplayName, 200},
		{record.HostingProfile, 200}, {record.StorageProfile, 200}, {record.GameAdapterID, 160},
		{record.ReleaseChannel, 100}, {record.ExecutionAdapterID, 160}, {record.ExecutionProfile, 200},
		{record.LastOperationID, 200},
	} {
		if !validIdentifier(field.value, field.maximum) {
			return false
		}
	}
	return len(record.NetworkProfile) <= 200 && isJSONObject(record.Definition) &&
		!record.CreatedAt.IsZero() && !record.UpdatedAt.Before(record.CreatedAt)
}

// knownServerState reports whether state is one of the protocol's server
// lifecycle states.
func knownServerState(state protocolstate.ServerState) bool {
	switch state {
	case protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing,
		protocolstate.ServerRestoring, protocolstate.ServerStarting, protocolstate.ServerValidating,
		protocolstate.ServerReady, protocolstate.ServerDegraded, protocolstate.ServerDraining,
		protocolstate.ServerStopping, protocolstate.ServerBackingUp, protocolstate.ServerStopped,
		protocolstate.ServerDestroying, protocolstate.ServerDestroyed, protocolstate.ServerFailed:
		return true
	}
	return false
}

func validateLogicalServerStateEvents(record LogicalServer) error {
	if len(record.LifecycleEvents) == 0 {
		if record.LifecycleState != protocolstate.ServerUnprovisioned {
			return fmt.Errorf("%w: lifecycle state changed without a durable state event", ErrInvalidRecord)
		}
		return nil
	}
	state := protocolstate.ServerUnprovisioned
	previousAt := record.CreatedAt
	for i, event := range record.LifecycleEvents {
		if event.Sequence != uint64(i+1) || !validIdentifier(event.OperationID, 200) || event.ActorID == "" || len(event.ActorID) > 200 ||
			!validQualifiedName(event.OperationType, 160) || event.Cause == "" || len(event.Cause) > 1000 || !validFingerprint(event.RequestFingerprint) ||
			event.FromState != state || event.OccurredAt.Before(previousAt) || event.OccurredAt.After(record.UpdatedAt) {
			return fmt.Errorf("%w: invalid logical server lifecycle event %d", ErrInvalidRecord, i+1)
		}
		if err := protocolstate.ValidateServerTransition(event.FromState, event.ToState); err != nil {
			return fmt.Errorf("%w: invalid logical server lifecycle event %d transition", ErrInvalidRecord, i+1)
		}
		seen := make(map[string]struct{}, len(event.EvidenceStepIDs))
		for _, stepID := range event.EvidenceStepIDs {
			if !validIdentifier(stepID, 200) {
				return fmt.Errorf("%w: invalid lifecycle evidence step ID", ErrInvalidRecord)
			}
			if _, duplicate := seen[stepID]; duplicate {
				return fmt.Errorf("%w: duplicate lifecycle evidence step ID", ErrInvalidRecord)
			}
			seen[stepID] = struct{}{}
		}
		state = event.ToState
		previousAt = event.OccurredAt
	}
	if state != record.LifecycleState {
		return fmt.Errorf("%w: lifecycle event history does not match current state", ErrInvalidRecord)
	}
	if record.LifecycleEvents[len(record.LifecycleEvents)-1].OperationID != record.LastOperationID {
		return fmt.Errorf("%w: last operation does not match lifecycle history", ErrInvalidRecord)
	}
	return nil
}

func logicalServerKey(environmentID, serverID string) []byte {
	return []byte(strconv.Itoa(len(environmentID)) + ":" + environmentID + serverID)
}

// GetLogicalServer returns the durable logical server identity.
func (s *BoltStore) GetLogicalServer(environmentID, serverID string) (StoredLogicalServer, error) {
	if environmentID == "" || serverID == "" {
		return StoredLogicalServer{}, ErrLogicalServerNotFound
	}
	var result StoredLogicalServer
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(logicalServersBucket).Get(logicalServerKey(environmentID, serverID))
		if value == nil {
			return ErrLogicalServerNotFound
		}
		if err := json.Unmarshal(value, &result); err != nil {
			return fmt.Errorf("%w: decode logical server: %v", ErrCorruptStore, err)
		}
		if result.Revision == 0 || result.Record.EnvironmentID != environmentID || result.Record.ServerID != serverID {
			return fmt.Errorf("%w: logical server key or revision mismatch", ErrCorruptStore)
		}
		if err := validatePersistedLogicalServer(result.Record); err != nil {
			return fmt.Errorf("%w: invalid logical server record", ErrCorruptStore)
		}
		return nil
	})
	if err != nil {
		return StoredLogicalServer{}, err
	}
	return result, nil
}

// ListLogicalServers returns records in stable environment/server key order.
func (s *BoltStore) ListLogicalServers() ([]StoredLogicalServer, error) {
	var result []StoredLogicalServer
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(logicalServersBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			var stored StoredLogicalServer
			if err := json.Unmarshal(value, &stored); err != nil {
				return fmt.Errorf("%w: decode logical server: %v", ErrCorruptStore, err)
			}
			if stored.Revision == 0 || !bytes.Equal(key, logicalServerKey(stored.Record.EnvironmentID, stored.Record.ServerID)) {
				return fmt.Errorf("%w: logical server key or revision mismatch", ErrCorruptStore)
			}
			if err := validatePersistedLogicalServer(stored.Record); err != nil {
				return fmt.Errorf("%w: invalid logical server record", ErrCorruptStore)
			}
			result = append(result, stored)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func encodeLogicalServer(record LogicalServer) ([]byte, error) {
	if err := validateLogicalServer(record); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(StoredLogicalServer{Revision: 1, Record: record})
	if err != nil {
		return nil, fmt.Errorf("encode logical server record: %w", err)
	}
	return encoded, nil
}
