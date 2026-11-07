package store

// OperationStore is the durable repository used by controller application
// services. Create must complete before a worker dispatches any adapter call.
type OperationStore interface {
	CreateOperation(record OperationRecord) (StoredOperation, error)
	GetOperation(operationID string) (StoredOperation, error)
	UpdateOperation(expectedRevision uint64, record OperationRecord) (StoredOperation, error)
	ListRecoverableOperations() ([]StoredOperation, error)
	Close() error
}
