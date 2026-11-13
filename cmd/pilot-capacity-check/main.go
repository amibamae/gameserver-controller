package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type policyDocument struct {
	PolicyID          string                   `json:"policy_id"`
	PolicyVersion     int                      `json:"policy_version"`
	Enforcement       *enforcementPolicy       `json:"enforcement"`
	MaxInstancesTotal int                      `json:"max_instances_total"`
	OwnershipScope    *ownershipScope          `json:"ownership_scope"`
	CapacityGroups    map[string]capacityGroup `json:"capacity_groups"`
	CreatePolicy      *createPolicy            `json:"create_policy"`
}

type enforcementPolicy struct {
	Mode            string `json:"mode"`
	RuntimeEnforced *bool  `json:"runtime_enforced"`
	Reason          string `json:"reason"`
}

type ownershipScope struct {
	CapacityCountsOnlyControllerOwnedInstances   *bool `json:"capacity_counts_only_controller_owned_instances"`
	UnmanagedProductionMayRemainLive             *bool `json:"unmanaged_production_may_remain_live"`
	ProductionAdoptionIsOptional                 *bool `json:"production_adoption_is_optional"`
	ProviderCreatedInstancesAreControllerManaged *bool `json:"provider_created_instances_are_controller_managed"`
}

type capacityGroup struct {
	MaxInstances         int      `json:"max_instances"`
	AcceptedEnvironments []string `json:"accepted_environments"`
}

type createPolicy struct {
	ReserveSlotAtomicallyBeforeProviderCreate *bool            `json:"reserve_slot_atomically_before_provider_create"`
	ReservationCountsAsCapacity               *bool            `json:"reservation_counts_as_capacity"`
	RejectDuplicateOrOccupiedSlot             *bool            `json:"reject_duplicate_or_occupied_slot"`
	RejectIfTotalWouldExceedLimit             *bool            `json:"reject_if_total_would_exceed_limit"`
	RejectUnsupportedEnvironment              *bool            `json:"reject_unsupported_environment"`
	Occupancy                                 *occupancyPolicy `json:"occupancy"`
}

type occupancyPolicy struct {
	CountEveryManagedInstanceWithoutProviderConfirmedAbsenceOrDestruction *bool    `json:"count_every_managed_instance_without_provider_confirmed_absence_or_destruction"`
	CountUnresolvedCreateOutcome                                          *bool    `json:"count_unresolved_create_outcome"`
	CountUnresolvedDestroyOutcome                                         *bool    `json:"count_unresolved_destroy_outcome"`
	ReleaseOnlyAfterProviderConfirmation                                  []string `json:"release_only_after_provider_confirmation"`
}

type casesDocument struct {
	PolicyID string       `json:"policy_id"`
	Cases    []policyCase `json:"cases"`
}

type policyCase struct {
	ID                 string             `json:"id"`
	RequestAction      string             `json:"request_action"`
	RequestEnvironment string             `json:"request_environment"`
	ExistingInstances  []existingInstance `json:"existing_instances"`
	Expected           *expectedDecision  `json:"expected"`
}

type existingInstance struct {
	Environment   string `json:"environment"`
	ProviderState string `json:"provider_state"`
	Managed       *bool  `json:"managed"`
}

type expectedDecision struct {
	Allowed *bool  `json:"allowed"`
	Reason  string `json:"reason"`
}

type decision struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

func main() {
	if err := run(".", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, stdout io.Writer) error {
	var policy policyDocument
	if err := readJSON(filepath.Join(root, "internal", "pilotcapacity", "capacity-policy.json"), &policy); err != nil {
		return fmt.Errorf("read capacity policy: %w", err)
	}
	var cases casesDocument
	if err := readJSON(filepath.Join(root, "pilot", "capacity-policy-cases.json"), &cases); err != nil {
		return fmt.Errorf("read capacity cases: %w", err)
	}

	validationErrors := validateDocuments(policy, cases)
	if len(validationErrors) != 0 {
		return fmt.Errorf("capacity policy validation failed:\n- %s", strings.Join(validationErrors, "\n- "))
	}
	fmt.Fprintf(stdout, "capacity policy is runtime-enforced by gsc-pilot provision; %d policy cases are valid declarative references\n", len(cases.Cases))
	return nil
}

func readJSON(path string, destination any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// validation collects error messages in the order the checks run and counts
// the structural policy problems that make case decisions meaningless.
type validation struct {
	errors             []string
	staticPolicyErrors int
}

// require records message unless condition holds.
func (v *validation) require(condition bool, message string) {
	if !condition {
		v.errors = append(v.errors, message)
	}
}

// requireTrue records that field must be true unless value is set and true.
func (v *validation) requireTrue(value *bool, field string) {
	v.require(isTrue(value), field+" must be true")
}

// structural records a missing or malformed policy section, which also
// suppresses the per-case decision comparison.
func (v *validation) structural(message string) {
	v.errors = append(v.errors, message)
	v.staticPolicyErrors++
}

func isTrue(value *bool) bool { return value != nil && *value }

// expectedGroups is the only capacity-group layout the pilot accepts.
var expectedGroups = map[string]capacityGroup{
	"production": {
		MaxInstances:         1,
		AcceptedEnvironments: []string{"production"},
	},
	"shared_nonproduction": {
		MaxInstances:         1,
		AcceptedEnvironments: []string{"staging", "testing"},
	},
}

func validateDocuments(policy policyDocument, cases casesDocument) []string {
	v := &validation{}
	v.require(policy.PolicyID == "supervised-pilot-vm-capacity", "policy_id must be supervised-pilot-vm-capacity")
	v.require(policy.PolicyVersion >= 2, "policy_version must be at least 2")
	v.require(policy.MaxInstancesTotal > 0, "max_instances_total must be greater than zero")
	v.checkEnforcement(policy.Enforcement)
	v.checkOwnership(policy.OwnershipScope)
	v.checkCapacityGroups(policy)
	v.checkCreatePolicy(policy.CreatePolicy)
	v.require(cases.PolicyID == policy.PolicyID, "cases.policy_id must match policy.policy_id")
	v.require(len(cases.Cases) > 0, "at least one capacity case is required")

	decisionsComparable := v.staticPolicyErrors == 0 && policy.Enforcement != nil && policy.CreatePolicy != nil && policy.CreatePolicy.Occupancy != nil
	seen := make(map[string]bool, len(cases.Cases))
	for index, testCase := range cases.Cases {
		if problem := caseProblem(index, testCase, seen); problem != "" {
			v.errors = append(v.errors, problem)
			continue
		}
		if decisionsComparable {
			v.checkDecision(policy, testCase)
		}
	}
	return v.errors
}

func (v *validation) checkEnforcement(enforcement *enforcementPolicy) {
	if enforcement == nil {
		v.structural("enforcement must be an object")
		return
	}
	v.require(enforcement.Mode == "runtime", "enforcement.mode must be runtime")
	v.require(isTrue(enforcement.RuntimeEnforced), "enforcement.runtime_enforced must be true")
	v.require(strings.Contains(enforcement.Reason, "gsc-pilot provision"), "enforcement.reason must identify the runtime provision command")
}

func (v *validation) checkOwnership(ownership *ownershipScope) {
	if ownership == nil {
		v.structural("ownership_scope must be an object")
		return
	}
	v.requireTrue(ownership.CapacityCountsOnlyControllerOwnedInstances, "ownership_scope.capacity_counts_only_controller_owned_instances")
	v.requireTrue(ownership.UnmanagedProductionMayRemainLive, "ownership_scope.unmanaged_production_may_remain_live")
	v.requireTrue(ownership.ProductionAdoptionIsOptional, "ownership_scope.production_adoption_is_optional")
	v.requireTrue(ownership.ProviderCreatedInstancesAreControllerManaged, "ownership_scope.provider_created_instances_are_controller_managed")
}

func (v *validation) checkCapacityGroups(policy policyDocument) {
	if !reflect.DeepEqual(policy.CapacityGroups, expectedGroups) {
		v.structural("capacity_groups must define one production slot and one shared staging/testing slot")
		return
	}
	total := 0
	environments := make([]string, 0, 3)
	for _, group := range policy.CapacityGroups {
		total += group.MaxInstances
		environments = append(environments, group.AcceptedEnvironments...)
	}
	sort.Strings(environments)
	v.require(policy.MaxInstancesTotal <= total, "max_instances_total cannot exceed the sum of capacity-group slots")
	v.require(reflect.DeepEqual(environments, []string{"production", "staging", "testing"}), "capacity groups must assign production, staging, and testing exactly once")
}

func (v *validation) checkCreatePolicy(create *createPolicy) {
	if create == nil {
		v.structural("create_policy must be an object")
		return
	}
	v.requireTrue(create.ReserveSlotAtomicallyBeforeProviderCreate, "create_policy.reserve_slot_atomically_before_provider_create")
	v.requireTrue(create.ReservationCountsAsCapacity, "create_policy.reservation_counts_as_capacity")
	v.requireTrue(create.RejectDuplicateOrOccupiedSlot, "create_policy.reject_duplicate_or_occupied_slot")
	v.requireTrue(create.RejectIfTotalWouldExceedLimit, "create_policy.reject_if_total_would_exceed_limit")
	v.requireTrue(create.RejectUnsupportedEnvironment, "create_policy.reject_unsupported_environment")
	occupancy := create.Occupancy
	if occupancy == nil {
		v.structural("create_policy.occupancy must be an object")
		return
	}
	v.requireTrue(occupancy.CountEveryManagedInstanceWithoutProviderConfirmedAbsenceOrDestruction, "create_policy.occupancy.count_every_managed_instance_without_provider_confirmed_absence_or_destruction")
	v.requireTrue(occupancy.CountUnresolvedCreateOutcome, "create_policy.occupancy.count_unresolved_create_outcome")
	v.requireTrue(occupancy.CountUnresolvedDestroyOutcome, "create_policy.occupancy.count_unresolved_destroy_outcome")
	v.require(reflect.DeepEqual(occupancy.ReleaseOnlyAfterProviderConfirmation, []string{"absent", "destroyed"}), "create_policy.occupancy.release_only_after_provider_confirmation must be [absent, destroyed]")
}

// caseProblem returns the first structural problem with a case, or "" when
// the case is well formed. It records the case ID in seen once the ID is known
// to be new.
func caseProblem(index int, testCase policyCase, seen map[string]bool) string {
	switch {
	case testCase.ID == "":
		return fmt.Sprintf("cases[%d].id must be a non-empty string", index)
	case seen[testCase.ID]:
		return "duplicate case id: " + testCase.ID
	}
	seen[testCase.ID] = true
	switch {
	case testCase.RequestEnvironment == "":
		return testCase.ID + ".request_environment must be a non-empty string"
	case testCase.RequestAction != "create" && testCase.RequestAction != "adopt":
		return testCase.ID + ".request_action must be create or adopt"
	case testCase.ExistingInstances == nil:
		return testCase.ID + ".existing_instances must be an array"
	case !instancesWellFormed(testCase.ExistingInstances):
		return testCase.ID + ".existing_instances entries need environment and provider_state strings"
	case testCase.Expected == nil || testCase.Expected.Allowed == nil || testCase.Expected.Reason == "":
		return testCase.ID + ".expected must include allowed and reason"
	}
	return ""
}

func instancesWellFormed(instances []existingInstance) bool {
	for _, instance := range instances {
		if instance.Environment == "" || instance.ProviderState == "" || instance.Managed == nil {
			return false
		}
	}
	return true
}

// checkDecision compares the policy's decision for a well-formed case with
// the case's expected result.
func (v *validation) checkDecision(policy policyDocument, testCase policyCase) {
	actual := decide(policy, testCase.RequestEnvironment, testCase.ExistingInstances)
	expected := testCase.Expected
	if actual.Allowed != *expected.Allowed || !matchesExpectedReason(actual.Reason, expected.Reason) {
		v.errors = append(v.errors, fmt.Sprintf("%s: expected {allowed:%t reason:%q}, got {allowed:%t reason:%q}", testCase.ID, *expected.Allowed, expected.Reason, actual.Allowed, actual.Reason))
	}
}

func matchesExpectedReason(actual, expected string) bool {
	if expected == "capacity_reached" {
		return actual == "total_limit_reached" || actual == "slot_occupied"
	}
	return actual == expected
}

func decide(policy policyDocument, environment string, instances []existingInstance) decision {
	requestedGroup := ""
	var requestedCapacity int
	for groupID, group := range policy.CapacityGroups {
		for _, accepted := range group.AcceptedEnvironments {
			if environment == accepted {
				requestedGroup = groupID
				requestedCapacity = group.MaxInstances
				break
			}
		}
		if requestedGroup != "" {
			break
		}
	}
	if requestedGroup == "" {
		return decision{Allowed: false, Reason: "unsupported_environment"}
	}

	releasedStates := make(map[string]bool)
	for _, state := range policy.CreatePolicy.Occupancy.ReleaseOnlyAfterProviderConfirmation {
		releasedStates[state] = true
	}
	occupied := 0
	groupOccupied := 0
	groupEnvironments := make(map[string]bool)
	for _, accepted := range policy.CapacityGroups[requestedGroup].AcceptedEnvironments {
		groupEnvironments[accepted] = true
	}
	for _, instance := range instances {
		if instance.Managed == nil || !*instance.Managed || releasedStates[instance.ProviderState] {
			continue
		}
		occupied++
		if groupEnvironments[instance.Environment] {
			groupOccupied++
		}
	}
	if occupied >= policy.MaxInstancesTotal {
		return decision{Allowed: false, Reason: "total_limit_reached"}
	}
	if groupOccupied >= requestedCapacity {
		return decision{Allowed: false, Reason: "slot_occupied"}
	}
	return decision{Allowed: true, Reason: "slot_available"}
}
