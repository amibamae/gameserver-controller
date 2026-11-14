package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// AcceptOperation atomically deduplicates an accepted request, reserves its
// resource locks, and persists its initial operation record. The verifier
// proof must be derived from the authenticated actor and client key by the
// acceptance service.
func (s *BoltStore) AcceptOperation(record OperationRecord, proof IdempotencyProof, lockScopes []string) (StoredOperation, bool, error) {
	prepared, scopes, err := prepareAcceptance(record, proof, lockScopes)
	if err != nil {
		return StoredOperation{}, false, err
	}
	var accepted StoredOperation
	replayed := false
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := requireVerifierForEveryKeyVersion(tx, proof); err != nil {
			return err
		}
		existing, err := findReplayedOperation(tx, proof, prepared.ActorID)
		if err != nil {
			return err
		}
		if existing != nil {
			accepted = *existing
			replayed = true
			return nil
		}
		if tx.Bucket(operationsBucket).Get([]byte(prepared.OperationID)) != nil {
			return ErrAlreadyExists
		}
		logicalServer, err := acceptanceLogicalServer(tx, prepared)
		if err != nil {
			return err
		}
		if err := requireFreeLocks(tx, scopes); err != nil {
			return err
		}
		if err := writeAcceptedOperation(tx, prepared, logicalServer, proof, scopes); err != nil {
			return err
		}
		accepted = StoredOperation{Record: prepared, Revision: 1}
		return nil
	})
	if err != nil {
		return StoredOperation{}, false, err
	}
	return accepted, replayed, nil
}

// prepareAcceptance checks the request outside the transaction: it needs an
// idempotency key, a request fingerprint, a valid proof and lock scopes, and
// the logical-server lock when it is a server operation. It returns the
// record as it will be stored and the sorted scopes.
func prepareAcceptance(record OperationRecord, proof IdempotencyProof, lockScopes []string) (OperationRecord, []string, error) {
	if record.IdempotencyKey == "" || !validFingerprint(record.RequestFingerprint) {
		return OperationRecord{}, nil, fmt.Errorf("%w: acceptance requires an idempotency key and request fingerprint", ErrInvalidRecord)
	}
	if err := validateIdempotencyProof(proof); err != nil {
		return OperationRecord{}, nil, err
	}
	scopes, err := validateLockScopes(lockScopes)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if requiredScope, required := requiredLogicalServerLock(record); required && !containsString(scopes, requiredScope) {
		return OperationRecord{}, nil, fmt.Errorf("%w: standard server operation requires lock %q", ErrInvalidRecord, requiredScope)
	}
	prepared, err := prepareNewRecord(record, time.Now().UTC())
	if err != nil {
		return OperationRecord{}, nil, err
	}
	return prepared, scopes, nil
}

// requireVerifierForEveryKeyVersion fails unless the proof carries a verifier
// for every key version the store has recorded, so a replay under a rotated
// key is still found.
func requireVerifierForEveryKeyVersion(tx *bolt.Tx, proof IdempotencyProof) error {
	cursor := tx.Bucket(keyVersionsBucket).Cursor()
	for version, _ := cursor.First(); version != nil; version, _ = cursor.Next() {
		if _, ok := findVerifier(proof, string(version)); !ok {
			return fmt.Errorf("%w: %s", ErrKeyVersionMissing, version)
		}
	}
	return nil
}

// findReplayedOperation returns the operation an earlier identical request
// created, or nil when no verifier candidate is recorded. A recorded verifier
// for a different request is a conflict; one whose operation is gone has
// expired; and entries that disagree with their key version, actor, or each
// other mean the store is corrupt.
func findReplayedOperation(tx *bolt.Tx, proof IdempotencyProof, actorID string) (*StoredOperation, error) {
	operations := tx.Bucket(operationsBucket)
	idempotency := tx.Bucket(idempotencyBucket)
	keyVersions := tx.Bucket(keyVersionsBucket)
	var existing *StoredOperation
	for _, candidate := range proof.Candidates {
		value := idempotency.Get(verifierIndex(candidate))
		if value == nil {
			continue
		}
		entry, err := decodeIdempotencyEntry(value)
		if err != nil {
			return nil, err
		}
		if entry.KeyVersion != candidate.KeyVersion || keyVersions.Get([]byte(entry.KeyVersion)) == nil {
			return nil, fmt.Errorf("%w: idempotency entry key version mismatch", ErrCorruptStore)
		}
		if !requestMatches(entry, candidate) {
			return nil, ErrIdempotencyConflict
		}
		value = operations.Get([]byte(entry.OperationID))
		if value == nil {
			return nil, ErrIdempotencyExpired
		}
		stored, err := decodeEnvelope(value)
		if err != nil {
			return nil, err
		}
		if stored.Record.ActorID != actorID {
			return nil, fmt.Errorf("%w: idempotency verifier actor mismatch", ErrCorruptStore)
		}
		if existing != nil && existing.Record.OperationID != stored.Record.OperationID {
			return nil, fmt.Errorf("%w: verifier candidates identify multiple operations", ErrCorruptStore)
		}
		existing = &stored
	}
	return existing, nil
}

// acceptanceLogicalServer applies the logical-server rules for a new
// operation. server.define returns the server record to create, after
// checking that the definition holds no steps, secrets, or instance and that
// its server ID is unused in every environment. Any other server operation
// requires an existing, valid logical server and returns nil.
func acceptanceLogicalServer(tx *bolt.Tx, prepared OperationRecord) (*LogicalServer, error) {
	if prepared.OperationType == "server.define" {
		return definedLogicalServer(tx.Bucket(logicalServersBucket), prepared)
	}
	if !strings.HasPrefix(prepared.OperationType, "server.") {
		return nil, nil
	}
	server, err := readLogicalServerTx(tx, prepared.Target, "logical server key does not match its record")
	if err != nil {
		return nil, err
	}
	return nil, requireValidStoredServer(server.Record)
}

// definedLogicalServer returns the logical server a server.define creates.
func definedLogicalServer(servers *bolt.Bucket, prepared OperationRecord) (*LogicalServer, error) {
	if !logicalDefinitionOnly(prepared) {
		return nil, fmt.Errorf("%w: server.define must contain only a logical definition", ErrInvalidRecord)
	}
	server, err := newLogicalServerFromDefinition(prepared)
	if err != nil {
		return nil, err
	}
	if err := requireUnusedServerID(servers, server); err != nil {
		return nil, err
	}
	server.CreatedAt = prepared.CreatedAt
	server.UpdatedAt = prepared.CreatedAt
	server.LastOperationID = prepared.OperationID
	return &server, nil
}

func logicalDefinitionOnly(prepared OperationRecord) bool {
	return len(prepared.Steps) == 0 && len(prepared.RequestSnapshot.SecretRefs) == 0 && len(prepared.Target.InstanceRef) == 0
}

// requireUnusedServerID fails when the server's key is taken or when any
// environment already uses its server ID, because management routes after
// definition name only the server.
func requireUnusedServerID(servers *bolt.Bucket, server LogicalServer) error {
	if servers.Get(logicalServerKey(server.EnvironmentID, server.ServerID)) != nil {
		return ErrLogicalServerAlreadyExists
	}
	return servers.ForEach(func(_, value []byte) error {
		// A record that does not decode is skipped rather than reported
		// here: this check only looks for a taken ID, and the reads that
		// depend on that record report its corruption.
		var existing StoredLogicalServer
		if json.Unmarshal(value, &existing) == nil && existing.Record.ServerID == server.ServerID {
			return ErrLogicalServerAlreadyExists
		}
		return nil
	})
}

// writeAcceptedOperation stores the operation at revision 1, the logical
// server a definition creates, the active-key idempotency entry, the key
// version, and the forward and reverse lock indexes, in one transaction.
func writeAcceptedOperation(tx *bolt.Tx, prepared OperationRecord, logicalServer *LogicalServer, proof IdempotencyProof, scopes []string) error {
	if err := putOperation(tx, StoredOperation{Record: prepared, Revision: 1}); err != nil {
		return err
	}
	if err := putDefinedLogicalServer(tx, logicalServer); err != nil {
		return err
	}
	if err := putIdempotencyEntry(tx, prepared, proof); err != nil {
		return err
	}
	return putAcceptedLocks(tx, prepared, scopes)
}

// putDefinedLogicalServer stores the server a definition creates, if any.
func putDefinedLogicalServer(tx *bolt.Tx, logicalServer *LogicalServer) error {
	if logicalServer == nil {
		return nil
	}
	serverJSON, err := encodeLogicalServer(*logicalServer)
	if err != nil {
		return err
	}
	return tx.Bucket(logicalServersBucket).Put(logicalServerKey(logicalServer.EnvironmentID, logicalServer.ServerID), serverJSON)
}

// putIdempotencyEntry records the active-key verifier and its key version.
func putIdempotencyEntry(tx *bolt.Tx, prepared OperationRecord, proof IdempotencyProof) error {
	// The proof always has an active-key verifier here: prepareAcceptance
	// rejects a proof without one before the transaction starts.
	activeVerifier, _ := findVerifier(proof, proof.ActiveKeyVersion)
	entry := idempotencyEntry{
		OperationID:   prepared.OperationID,
		BindingDigest: activeVerifier.BindingDigest,
		KeyVersion:    proof.ActiveKeyVersion,
		RecordedAt:    prepared.CreatedAt,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode idempotency entry: %w", err)
	}
	if err := tx.Bucket(idempotencyBucket).Put(verifierIndex(activeVerifier), entryJSON); err != nil {
		return err
	}
	return tx.Bucket(keyVersionsBucket).Put([]byte(proof.ActiveKeyVersion), []byte{1})
}

// putAcceptedLocks writes the forward lock for every scope and the reverse
// index of the operation's scopes. An operation without scopes has neither.
func putAcceptedLocks(tx *bolt.Tx, prepared OperationRecord, scopes []string) error {
	if len(scopes) == 0 {
		return nil
	}
	locks := tx.Bucket(locksBucket)
	for _, scope := range scopes {
		lockJSON, err := json.Marshal(ResourceLock{Scope: scope, OperationID: prepared.OperationID, AcquiredAt: prepared.CreatedAt})
		if err != nil {
			return err
		}
		if err := locks.Put([]byte(scope), lockJSON); err != nil {
			return err
		}
	}
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return err
	}
	return tx.Bucket(operationLocksBucket).Put([]byte(prepared.OperationID), scopesJSON)
}
