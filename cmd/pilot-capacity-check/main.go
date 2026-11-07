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
	PolicyID          string                    `json:"policy_id"`
	PolicyVersion     int                       `json:"policy_version"`
	Enforcement       *enforcementPolicy        `json:"enforcement"`
	MaxInstancesTotal int                       `json:"max_instances_total"`
	CapacityGroups    map[string]capacityGroup  `json:"capacity_groups"`
	CreatePolicy      *createPolicy             `json:"create_policy"`
}

type enforcementPolicy struct {
	Mode           string `json:"mode"`
	RuntimeEnforced *bool  `json:"runtime_enforced"`
	Reason         string `json:"reason"`
}

type capacityGroup struct {
	MaxInstances         int      `json:"max_instances"`
	AcceptedEnvironments []string `json:"accepted_environments"`
}

type createPolicy struct {
	ReserveSlotAtomicallyBeforeProviderCreate *bool             `json:"reserve_slot_atomically_before_provider_create"`
	ReservationCountsAsCapacity               *bool             `json:"reservation_counts_as_capacity"`
	RejectDuplicateOrOccupiedSlot             *bool             `json:"reject_duplicate_or_occupied_slot"`
	RejectIfTotalWouldExceedLimit             *bool             `json:"reject_if_total_would_exceed_limit"`
	RejectUnsupportedEnvironment              *bool             `json:"reject_unsupported_environment"`
	Occupancy                                 *occupancyPolicy  `json:"occupancy"`
}

type occupancyPolicy struct {
	CountEveryInstanceWithoutProviderConfirmedAbsenceOrDestruction *bool    `json:"count_every_instance_without_provider_confirmed_absence_or_destruction"`
	CountUnresolvedCreateOutcome                                  *bool    `json:"count_unresolved_create_outcome"`
	CountUnresolvedDestroyOutcome                                 *bool    `json:"count_unresolved_destroy_outcome"`
	ReleaseOnlyAfterProviderConfirmation                          []string `json:"release_only_after_provider_confirmation"`
}

type casesDocument struct {
	PolicyID string        `json:"policy_id"`
	Cases    []policyCase  `json:"cases"`
}

type policyCase struct {
	ID                string              `json:"id"`
	RequestEnvironment string             `json:"request_environment"`
	ExistingInstances []existingInstance   `json:"existing_instances"`
	Expected          *expectedDecision    `json:"expected"`
}

type existingInstance struct {
	Environment  string `json:"environment"`
	ProviderState string `json:"provider_state"`
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
	if err := readJSON(filepath.Join(root, "pilot", "capacity-policy.json"), &policy); err != nil {
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
	fmt.Fprintf(stdout, "capacity policy and %d cases are valid (declarative reference checks; not runtime-enforced)\n", len(cases.Cases))
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

func validateDocuments(policy policyDocument, cases casesDocument) []string {
	var errors []string
	add := func(condition bool, message string) {
		if !condition {
			errors = append(errors, message)
		}
	}

	add(policy.PolicyID == "supervised-pilot-vm-capacity", "policy_id must be supervised-pilot-vm-capacity")
	add(policy.PolicyVersion == 1, "policy_version must be 1")
	add(policy.MaxInstancesTotal == 2, "max_instances_total must be 2")

	staticPolicyErrors := 0
	if policy.Enforcement == nil {
		errors = append(errors, "enforcement must be an object")
		staticPolicyErrors++
	} else {
		add(policy.Enforcement.Mode == "declarative", "enforcement.mode must be declarative")
		add(policy.Enforcement.RuntimeEnforced != nil && !*policy.Enforcement.RuntimeEnforced, "enforcement.runtime_enforced must be false")
		add(strings.Contains(policy.Enforcement.Reason, "no VM-create operation"), "enforcement.reason must explain that the CLI has no VM-create operation")
	}
	expectedGroups := map[string]capacityGroup{
		"production": {
			MaxInstances:         1,
			AcceptedEnvironments: []string{"production"},
		},
		"shared_nonproduction": {
			MaxInstances:         1,
			AcceptedEnvironments: []string{"staging", "testing"},
		},
	}
	if !reflect.DeepEqual(policy.CapacityGroups, expectedGroups) {
		errors = append(errors, "capacity_groups must define one production slot and one shared staging/testing slot")
		staticPolicyErrors++
	} else {
		total := 0
		environments := make([]string, 0, 3)
		for _, group := range policy.CapacityGroups {
			total += group.MaxInstances
			environments = append(environments, group.AcceptedEnvironments...)
		}
		sort.Strings(environments)
		add(total == policy.MaxInstancesTotal, "sum of capacity groups must equal max_instances_total")
		add(reflect.DeepEqual(environments, []string{"production", "staging", "testing"}), "capacity groups must assign production, staging, and testing exactly once")
	}
	if policy.CreatePolicy == nil {
		errors = append(errors, "create_policy must be an object")
		staticPolicyErrors++
	} else {
		create := policy.CreatePolicy
		checkTrue := func(value *bool, field string) {
			add(value != nil && *value, field+" must be true")
		}
		checkTrue(create.ReserveSlotAtomicallyBeforeProviderCreate, "create_policy.reserve_slot_atomically_before_provider_create")
		checkTrue(create.ReservationCountsAsCapacity, "create_policy.reservation_counts_as_capacity")
		checkTrue(create.RejectDuplicateOrOccupiedSlot, "create_policy.reject_duplicate_or_occupied_slot")
		checkTrue(create.RejectIfTotalWouldExceedLimit, "create_policy.reject_if_total_would_exceed_limit")
		checkTrue(create.RejectUnsupportedEnvironment, "create_policy.reject_unsupported_environment")
		if create.Occupancy == nil {
			errors = append(errors, "create_policy.occupancy must be an object")
			staticPolicyErrors++
		} else {
			occupancy := create.Occupancy
			checkTrue(occupancy.CountEveryInstanceWithoutProviderConfirmedAbsenceOrDestruction, "create_policy.occupancy.count_every_instance_without_provider_confirmed_absence_or_destruction")
			checkTrue(occupancy.CountUnresolvedCreateOutcome, "create_policy.occupancy.count_unresolved_create_outcome")
			checkTrue(occupancy.CountUnresolvedDestroyOutcome, "create_policy.occupancy.count_unresolved_destroy_outcome")
			add(reflect.DeepEqual(occupancy.ReleaseOnlyAfterProviderConfirmation, []string{"absent", "destroyed"}), "create_policy.occupancy.release_only_after_provider_confirmation must be [absent, destroyed]")
		}
	}
	if cases.PolicyID != policy.PolicyID {
		errors = append(errors, "cases.policy_id must match policy.policy_id")
	}
	if len(cases.Cases) == 0 {
		errors = append(errors, "at least one capacity case is required")
	}

	seen := make(map[string]bool, len(cases.Cases))
	for index, testCase := range cases.Cases {
		label := fmt.Sprintf("cases[%d]", index)
		if testCase.ID == "" {
			errors = append(errors, label+".id must be a non-empty string")
			continue
		}
		if seen[testCase.ID] {
			errors = append(errors, "duplicate case id: "+testCase.ID)
			continue
		}
		seen[testCase.ID] = true
		if testCase.RequestEnvironment == "" {
			errors = append(errors, testCase.ID+".request_environment must be a non-empty string")
			continue
		}
		if testCase.ExistingInstances == nil {
			errors = append(errors, testCase.ID+".existing_instances must be an array")
			continue
		}
		invalidInstance := false
		for _, instance := range testCase.ExistingInstances {
			if instance.Environment == "" || instance.ProviderState == "" {
				invalidInstance = true
				break
			}
		}
		if invalidInstance {
			errors = append(errors, testCase.ID+".existing_instances entries need environment and provider_state strings")
			continue
		}
		if testCase.Expected == nil || testCase.Expected.Allowed == nil || testCase.Expected.Reason == "" {
			errors = append(errors, testCase.ID+".expected must include allowed and reason")
			continue
		}
		if staticPolicyErrors == 0 && policy.Enforcement != nil && policy.CreatePolicy != nil && policy.CreatePolicy.Occupancy != nil {
			actual := decide(policy, testCase.RequestEnvironment, testCase.ExistingInstances)
			if actual.Allowed != *testCase.Expected.Allowed || actual.Reason != testCase.Expected.Reason {
				errors = append(errors, fmt.Sprintf("%s: expected {allowed:%t reason:%q}, got {allowed:%t reason:%q}", testCase.ID, *testCase.Expected.Allowed, testCase.Expected.Reason, actual.Allowed, actual.Reason))
			}
		}
	}
	return errors
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
		if releasedStates[instance.ProviderState] {
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
