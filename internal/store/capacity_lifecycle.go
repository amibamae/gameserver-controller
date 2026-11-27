package store

import (
	"encoding/json"
	"errors"
	"time"

	bolt "go.etcd.io/bbolt"
)

// recordCapacityLifecycleOutcomeTx ties boot samples and confirmed destruction
// releases to the corresponding committed lifecycle result. Missing
// reservations and already-recorded first boots are normal for unmanaged
// servers and subsequent starts.
func recordCapacityLifecycleOutcomeTx(tx *bolt.Tx, operation OperationRecord, server LogicalServer) error {
	if operation.OperationType == "server.destroy" && operation.Status == "succeeded" && server.LifecycleState == "destroyed" {
		return recordDestroyedCapacityTx(tx, operation, server)
	}
	if operation.OperationType != "server.start" || operation.Status != "succeeded" || server.LifecycleState != "ready" {
		return nil
	}
	return recordStartedCapacityTx(tx, operation, server)
}

// recordStartedCapacityTx records the first boot sample of a successful
// server.start. A start without an exact readiness assertion, or one whose
// reservation is missing, already measured, or not active, records nothing.
func recordStartedCapacityTx(tx *bolt.Tx, operation OperationRecord, server LogicalServer) error {
	instance, observedAt, ok := startedCapacityReadiness(operation, server)
	if !ok {
		return nil
	}
	_, err := recordCapacityReadyTx(tx, capacityInstanceQuery{
		ServerID: server.ServerID, EnvironmentID: server.EnvironmentID, ProviderInstanceID: instance.ExternalID, AdapterID: instance.AdapterID,
	}, observedAt)
	if errors.Is(err, ErrCapacityNotFound) || errors.Is(err, ErrCapacityConflict) || errors.Is(err, ErrCapacityTransition) {
		return nil
	}
	return err
}

// startedCapacityInstance is the provider instance a ready server runs on.
type startedCapacityInstance struct {
	ExternalID string `json:"external_id"`
	AdapterID  string `json:"adapter_id"`
}

// startedCapacityReadiness returns the server's provider instance and the
// readiness time asserted by the server.start result, and false unless the
// assertion is for exactly this server, environment and instance.
func startedCapacityReadiness(operation OperationRecord, server LogicalServer) (startedCapacityInstance, time.Time, bool) {
	assertion, ok := decodeStartedReadinessAssertion(operation.Result)
	if !ok || !assertion.isFor(server) {
		return startedCapacityInstance{}, time.Time{}, false
	}
	instance, ok := decodeStartedCapacityInstance(server.InstanceRef)
	if !ok {
		return startedCapacityInstance{}, time.Time{}, false
	}
	return instance, assertion.ObservedAt, true
}

// startedReadinessAssertion is the readiness assertion in a server.start
// result.
type startedReadinessAssertion struct {
	ServerID      string          `json:"server_id"`
	EnvironmentID string          `json:"environment_id"`
	InstanceRef   json.RawMessage `json:"instance_ref"`
	ObservedAt    time.Time       `json:"observed_at"`
}

// decodeStartedReadinessAssertion returns the readiness assertion in a
// server.start result, and false when the result does not decode.
func decodeStartedReadinessAssertion(result json.RawMessage) (startedReadinessAssertion, bool) {
	var decoded struct {
		Data struct {
			Assertion startedReadinessAssertion `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(result, &decoded) != nil {
		return startedReadinessAssertion{}, false
	}
	return decoded.Data.Assertion, true
}

// isFor reports whether the assertion is timestamped and for exactly the
// server's identity and provider instance.
func (a startedReadinessAssertion) isFor(server LogicalServer) bool {
	return a.ServerID == server.ServerID && a.EnvironmentID == server.EnvironmentID &&
		equalCanonicalJSON(a.InstanceRef, server.InstanceRef) && !a.ObservedAt.IsZero()
}

// decodeStartedCapacityInstance returns the provider instance a server's
// instance reference names, and false unless it names both the instance
// and its adapter.
func decodeStartedCapacityInstance(ref json.RawMessage) (startedCapacityInstance, bool) {
	var instance startedCapacityInstance
	if json.Unmarshal(ref, &instance) != nil || instance.ExternalID == "" || instance.AdapterID == "" {
		return startedCapacityInstance{}, false
	}
	return instance, true
}
