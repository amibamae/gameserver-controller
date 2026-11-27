package workflow

import (
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func currentStepIndex(record store.OperationRecord) int {
	if record.CurrentStepID == nil {
		return -1
	}
	for index := range record.Steps {
		if record.Steps[index].StepID == *record.CurrentStepID {
			return index
		}
	}
	return -1
}

func activeStepIndex(record store.OperationRecord) int {
	firstActive := -1
	for index := range record.Steps {
		step := &record.Steps[index]
		switch step.Status {
		case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
		default:
			continue
		}
		if record.CurrentStepID == nil || step.StepID == *record.CurrentStepID {
			return index
		}
		if firstActive < 0 {
			firstActive = index
		}
	}
	return firstActive
}

func firstPendingStep(record store.OperationRecord) int {
	for index := range record.Steps {
		switch record.Steps[index].Status {
		case protocolstate.StepPending:
			return index
		case protocolstate.StepSucceeded, protocolstate.StepSkipped:
			continue
		default:
			return -1
		}
	}
	return -1
}

func hasPendingSteps(record store.OperationRecord) bool { return firstPendingStep(record) >= 0 }

func allStepsComplete(record store.OperationRecord) bool {
	for _, step := range record.Steps {
		if step.Status != protocolstate.StepSucceeded && step.Status != protocolstate.StepSkipped {
			return false
		}
	}
	return true
}

func stepOrderValid(record store.OperationRecord) bool {
	incompleteSeen := false
	activeCount := 0
	for _, step := range record.Steps {
		switch step.Status {
		case protocolstate.StepSucceeded, protocolstate.StepSkipped:
			if incompleteSeen {
				return false
			}
		case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
			if incompleteSeen {
				return false
			}
			activeCount++
			incompleteSeen = true
		case protocolstate.StepPending, protocolstate.StepFailed, protocolstate.StepCancelled:
			incompleteSeen = true
		default:
			return false
		}
	}
	return activeCount <= 1
}
