package store

import (
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// prepareNewRecord applies the defaults and creation rules for a record that
// is about to be stored for the first time, stamps its creation time, and
// then runs the full record validation.
func prepareNewRecord(record OperationRecord, now time.Time) (OperationRecord, error) {
	if err := applyNewRecordRules(&record); err != nil {
		return OperationRecord{}, err
	}
	record.CreatedAt = now.UTC()
	record.UpdatedAt = record.CreatedAt
	if err := validateRecord(record); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// applyNewRecordRules defaults an empty status to pending and refuses a new
// record that carries core-owned or execution state. The checks run in a
// fixed order and the first failure is returned.
func applyNewRecordRules(record *OperationRecord) error {
	if record.BackupInitialLifecycleState != "" {
		return newRecordError("backup initial state is core-owned")
	}
	if record.Status == "" {
		record.Status = protocolstate.OperationPending
	}
	if record.Status != protocolstate.OperationPending {
		return newRecordError("new operations must start pending")
	}
	if record.Steps == nil {
		record.Steps = []OperationStep{}
	}
	if err := applyNewStepRules(record.Steps); err != nil {
		return err
	}
	if hasExecutionOutcome(*record) {
		return newRecordError("new operation cannot contain an execution outcome")
	}
	return nil
}

// applyNewStepRules checks the steps in order. It defaults each empty step
// status to pending in place, in the caller's slice.
func applyNewStepRules(steps []OperationStep) error {
	for i := range steps {
		if err := applyNewStepRule(&steps[i]); err != nil {
			return err
		}
	}
	return nil
}

// applyNewStepRule requires a new step to be pending and to have no resolved
// input, which the engine writes only before dispatch.
func applyNewStepRule(step *OperationStep) error {
	if step.Status == "" {
		step.Status = protocolstate.StepPending
	}
	if step.Status != protocolstate.StepPending {
		return newRecordError("new operation steps must start pending")
	}
	if len(step.ResolvedInput) > 0 {
		return newRecordError("resolved step inputs are written by the engine before dispatch")
	}
	return nil
}

// hasExecutionOutcome reports a record that has started, completed, or
// recorded a result or error.
func hasExecutionOutcome(record OperationRecord) bool {
	return record.StartedAt != nil || record.CompletedAt != nil || len(record.Result) != 0 || len(record.Error) != 0
}

// newRecordError refuses a new record for the given reason.
func newRecordError(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecord, reason)
}
