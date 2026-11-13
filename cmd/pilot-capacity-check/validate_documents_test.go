package main

import (
	"reflect"
	"testing"
)

// TestValidateDocumentsReportsEveryProblemInOrder pins the exact error list,
// in order, for documents that break each rule, so the checks can be
// reorganized without changing what an operator sees.
func TestValidateDocumentsReportsEveryProblemInOrder(t *testing.T) {
	falseValue := false
	t.Run("empty policy", func(t *testing.T) {
		got := validateDocuments(policyDocument{}, casesDocument{PolicyID: "other"})
		want := []string{
			"policy_id must be supervised-pilot-vm-capacity",
			"policy_version must be at least 2",
			"max_instances_total must be greater than zero",
			"enforcement must be an object",
			"ownership_scope must be an object",
			"capacity_groups must define one production slot and one shared staging/testing slot",
			"create_policy must be an object",
			"cases.policy_id must match policy.policy_id",
			"at least one capacity case is required",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("errors =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("every nested flag wrong", func(t *testing.T) {
		policy, cases := loadPolicyFixtures(t)
		policy.Enforcement = &enforcementPolicy{Mode: "declarative", RuntimeEnforced: &falseValue, Reason: "manual"}
		policy.OwnershipScope = &ownershipScope{}
		policy.MaxInstancesTotal = 3
		policy.CreatePolicy = &createPolicy{Occupancy: &occupancyPolicy{ReleaseOnlyAfterProviderConfirmation: []string{"absent"}}}
		got := validateDocuments(policy, cases)
		want := []string{
			"enforcement.mode must be runtime",
			"enforcement.runtime_enforced must be true",
			"enforcement.reason must identify the runtime provision command",
			"ownership_scope.capacity_counts_only_controller_owned_instances must be true",
			"ownership_scope.unmanaged_production_may_remain_live must be true",
			"ownership_scope.production_adoption_is_optional must be true",
			"ownership_scope.provider_created_instances_are_controller_managed must be true",
			"max_instances_total cannot exceed the sum of capacity-group slots",
			"create_policy.reserve_slot_atomically_before_provider_create must be true",
			"create_policy.reservation_counts_as_capacity must be true",
			"create_policy.reject_duplicate_or_occupied_slot must be true",
			"create_policy.reject_if_total_would_exceed_limit must be true",
			"create_policy.reject_unsupported_environment must be true",
			"create_policy.occupancy.count_every_managed_instance_without_provider_confirmed_absence_or_destruction must be true",
			"create_policy.occupancy.count_unresolved_create_outcome must be true",
			"create_policy.occupancy.count_unresolved_destroy_outcome must be true",
			"create_policy.occupancy.release_only_after_provider_confirmation must be [absent, destroyed]",
		}
		if len(got) < len(want) || !reflect.DeepEqual(got[:len(want)], want) {
			t.Fatalf("errors =\n%q\nwant prefix\n%q", got, want)
		}
	})
	t.Run("missing occupancy skips decisions", func(t *testing.T) {
		policy, cases := loadPolicyFixtures(t)
		policy.CreatePolicy.Occupancy = nil
		got := validateDocuments(policy, cases)
		if !reflect.DeepEqual(got, []string{"create_policy.occupancy must be an object"}) {
			t.Fatalf("errors = %q", got)
		}
	})
	t.Run("malformed cases", func(t *testing.T) {
		policy, _ := loadPolicyFixtures(t)
		allowed := true
		managed := true
		cases := casesDocument{PolicyID: policy.PolicyID, Cases: []policyCase{
			{},
			{ID: "dup", RequestEnvironment: "staging", RequestAction: "create", ExistingInstances: []existingInstance{}, Expected: &expectedDecision{Allowed: &allowed, Reason: "ok"}},
			{ID: "dup"},
			{ID: "no-environment"},
			{ID: "bad-action", RequestEnvironment: "staging", RequestAction: "destroy"},
			{ID: "nil-instances", RequestEnvironment: "staging", RequestAction: "create"},
			{ID: "bad-instance", RequestEnvironment: "staging", RequestAction: "adopt", ExistingInstances: []existingInstance{{Environment: "staging", Managed: &managed}}},
			{ID: "no-expected", RequestEnvironment: "staging", RequestAction: "create", ExistingInstances: []existingInstance{}},
		}}
		got := validateDocuments(policy, cases)
		want := []string{
			"cases[0].id must be a non-empty string",
			`dup: expected {allowed:true reason:"ok"}, got {allowed:true reason:"slot_available"}`,
			"duplicate case id: dup",
			"no-environment.request_environment must be a non-empty string",
			"bad-action.request_action must be create or adopt",
			"nil-instances.existing_instances must be an array",
			"bad-instance.existing_instances entries need environment and provider_state strings",
			"no-expected.expected must include allowed and reason",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("errors =\n%q\nwant\n%q", got, want)
		}
	})
}
