package pilotcapacity

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// policyWith decodes the embedded policy, applies change to its JSON object,
// and decodes the result as a policy.
func policyWith(t *testing.T, change func(map[string]any)) (Policy, error) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(embeddedPolicy, &document); err != nil {
		t.Fatal(err)
	}
	change(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return DecodePolicy(bytes.NewReader(encoded))
}

func object(document map[string]any, path ...string) map[string]any {
	for _, key := range path {
		document = document[key].(map[string]any)
	}
	return document
}

// TestPolicyRulesRejectUnsafeDocuments breaks one rule of the embedded policy
// per case and pins the reason it is rejected.
func TestPolicyRulesRejectUnsafeDocuments(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"another policy ID", func(d map[string]any) { d["policy_id"] = "other" }, "invalid identity, version, or total limit"},
		{"policy version 1", func(d map[string]any) { d["policy_version"] = 1 }, "invalid identity, version, or total limit"},
		{"zero total limit", func(d map[string]any) { d["max_instances_total"] = 0 }, "invalid identity, version, or total limit"},
		{"unknown enforcement mode", func(d map[string]any) { object(d, "enforcement")["mode"] = "advisory" }, "enforcement mode must be declarative or runtime"},
		{"runtime mode without runtime enforcement", func(d map[string]any) { object(d, "enforcement")["runtime_enforced"] = false }, "runtime enforcement mode requires runtime_enforced=true"},
		{"runtime mode with enforcement unset", func(d map[string]any) { delete(object(d, "enforcement"), "runtime_enforced") }, "runtime enforcement mode requires runtime_enforced=true"},
		{"declarative mode claiming runtime enforcement", func(d map[string]any) { object(d, "enforcement")["mode"] = "declarative" }, "declarative mode requires runtime_enforced=false"},
		{"capacity counts unmanaged instances", func(d map[string]any) {
			object(d, "ownership_scope")["capacity_counts_only_controller_owned_instances"] = false
		}, "ownership scope must preserve"},
		{"production adoption required", func(d map[string]any) { object(d, "ownership_scope")["production_adoption_is_optional"] = false }, "ownership scope must preserve"},
		{"create without a prior reservation", func(d map[string]any) {
			object(d, "create_policy")["reserve_slot_atomically_before_provider_create"] = false
		}, "create and occupancy rules are incomplete"},
		{"unknown creates not counted", func(d map[string]any) {
			object(d, "create_policy", "occupancy")["count_unresolved_create_outcome"] = false
		}, "create and occupancy rules are incomplete"},
		{"release before confirmed destruction", func(d map[string]any) {
			object(d, "create_policy", "occupancy")["release_only_after_provider_confirmation"] = []any{"absent"}
		}, "create and occupancy rules are incomplete"},
		{"no capacity groups", func(d map[string]any) { d["capacity_groups"] = map[string]any{} }, "capacity groups are required"},
		{"group without capacity", func(d map[string]any) { object(d, "capacity_groups", "production")["max_instances"] = 0 }, `invalid capacity group "production"`},
		{"group without environments", func(d map[string]any) {
			object(d, "capacity_groups", "production")["accepted_environments"] = []any{}
		}, `invalid capacity group "production"`},
		{"environment in two groups", func(d map[string]any) {
			object(d, "capacity_groups", "production")["accepted_environments"] = []any{"production", "staging"}
		}, `empty or duplicate environment "staging"`},
		{"empty environment", func(d map[string]any) {
			object(d, "capacity_groups", "production")["accepted_environments"] = []any{""}
		}, `empty or duplicate environment ""`},
		{"total above the group capacities", func(d map[string]any) { d["max_instances_total"] = 3 }, "total limit exceeds the sum of group capacities"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := policyWith(t, test.change)
			if !errors.Is(err, ErrInvalidPolicy) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodePolicy error = %v, want ErrInvalidPolicy containing %q", err, test.want)
			}
		})
	}
}

func TestPolicyRulesAcceptDeclarativeAndEmbeddedPolicies(t *testing.T) {
	policy, err := policyWith(t, func(map[string]any) {})
	if err != nil || !policy.RuntimeEnforced || policy.MaxInstancesTotal != 2 || len(policy.CapacityGroups) != 2 {
		t.Fatalf("embedded policy = %+v, %v", policy, err)
	}
	if !policy.ReleasedStates["absent"] || !policy.ReleasedStates["destroyed"] || len(policy.ReleasedStates) != 2 {
		t.Fatalf("released states = %v, want absent and destroyed", policy.ReleasedStates)
	}
	declarative, err := policyWith(t, func(d map[string]any) {
		object(d, "enforcement")["mode"] = "declarative"
		object(d, "enforcement")["runtime_enforced"] = false
		d["max_instances_total"] = 1
	})
	if err != nil || declarative.RuntimeEnforced || declarative.MaxInstancesTotal != 1 {
		t.Fatalf("declarative policy = %+v, %v", declarative, err)
	}
	reordered, err := policyWith(t, func(d map[string]any) {
		object(d, "create_policy", "occupancy")["release_only_after_provider_confirmation"] = []any{"destroyed", "absent"}
	})
	if err != nil || reordered.MaxInstancesTotal != 2 {
		t.Fatalf("release states in another order: %+v, %v", reordered, err)
	}
}
