package workflow

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// This file reads the stored server.stop step evidence: the provider and
// runtime inspections, the game save and the graceful stop result.

func stopUnitInspectOutput(operation store.OperationRecord, id string) (stopUnitInspection, error) {
	return plannedStepOutput[stopUnitInspection](operation, id, "runtime inspection output is missing")
}

// stopUnitObservedRunning reports whether the runtime inspection observed
// the exact planned unit running.
func stopUnitObservedRunning(operation store.OperationRecord) bool {
	observation, err := stopUnitInspectOutput(operation, stopInspectUnitStep)
	return err == nil && equalJSON(observation.UnitRef, stopUnitRef(operation)) && observation.State == "running"
}

func stopUnitRef(operation store.OperationRecord) json.RawMessage {
	return stepField(operationStep(operation, stopRuntimeStep).Input, "unit_ref")
}

func sameStopStepReference(operation store.OperationRecord, first, second, field string) bool {
	return stepBindsRef(operationStep(operation, first).Input, field, stepField(operationStep(operation, second).Input, field))
}

func gameSaveConfirmed(operation store.OperationRecord) bool {
	save, err := stopSaveOutput(operation)
	return err == nil && save.Outcome == "confirmed" && save.SavedAt != "" &&
		stopSaveBinds(operation, save)
}

func gameSaveRejected(operation store.OperationRecord) bool {
	save, err := stopSaveOutput(operation)
	return err == nil && save.Outcome == "rejected" && stopSaveBinds(operation, save)
}

// stopSaveBinds reports whether a save observation names the operation's
// server, the planned release and the instance the plan inspected.
func stopSaveBinds(operation store.OperationRecord, save gameSaveObservation) bool {
	var input struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID string `json:"release_id"`
	}
	if json.Unmarshal(operationStep(operation, stopSaveGameStep).Input, &input) != nil {
		return false
	}
	plannedInstance := stepField(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref")
	return saveNamesTarget(save, operation.Target) && save.ReleaseID == input.ReleaseID &&
		savedPlannedInstance(save.Target.InstanceRef, input.Target.InstanceRef, plannedInstance)
}

func saveNamesTarget(save gameSaveObservation, target store.OperationTarget) bool {
	return save.Target.ServerID == target.ServerID && save.Target.EnvironmentID == target.EnvironmentID
}

// savedPlannedInstance reports whether the saved instance is the one the
// save request named, and that one is the instance the plan inspected.
func savedPlannedInstance(saved, requested, planned json.RawMessage) bool {
	return equalJSON(saved, requested) && equalJSON(requested, planned)
}

func stopSaveOutput(operation store.OperationRecord) (gameSaveObservation, error) {
	output, err := plannedStepOutput[gameSaveObservation](operation, stopSaveGameStep, "game save output is missing")
	if err != nil {
		return output, err
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return gameSaveObservation{}, err
	}
	if output.Outcome == "confirmed" {
		if _, err := time.Parse(time.RFC3339Nano, output.SavedAt); err != nil {
			return gameSaveObservation{}, err
		}
	}
	return output, nil
}

func stopRuntimeOutput(operation store.OperationRecord) (executionUnitStopResult, error) {
	var output executionUnitStopResult
	step := operationStep(operation, stopRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("graceful stop output is missing")
	}
	return output, nil
}
