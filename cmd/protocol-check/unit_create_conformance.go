package main

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
)

func evaluateUnitCreate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		results = append(results, evaluateUnitCreateCase(pkg, asObject(rawCase)))
	}
	return map[string]any{"evaluations": results}, nil
}

// createCase is one evaluation of the systemd unit create vector.
type createCase struct {
	request, output, preconditions map[string]any
	unit, target, instance         map[string]any
	definition, checksum           map[string]any
	existing, resolved, inspection map[string]any
}

func newCreateCase(c map[string]any) createCase {
	request := asObject(c["request"])
	target := asObject(request["target"])
	definition := asObject(request["unit_definition"])
	return createCase{
		request: request, output: asObject(c["output"]), preconditions: asObject(c["core_preconditions"]),
		unit: asObject(request["unit_ref"]), target: target, instance: asObject(target["instance_ref"]),
		definition: definition, checksum: asObject(definition["checksum"]),
		existing: asObject(c["existing_unit"]), resolved: asObject(c["resolved_operation"]), inspection: asObject(c["unit_inspection"]),
	}
}

// definitionVerified: the unit definition checksum is the SHA-256 of the
// artifact bytes.
func (u createCase) definitionVerified(artifactBytes string) bool {
	artifactDigest := sha256.Sum256([]byte(artifactBytes))
	return stringValue(u.checksum, "algorithm") == "sha256" &&
		stringValue(u.checksum, "digest") == hex.EncodeToString(artifactDigest[:])
}

// existingCompatible: no unit exists, or the existing one is this exact unit,
// target, release, and definition in a stopped or running state.
func (u createCase) existingCompatible() bool {
	return len(u.existing) == 0 || (reflect.DeepEqual(u.existing["unit_ref"], u.request["unit_ref"]) &&
		reflect.DeepEqual(u.existing["target"], u.request["target"]) && u.existing["release_id"] == u.request["release_id"] &&
		reflect.DeepEqual(u.existing["definition_checksum"], u.checksum) &&
		(stringValue(u.existing, "state") == "stopped" || stringValue(u.existing, "state") == "running"))
}

// operationBound: an external operation reference, if any, names this
// adapter's unit operation and matches the resolved operation.
func (u createCase) operationBound() bool {
	operationRef, hasOperationRef := u.output["external_operation_ref"]
	return !hasOperationRef || (asObject(operationRef)["adapter_id"] == u.unit["adapter_id"] &&
		asObject(operationRef)["environment_id"] == u.unit["environment_id"] &&
		stringValue(asObject(operationRef), "resource_kind") == "unit-operation" &&
		reflect.DeepEqual(operationRef, u.resolved["external_operation_ref"]) &&
		reflect.DeepEqual(u.request["unit_ref"], u.resolved["unit_ref"]) &&
		reflect.DeepEqual(u.target, u.resolved["target"]) &&
		u.request["release_id"] == u.resolved["release_id"] &&
		reflect.DeepEqual(u.checksum, u.resolved["definition_checksum"]))
}

// resultBound: the create result names this unit, target, release, and
// definition.
func (u createCase) resultBound(operationBound bool) bool {
	return reflect.DeepEqual(u.output["unit_ref"], u.request["unit_ref"]) && reflect.DeepEqual(u.output["target"], u.target) &&
		u.output["release_id"] == u.request["release_id"] && reflect.DeepEqual(u.output["definition_checksum"], u.checksum) && operationBound
}

func installedState(state string) bool { return state == "stopped" || state == "running" }

// createDispatchChecks are the preconditions for dispatching a unit create.
type createDispatchChecks struct {
	requestValid, hostAvailable, definitionVerified, adapterBound bool
	definitionApproved, targetConsistent, existingCompatible      bool
}

func (u createCase) dispatchChecks(pkg *protocolPackage, artifactBytes string) createDispatchChecks {
	host := asObject(u.preconditions["host_inspection"])
	approvedDefinition := asObject(u.preconditions["approved_release_definition"])
	return createDispatchChecks{
		requestValid:       pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-input", u.request) == nil,
		hostAvailable:      reflect.DeepEqual(u.instance, host["instance_ref"]) && stringValue(host, "state") == "active",
		definitionVerified: u.definitionVerified(artifactBytes),
		adapterBound: stringValue(u.preconditions, "execution_adapter_id") != "" &&
			u.unit["adapter_id"] == u.preconditions["execution_adapter_id"],
		definitionApproved: u.request["release_id"] == approvedDefinition["release_id"] &&
			reflect.DeepEqual(u.definition, approvedDefinition["unit_definition"]),
		targetConsistent: u.unit["environment_id"] == u.target["environment_id"] &&
			u.target["environment_id"] == u.instance["environment_id"] && stringValue(u.unit, "resource_kind") == "systemd-unit",
		existingCompatible: u.existingCompatible(),
	}
}

func (d createDispatchChecks) allowed() bool {
	return d.requestValid && d.adapterBound && d.targetConsistent && d.definitionApproved && d.definitionVerified && d.existingCompatible &&
		d.hostAvailable
}

func evaluateUnitCreateCase(pkg *protocolPackage, creationCase map[string]any) map[string]any {
	u := newCreateCase(creationCase)
	outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-output", u.output) == nil
	checks := u.dispatchChecks(pkg, stringValue(creationCase, "artifact_bytes"))
	dispatchAllowed := checks.allowed()

	operationBound := u.operationBound()
	bound := u.resultBound(operationBound)
	outcome := stringValue(u.output, "outcome")
	confirmed := dispatchAllowed && outputValid && bound && (outcome == "created" || outcome == "unchanged")
	uncertain := outcome == "accepted" || outcome == "outcome_unknown"
	inspectionBound := reflect.DeepEqual(u.request["unit_ref"], u.inspection["unit_ref"]) && reflect.DeepEqual(u.target, u.inspection["target"])
	inspectionState := stringValue(u.inspection, "state")
	installedDefinitionMatches := u.inspection["release_id"] == u.request["release_id"] && reflect.DeepEqual(u.inspection["definition_checksum"], u.checksum)
	// Each outcome below needs a dispatched, valid, bound, uncertain result
	// whose follow-up inspection names this unit.
	inspectable := dispatchAllowed && outputValid && bound && uncertain && inspectionBound
	reconciled := inspectable && installedDefinitionMatches &&
		stringValue(u.resolved, "state") == "completed" && installedState(inspectionState)
	retryPermitted := inspectable && inspectionState == "absent" &&
		stringValue(u.resolved, "state") == "no_effect"
	operatorRequired := dispatchAllowed && uncertain && (!operationBound || (len(u.inspection) > 0 &&
		(!inspectionBound || inspectionState == "unknown" || (installedState(inspectionState) && !installedDefinitionMatches))))
	mustInspect := stringValue(u.existing, "state") == "unknown" ||
		(dispatchAllowed && (!outputValid || !bound || (uncertain && !reconciled && !retryPermitted)))
	return map[string]any{
		"case_id":                   stringValue(creationCase, "case_id"),
		"request_schema_valid":      checks.requestValid,
		"result_schema_valid":       outputValid,
		"definition_verified":       checks.definitionVerified,
		"adapter_bound":             checks.adapterBound,
		"host_available":            checks.hostAvailable,
		"definition_approved":       checks.definitionApproved,
		"target_consistent":         checks.targetConsistent,
		"existing_unit_compatible":  checks.existingCompatible,
		"dispatch_allowed":          dispatchAllowed,
		"result_bound":              bound,
		"definition_confirmed":      confirmed || reconciled,
		"must_inspect_before_retry": mustInspect,
		"reconciliation_confirmed":  reconciled,
		"retry_permitted":           retryPermitted,
		"operator_required":         operatorRequired,
		"readiness_established":     false,
	}
}

func init() {
	registerEvaluator(evaluateUnitCreate, "execution.unit.create.exact-definition-no-autostart")
}
