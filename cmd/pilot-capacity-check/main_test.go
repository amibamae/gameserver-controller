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
			instance:    existingInstance{Environment: "production", ProviderState: "create_outcome_unknown"},
		},
		{
			name:        "unknown shared-slot destroy",
			environment: "testing",
			instance:    existingInstance{Environment: "staging", ProviderState: "destroy_outcome_unknown"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decide(policy, test.environment, []existingInstance{test.instance})
			if got != (decision{Allowed: false, Reason: "slot_occupied"}) {
				t.Fatalf("decide() = %+v; want occupied slot denial", got)
			}
		})
	}
}

func TestConfirmedProviderAbsenceReleasesCapacity(t *testing.T) {
	policy, _ := loadPolicyFixtures(t)
	got := decide(policy, "production", []existingInstance{
		{Environment: "production", ProviderState: "absent"},
	})
	if got != (decision{Allowed: true, Reason: "slot_available"}) {
		t.Fatalf("decide() = %+v; want available slot", got)
	}
}

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
