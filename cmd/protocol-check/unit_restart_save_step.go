package main

import (
	"fmt"
	"path/filepath"
	"reflect"
)

// This file holds the save evidence a unit restart needs: the completed
// game.save.request step and the adapter descriptor that advertises it.

// restartSaveCapability is the game capability whose completed step proves
// the save before a restart.
const restartSaveCapability = "game.save.request"

// restartFieldsEqual: left and right hold equal values under every field.
func restartFieldsEqual(left, right map[string]any, fields ...string) bool {
	for _, field := range fields {
		if left[field] != right[field] {
			return false
		}
	}
	return true
}

// saveStepBound: the completed save step is the selected, advertised
// game.save.request capability bound to this operation, target, and release.
func restartSaveStepBound(pkg *protocolPackage, fixture, c map[string]any, r restartCase) bool {
	selectedAdapterID := stringValue(r.core, "selected_game_adapter_id")
	selectedCapability := asObject(c["selected_game_capability"])
	if selectedCapability == nil {
		selectedCapability = asObject(fixture["selected_game_capability"])
	}
	return selectedSaveCapabilityBound(pkg, selectedAdapterID, selectedCapability) &&
		r.saveStepPayloadBound(pkg, selectedCapability) &&
		r.saveStepRecordBound(selectedAdapterID, selectedCapability)
}

// selectedSaveCapabilityBound: the selected capability is game.save.request
// on the selected adapter, exactly as that adapter's descriptor advertises
// it (name, version and both schemas).
func selectedSaveCapabilityBound(pkg *protocolPackage, adapterID string, selected map[string]any) bool {
	advertised, err := findAdapterCapability(pkg, adapterID, restartSaveCapability)
	return err == nil && selected["adapter_id"] == adapterID && selected["name"] == restartSaveCapability &&
		restartFieldsEqual(selected, advertised, "name", "version", "input_schema_uri", "output_schema_uri")
}

// saveStepPayloadBound: the save step's input and output match the selected
// capability's schemas, and the input asks to save this target and release
// at the operator's request.
func (r restartCase) saveStepPayloadBound(pkg *protocolPackage, selected map[string]any) bool {
	stepInput := asObject(r.saveStep["input"])
	return pkg.validateSchema(stringValue(selected, "input_schema_uri"), stepInput) == nil &&
		r.saveStepInputRequested(stepInput) &&
		pkg.validateSchema(stringValue(selected, "output_schema_uri"), r.saveStep["output"]) == nil
}

// saveStepInputRequested: the save step's input saves this target and
// release at the operator's request.
func (r restartCase) saveStepInputRequested(stepInput map[string]any) bool {
	return reflect.DeepEqual(r.target, stepInput["target"]) && r.request["release_id"] == stepInput["release_id"] &&
		stringValue(stepInput, "reason") == "operator_request"
}

// saveStepRecordBound: the save step is the request's succeeded step of
// this operation, ran the selected adapter's game.save.request at the
// selected version, and its output is the save confirmation.
func (r restartCase) saveStepRecordBound(adapterID string, selected map[string]any) bool {
	return r.saveStepSucceededInOperation() && r.saveStepRanCapability(adapterID, selected) &&
		reflect.DeepEqual(r.save, r.saveStep["output"])
}

// saveStepSucceededInOperation: the save step is the request's save step of
// this operation, and it succeeded.
func (r restartCase) saveStepSucceededInOperation() bool {
	return stringValue(r.core, "operation_id") != "" && r.saveStep["operation_id"] == r.core["operation_id"] &&
		r.saveStep["step_id"] == r.request["save_step_id"] && stringValue(r.saveStep, "status") == "succeeded"
}

// saveStepRanCapability: the save step ran the selected adapter's
// game.save.request at the selected version.
func (r restartCase) saveStepRanCapability(adapterID string, selected map[string]any) bool {
	return r.saveStep["adapter_id"] == adapterID && r.saveStep["capability_name"] == restartSaveCapability &&
		r.saveStep["capability_version"] == selected["version"]
}

func findAdapterCapability(pkg *protocolPackage, adapterID, capabilityName string) (map[string]any, error) {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "examples", "adapter-descriptor.*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		value, err := parseJSONFile(path)
		if err != nil {
			return nil, err
		}
		descriptor := asObject(value)
		if stringValue(descriptor, "adapter_id") != adapterID {
			continue
		}
		if stringValue(descriptor, "category") != "game" {
			return nil, fmt.Errorf("selected adapter %q is not a game adapter", adapterID)
		}
		for _, rawCapability := range arrayValue(descriptor, "capabilities") {
			capability := asObject(rawCapability)
			if stringValue(capability, "name") == capabilityName {
				return capability, nil
			}
		}
		return nil, fmt.Errorf("adapter %q does not advertise %q", adapterID, capabilityName)
	}
	return nil, fmt.Errorf("selected adapter descriptor %q is missing", adapterID)
}
