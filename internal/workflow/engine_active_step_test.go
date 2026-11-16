package workflow

import (
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestActiveStepIndexPrefersCurrentActiveStep(t *testing.T) {
	steps := []store.OperationStep{
		{StepID: "finished", Status: protocolstate.StepSucceeded},
		{StepID: "running", Status: protocolstate.StepRunning},
		{StepID: "pending", Status: protocolstate.StepPending},
		{StepID: "waiting", Status: protocolstate.StepWaiting},
		{StepID: "unknown", Status: protocolstate.StepOutcomeUnknown},
	}
	for _, tc := range []struct {
		name    string
		current *string
		steps   []store.OperationStep
		want    int
	}{
		{name: "no steps", want: -1},
		{name: "no active steps", current: stringPointer("finished"), steps: steps[:1], want: -1},
		{name: "no current pointer", steps: steps, want: 1},
		{name: "unknown current pointer", current: stringPointer("missing"), steps: steps, want: 1},
		{name: "inactive current pointer", current: stringPointer("pending"), steps: steps, want: 1},
		{name: "later waiting current step", current: stringPointer("waiting"), steps: steps, want: 3},
		{name: "later unknown current step", current: stringPointer("unknown"), steps: steps, want: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := store.OperationRecord{CurrentStepID: tc.current, Steps: tc.steps}
			if got := activeStepIndex(record); got != tc.want {
				t.Errorf("activeStepIndex() = %d, want %d", got, tc.want)
			}
		})
	}
}
