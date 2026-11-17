package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestNetworkProfileUsesLastAdapterRegistration(t *testing.T) {
	pkg, err := loadPackage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const inspect = "integration.dns.record.inspect"
	const upsert = "integration.dns.record.upsert"
	const deleteRecord = "integration.dns.record.delete"
	adapter := func(environment string, capabilities ...any) map[string]any {
		return map[string]any{"adapter_id": "fixture-dns", "environment_id": environment, "capabilities": capabilities}
	}
	for _, tc := range []struct {
		name string
		last map[string]any
		want []any
	}{
		{"replacement capability set", adapter("fixture-env", nil, inspect, 4), requirementsJSON([]map[string]string{
			{"role": "dns", "capability": deleteRecord},
			{"role": "dns", "capability": upsert},
		})},
		{"replacement adapter environment", adapter("other-env", inspect, upsert, deleteRecord), requirementsJSON([]map[string]string{
			{"role": "dns", "capability": deleteRecord},
			{"role": "dns", "capability": inspect},
			{"role": "dns", "capability": upsert},
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := evaluateNetworkProfileRequirements(pkg, map[string]any{
				"input": map[string]any{"evaluations": []any{map[string]any{
					"network_profile":     map[string]any{"join_mode": "direct_dns", "dns_adapter_id": "fixture-dns"},
					"environment_id":      "fixture-env",
					"registered_adapters": []any{adapter("fixture-env", inspect, upsert, deleteRecord), tc.last},
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			evaluation := asObject(arrayValue(result, "evaluations")[0])
			if !reflect.DeepEqual(evaluation["missing_capabilities"], tc.want) || boolValue(evaluation, "planning_permitted") {
				t.Fatalf("unexpected evaluation: %#v", evaluation)
			}
		})
	}
}
