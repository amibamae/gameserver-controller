package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// BackupObjectReadback is produced by a trusted off-instance storage reader.
// The reader must fetch and hash the full exact object, not trust an ETag or a
// caller-controlled manifest. Its bytes must never be returned to the caller.
type BackupObjectReadback struct {
	Remote    bool
	SizeBytes int64
	SHA256    string
}

// BackupObjectReader independently rechecks every object before destruction.
// Implementations must fetch exact-version objects from the configured remote
// storage provider, compare complete content, and reject unknown outcomes.
type BackupObjectReader interface {
	Readback(context.Context, json.RawMessage) (BackupObjectReadback, error)
}

// backupOperationReader reads an immutable, persisted server.backup outcome.
type backupOperationReader interface {
	GetOperation(string) (store.StoredOperation, error)
}

// StoredBackupResolver derives destroy approval evidence from a successful
// durable offline backup, a final stopped lifecycle boundary and fresh remote
// object readbacks. The caller still requires independent destroy authorization.
type StoredBackupResolver struct {
	operations backupOperationReader
	objects    BackupObjectReader
	schemas    schemaRuntime
}

func NewStoredBackupResolver(operations backupOperationReader, objects BackupObjectReader, schemas schemaRuntime) (*StoredBackupResolver, error) {
	if operations == nil || objects == nil || schemas == nil {
		return nil, errors.New("backup operation store, remote object reader and schemas are required")
	}
	return &StoredBackupResolver{operations: operations, objects: objects, schemas: schemas}, nil
}

func (r *StoredBackupResolver) ResolveVerifiedServerBackup(ctx context.Context, server store.LogicalServer, profile, backupID string) (VerifiedServerBackup, error) {
	if ctx == nil || profile == "" || backupID == "" {
		return VerifiedServerBackup{}, errors.New("context, storage profile and backup ID are required")
	}
	if err := ctx.Err(); err != nil {
		return VerifiedServerBackup{}, err
	}
	stored, err := r.operations.GetOperation(backupID)
	if err != nil {
		return VerifiedServerBackup{}, fmt.Errorf("load accepted backup operation: %w", err)
	}
	operation := stored.Record
	if !offlineBackupBindsCurrentServer(operation, server, profile, r.schemas) {
		return VerifiedServerBackup{}, errors.New("backup is not a successful offline final recovery point for this instance")
	}
	manifest, err := decodeStoredBackupManifest(operation)
	if err != nil {
		return VerifiedServerBackup{}, err
	}
	if manifest.Consistency.Mode != "offline" || manifest.Consistency.Evidence == "" || len(manifest.Objects) == 0 {
		return VerifiedServerBackup{}, errors.New("backup does not establish verified offline consistency")
	}
	for _, object := range manifest.Objects {
		if err := r.recheckBackupObject(ctx, server, profile, backupID, object); err != nil {
			return VerifiedServerBackup{}, err
		}
	}
	return VerifiedServerBackup{
		BackupID: backupID, ServerID: server.ServerID, EnvironmentID: server.EnvironmentID,
		StorageProfile: profile, DataGenerationID: server.DataGenerationID,
		SourceInstanceRef: append(json.RawMessage(nil), server.InstanceRef...),
		IntegrityVerified: true, OffInstanceVerified: true, ConsistencyVerified: true,
		FinalWriteBoundaryVerified: true,
	}, nil
}

func offlineBackupBindsCurrentServer(operation store.OperationRecord, server store.LogicalServer, profile string, schemas schemaRuntime) bool {
	if operation.OperationType != "server.backup" || operation.Status != protocolstate.OperationSucceeded || operation.CompletedAt == nil {
		return false
	}
	if operation.ProtocolVersion != schemas.ProtocolVersion() || !equalJSON(operation.Target.InstanceRef, server.InstanceRef) {
		return false
	}
	if operation.Target.ServerID != server.ServerID || operation.Target.EnvironmentID != server.EnvironmentID || server.StorageProfile != profile {
		return false
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID == "" || backupResumeAfterBackup(operation) {
		return false
	}
	if !finalBackupLifecycleBoundary(server, operation.OperationID) {
		return false
	}
	var request struct {
		BackupProfile string `json:"backup_profile"`
	}
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || request.BackupProfile != profile {
		return false
	}
	if operation.BackupInitialLifecycleState != protocolstate.ServerStopped && !gameBackupSaveConfirmed(operation) {
		return false
	}
	if !backupEvidenceStepSucceeded(operationStep(operation, backupValidateStep)) {
		return false
	}
	return hasValidBackupResult(operation.Result, operation, server, schemas, protocolstate.ServerStopped)
}

// finalBackupLifecycleBoundary rejects a backup followed by ANY managed
// operation except the destruction of that same stopped source instance.
// The current generation alone cannot prove the last writes were captured.
func finalBackupLifecycleBoundary(server store.LogicalServer, backupID string) bool {
	if server.LifecycleState != protocolstate.ServerStopped && server.LifecycleState != protocolstate.ServerDestroying {
		return false
	}
	events := server.LifecycleEvents
	if len(events) == 0 {
		return false
	}
	boundary := -1
	for index, event := range events {
		if event.OperationID == backupID && event.OperationType == "server.backup" && event.ToState == protocolstate.ServerStopped {
			boundary = index
		}
	}
	if boundary < 0 {
		return false
	}
	for _, event := range events[boundary+1:] {
		if event.OperationType != "server.destroy" || event.ToState != protocolstate.ServerDestroying {
			return false
		}
	}
	if server.LifecycleState == protocolstate.ServerStopped {
		return server.LastOperationID == backupID
	}
	return server.LastOperationID != backupID
}

type storedBackupManifest struct {
	Consistency struct {
		Mode     string `json:"mode"`
		Evidence string `json:"evidence"`
	} `json:"consistency"`
	Objects []storedBackupObject `json:"objects"`
}

type storedBackupObject struct {
	ObjectRef         json.RawMessage `json:"object_ref"`
	SizeBytes         int64           `json:"size_bytes"`
	ChecksumAlgorithm string          `json:"checksum_algorithm"`
	Checksum          string          `json:"checksum"`
}

func decodeStoredBackupManifest(operation store.OperationRecord) (storedBackupManifest, error) {
	var result struct {
		OperationData struct {
			Manifest json.RawMessage `json:"manifest"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(operation.Result, &result) != nil {
		return storedBackupManifest{}, errors.New("decode verified backup result")
	}
	var manifest storedBackupManifest
	if json.Unmarshal(result.OperationData.Manifest, &manifest) != nil {
		return storedBackupManifest{}, errors.New("decode verified backup manifest")
	}
	return manifest, nil
}

func (r *StoredBackupResolver) recheckBackupObject(ctx context.Context, server store.LogicalServer, profile, backupID string, object storedBackupObject) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if object.ChecksumAlgorithm != "sha256" || len(object.Checksum) != 64 || object.SizeBytes < 0 {
		return errors.New("backup object has incomplete size or SHA-256 evidence")
	}
	var ref struct {
		AdapterID       string `json:"adapter_id"`
		StorageProfileID string `json:"storage_profile_id"`
		EnvironmentID   string `json:"environment_id"`
		Container       string `json:"container"`
		Key             string `json:"key"`
	}
	if json.Unmarshal(object.ObjectRef, &ref) != nil || !backupObjectIsRemoteAndUnique(ref.AdapterID, ref.StorageProfileID, ref.EnvironmentID, ref.Container, ref.Key, server, profile, backupID) {
		return errors.New("backup object reference is not in the remote per-backup namespace")
	}
	reading, err := r.objects.Readback(ctx, object.ObjectRef)
	if err != nil {
		return fmt.Errorf("remote backup object readback failed: %w", err)
	}
	if !reading.Remote || reading.SizeBytes != object.SizeBytes || !strings.EqualFold(reading.SHA256, object.Checksum) {
		return errors.New("remote backup object failed size or full SHA-256 readback verification")
	}
	return nil
}

func backupObjectIsRemoteAndUnique(adapterID, storageProfile, environment, container, key string, server store.LogicalServer, profile, backupID string) bool {
	if adapterID == "" || storageProfile != profile || environment != server.EnvironmentID || container == "" {
		return false
	}
	prefix := url.PathEscape(server.ServerID) + "/" + url.PathEscape(backupID) + "/"
	return strings.HasPrefix(key, prefix) && len(key) > len(prefix)
}
