package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	bolt "go.etcd.io/bbolt"
)

func TestValidateBackupOperationRecord(t *testing.T) {
	testCases := []struct {
		name           string
		recordType     string
		status         string
		wantValid      bool
		description    string
	}{
		{
			name: "valid-server-backup-not-terminal",
			recordType: "server.backup",
			status: protocolstate.OperationPending,
			wantValid: true,
			description: "Should accept a valid server.backup operation in pending state",
		},
		{
			name:         "wrong-operation-type",
			recordType:   "server.provision",
			status:       protocolstate.OperationPending,
			wantValid:    false,
			description:  "Should reject non-backup operations",
		},
		{
			name:           "terminal-operation-completed",
			recordType:     "server.backup",
			status:         protocolstate.OperationCompleted,
			wantValid:      false,
			description:    "Should reject terminal state operations",
		},
		{
			name:           "terminal-operation-failed",
			recordType:     "server.backup",
			status:         protocolstate.OperationFailed,
			wantValid:      false,
			description:    "Should reject failed operations",
		},
		{
			name:         "terminal-operation-cancelled",
			recordType:   "server.backup",
			status:       protocolstate.OperationCancelled,
			wantValid:    false,
			description:  "Should reject cancelled operations",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			record := OperationRecord{
				OperationType: tc.recordType,
				Status:        tc.status,
			}
			got := validateBackupOperationRecord(record)
			if got != tc.wantValid {
				t.Errorf("%s: got %v, want %v (%s)", tc.name, got, tc.wantValid, tc.description)
			}
		})
	}
}

func TestValidateBackupTargetInstanceRef(t *testing.T) {
	testCases := []struct {
		name        string
		instanceRef string
		envID       string
		wantValid   bool
		description string
	}{
		{
			name: "valid-instance-ref",
			instanceRef: `{
				"adapter_id": "hosting.fake",
				"environment_id": "staging",
				"resource_kind": "instance",
				"external_id": "vm-1"
			}`,
			envID: "staging",
			wantValid: true,
			description: "Should accept a valid instance reference matching environment",
		},
		{
			name: "missing-adapter-id",
			instanceRef: `{
				"environment_id": "staging",
				"resource_kind": "instance",
				"external_id": "vm-1"
			}`,
			envID:       "staging",
			wantValid:   false,
			description:  "Should reject missing adapter_id",
		},
		{
			name: "missing-resource-kind",
			instanceRef: `{
				"adapter_id": "hosting.fake",
				"environment_id": "staging",
				"external_id": "vm-1"
			}`,
			envID:       "staging",
			wantValid:   false,
			description:  "Should reject missing resource_kind",
		},
		{
			name:        "missing-instance-ref-empty",
			instanceRef: ``,
			envID:       "",
			wantValid:   false,
			description:  "Should reject empty instance reference",
		},
		{
			name: "mismatched-environment-id",
			instanceRef: `{
				"adapter_id": "hosting.fake",
				"environment_id": "production",
				"resource_kind": "instance",
				"external_id": "vm-1"
			}`,
			envID:       "staging",
			wantValid:   false,
			description:  "Should reject instance ref with mismatched environment ID",
		},
		{
			name:        "invalid-json",
			instanceRef: `{invalid json`,
			envID:       "",
			wantValid:   false,
			description:  "Should reject invalid JSON in instance reference",
		},
		{
			name:         "missing-external-id",
			instanceRef: `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance"}`,
			envID:       "staging",
			wantValid:    false,
			description:  "Should reject missing external_id",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target := OperationTarget{
				InstanceRef: json.RawMessage(tc.instanceRef),
				EnvironmentID: tc.envID,
			}
			got := validateBackupTargetInstanceRef(target)
			if got != tc.wantValid {
				t.Errorf("%s: got %v, want %v (%s)", tc.name, got, tc.wantValid, tc.description)
			}
		})
	}
}

func TestHasNonPendingSteps(t *testing.T) {
	testCases := []struct {
		name     string
		steps    []Step
		wantTrue bool
		desc     string
	}{
		{
			name: "all-pending-no-attempts",
			steps: []Step{
				{Status: protocolstate.StepPending, Attempt: 0},
				{Status: protocolstate.StepPending, Attempt: 0},
			},
			wantTrue: false,
			desc:     "Should return false when all steps are pending with zero attempts",
		},
		{
			name: "one-step-running",
			steps: []Step{
				{Status: protocolstate.StepRunning, Attempt: 1},
			},
			wantTrue: true,
			desc:     "Should return true when any step is running with attempts",
		},
		{
			name: "one-step-completed",
			steps: []Step{
				{Status: protocolstate.StepCompleted, Attempt: 1},
			},
			wantTrue: true,
			desc:     "Should return true when any step is completed",
		},
		{
			name: "pending-but-has-attempts",
			steps: []Step{
				{Status: protocolstate.StepPending, Attempt: 3},
			},
			wantTrue: true,
			desc:     "Should return true when pending step has attempts > 0",
		},
		{
			name: "empty-steps-slice",
			steps: []Step{},
			wantTrue: false,
			desc:     "Should return false for empty steps slice",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := hasNonPendingSteps(tc.steps)
			if got != tc.wantTrue {
				t.Errorf("%s: got %v, want %v (%s)", tc.name, got, tc.wantTrue, tc.desc)
			}
		})
	}
}

func TestReadAndValidateBackupOperation(t *testing.T) {
	s := openTestStore(t)
	server := testLogicalServerFixture(t, s)
	server.Record.LifecycleState = protocolstate.ServerStopped
	server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)

	states := []protocolstate.ServerState{protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady, protocolstate.ServerStopping, protocolstate.ServerStopped}
	for i := 1; i < len(states); i++ {
		server.Record.LifecycleEvents = append(server.Record.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(i), OperationID: server.Record.LastOperationID, ActorID: "operator-1", OperationType: "server.provision", FromState: states[i-1], ToState: states[i], Cause: "test fixture history", RequestFingerprint: "sha256:" + strings.Repeat("a", 64), OccurredAt: server.Record.CreatedAt,
		})
	}

	op := testOperation("backup-context-test")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = server.Record.InstanceRef
	op.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	accepted, _, err := s.AcceptOperation(op, proof, []string{})
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		name     string
		setup    func(*testing.T, *BoltStore, StoredOperation)
		wantErr  bool
		errType  error
		desc     string
	}{
		{
			name: "successful-read-and-validate",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				// Setup already done in backupContextFixture, no additional setup needed
			},
			wantErr:  false,
			errType:  nil,
			desc:     "Should successfully read and validate a valid operation",
		},
		{
			name: "not-found-error",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				opID := op.Record.OperationID + "-nonexistent"
				_, err := s.CaptureBackupInitialState(opID, op.Revision)
				if !errors.Is(err, ErrNotFound) {
					t.Errorf("expected ErrNotFound, got %v", err)
				}
			},
			wantErr:  true,
			errType:  ErrNotFound,
			desc:     "Should return ErrNotFound for non-existent operation",
		},
		{
			name: "revision-conflict-error",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				captured, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.CaptureBackupInitialState(captured.Record.OperationID, captured.Revision+1)
				if !errors.Is(err, ErrRevisionConflict) {
					t.Errorf("expected ErrRevisionConflict, got %v", err)
				}
			},
			wantErr:  true,
			errType:  ErrRevisionConflict,
			desc:     "Should return ErrRevisionConflict for stale revision",
		},
		{
			name: "invalid-record-type-error",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				// Operation is already a backup operation, need to create a different one
				badOp := testOperation("bad-backup")
				badOp.OperationType = "server.wrong-type"
				badOp.RequestSnapshot.OperationType = badOp.OperationType
				badOp.Target.InstanceRef = server.Record.InstanceRef
				badOp.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
				_, _, err := s.AcceptOperation(badOp, proof, []string{})
				if err != nil {
					t.Fatal(err)
				}
				// Try to capture this invalid operation
				_, err = s.CaptureBackupInitialState(badOp.Record.OperationID, badOp.Revision)
				if !errors.Is(err, ErrInvalidRecord) {
					t.Errorf("expected ErrInvalidRecord for wrong type, got %v", err)
				}
			},
			wantErr:  true,
			errType:  ErrInvalidRecord,
			desc:     "Should return ErrInvalidRecord for non-backup operation type",
		},
		{
			name: "terminal-status-error",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				op.Record.Status = protocolstate.OperationCompleted
				if err := s.UpdateOperation(op.Revision, op.Record); err != nil {
					t.Fatal(err)
				}
				// Now try to capture with terminal status
				_, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision)
				if !errors.Is(err, ErrInvalidRecord) {
					t.Errorf("expected ErrInvalidRecord for terminal status, got %v", err)
				}
			},
			wantErr:  true,
			errType:  ErrInvalidRecord,
			desc:     "Should return ErrInvalidRecord for terminal operation status",
		},
		{
			name: "invalid-target-error",
			setup: func(t *testing.T, s *BoltStore, op StoredOperation) {
				op.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"production","resource_kind":"instance","external_id":"vm-1"}`)
				if err := s.UpdateOperation(op.Revision, op.Record); err != nil {
					t.Fatal(err)
				}
				// Try to capture with invalid target
				_, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision)
				if !errors.Is(err, ErrLogicalServerTargetMismatch) {
					t.Errorf("expected ErrLogicalServerTargetMismatch for wrong env, got %v", err)
				}
			},
			wantErr:  true,
			errType:  ErrLogicalServerTargetMismatch,
			desc:     "Should return ErrLogicalServerTargetMismatch for invalid target reference",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t, s, op)
		})
	}
}
