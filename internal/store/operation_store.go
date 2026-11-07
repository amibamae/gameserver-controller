package store

// OperationStore is the durable repository used by controller application
// services. Acceptance must complete before a worker dispatches any adapter call.
type OperationStore interface {
	AcceptOperation(record OperationRecord, proof IdempotencyProof, lockScopes []string) (StoredOperation, bool, error)
	GetOperation(operationID string) (StoredOperation, error)
	UpdateOperation(expectedRevision uint64, record OperationRecord) (StoredOperation, error)
	ListRecoverableOperations() ([]StoredOperation, error)
	GetResourceLock(scope string) (ResourceLock, error)
	ListResourceLocks() ([]ResourceLock, error)
	Close() error
}
