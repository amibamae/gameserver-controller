package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadPolicyFixtures(t *testing.T) (policyDocument, casesDocument) {
	t.Helper()
	var policy policyDocument
	if err := readJSON(filepath.Join("..", "..", "pilot", "capacity-policy.json"), &policy); err != nil {
		t.Fatalf("read policy fixture: %v", err)
	}
	var cases casesDocument
	if err := readJSON(filepath.Join("..", "..", "pilot", "capacity-policy-cases.json"), &cases); err != nil {
		t.Fatalf("read cases fixture: %v", err)
	}
	return policy, cases
}

func TestPublishedCapacityPolicyCases(t *testing.T) {
	policy, cases := loadPolicyFixtures(t)
	if validationErrors := validateDocuments(policy, cases); len(validationErrors) != 0 {
		t.Fatalf("capacity policy validation failed: %s", strings.Join(validationErrors, "; "))
	}
}

func TestDecisionKeepsUnresolvedInstancesOccupied(t *testing.T) {
	policy, _ := loadPolicyFixtures(t)
	tests := []struct {
		name        string
		environment string
		instance    existingInstance
	}{
		{
			name:        "unknown production create",
			environment: "production",
			instance:    existingInstance{Environment: "production", ProviderState: "create_outcome_unknown", Managed: boolPointer(true)},
		},
		{
			name:        "unknown shared-slot destroy",
			environment: "testing",
			instance:    existingInstance{Environment: "staging", ProviderState: "destroy_outcome_unknown", Managed: boolPointer(true)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decide(policy, test.environment, []existingInstance{test.instance})
			if got.Allowed || !matchesExpectedReason(got.Reason, "capacity_reached") {
				t.Fatalf("decide() = %+v; want occupied slot denial", got)
			}
		})
	}
}

func TestConfirmedProviderAbsenceReleasesCapacity(t *testing.T) {
	policy, _ := loadPolicyFixtures(t)
	got := decide(policy, "production", []existingInstance{
		{Environment: "production", ProviderState: "absent", Managed: boolPointer(true)},
	})
	if got != (decision{Allowed: true, Reason: "slot_available"}) {
		t.Fatalf("decide() = %+v; want available slot", got)
	}
}

func TestUnmanagedProductionDoesNotConsumePilotSlot(t *testing.T) {
	policy, _ := loadPolicyFixtures(t)
	got := decide(policy, "staging", []existingInstance{
		{Environment: "production", ProviderState: "running", Managed: boolPointer(false)},
	})
	if got != (decision{Allowed: true, Reason: "slot_available"}) {
		t.Fatalf("decide() = %+v; unmanaged production should remain outside managed capacity", got)
	}
}

func TestConfiguredLimitCanBeChangedInPolicyFile(t *testing.T) {
	policy, cases := loadPolicyFixtures(t)
	policy.MaxInstancesTotal = 2
	policy.PolicyVersion++
	if validationErrors := validateDocuments(policy, cases); len(validationErrors) != 0 {
		t.Fatalf("changing the configured limit to two should remain valid: %s", strings.Join(validationErrors, "; "))
	}
	got := decide(policy, "staging", []existingInstance{
		{Environment: "production", ProviderState: "running", Managed: boolPointer(true)},
	})
	if got != (decision{Allowed: true, Reason: "slot_available"}) {
		t.Fatalf("decision with a two-VM policy = %+v; want the second managed slot available", got)
	}
}

func TestConfiguredLimitCannotExceedDeclaredGroupSlots(t *testing.T) {
	policy, cases := loadPolicyFixtures(t)
	policy.MaxInstancesTotal = 3
	policy.PolicyVersion++
	if validationErrors := validateDocuments(policy, cases); len(validationErrors) == 0 {
		t.Fatal("limit greater than declared capacity groups passed validation")
	}
}

func boolPointer(value bool) *bool { return &value }

func TestReadJSONRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"known":"value","unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Known string `json:"known"`
	}
	if err := readJSON(path, &decoded); err == nil {
		t.Fatal("readJSON accepted an unknown field")
	}
}
