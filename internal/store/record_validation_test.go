package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func TestValidateRecordPreservesFieldAndStepFailures(t *testing.T) {
	baseline := testOperation("validation-cases")
	now := time.Now().UTC()
	baseline.CreatedAt = now
	baseline.UpdatedAt = now
	if err := validateRecord(baseline); err != nil {
		t.Fatalf("baseline is invalid: %v", err)
	}
	tests := []struct {
		name string
		field string
		change func(*OperationRecord)
	}{
		{"invalid identity", "operation_id", func(r *OperationRecord) { r.OperationID = "" }},
		{"invalid request object", "request_snapshot", func(r *OperationRecord) { r.RequestSnapshot.Parameters = json.RawMessage(`[]`) }},
		{"invalid step status", "steps[0]", func(r *OperationRecord) { r.Steps[0].Status = protocolstate.StepStatus("unrecognized") }},
		{"started after update", "steps[0].started_at", func(r *OperationRecord) { later := r.UpdatedAt.Add(time.Minute); r.Steps[0].StartedAt = &later }},
		{"missing current step", "current_step_id", func(r *OperationRecord) { absent := "absent"; r.CurrentStepID = &absent }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := baseline
			r.Steps = append([]OperationStep(nil), baseline.Steps...)
			tt.change(&r)
			err := validateRecord(r)
			if !errors.Is(err, ErrInvalidRecord) || !strings.Contains(err.Error(), tt.field) {
				t.Errorf("got %v, want invalid field %q", err, tt.field)
			}
		})
	}
}
