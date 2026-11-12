package main

import (
	"path/filepath"
	"testing"
)

// unitCreateFixtureCase loads the unit-creation conformance fixture and returns
// a fresh copy of the named case.
func unitCreateFixtureCase(t *testing.T, caseID string) (*protocolPackage, map[string]any) {
	t.Helper()
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := parseJSONFile(filepath.Join(root, "protocol", "conformance", "test-vectors", "execution-unit-create-exact-definition-no-autostart.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rawCase := range arrayValue(asObject(raw), "evaluations") {
		if c := asObject(rawCase); stringValue(c, "case_id") == caseID {
			return pkg, c
		}
	}
	t.Fatalf("unit-creation fixture has no case %q", caseID)
	return nil, nil
}

// TestUnitCreateChecksNotCoveredByFixture covers conditions that no case in the
// conformance fixture isolates. Each subtest starts from a case where the flag
// holds, changes one field, and expects the flag to flip.
func TestUnitCreateChecksNotCoveredByFixture(t *testing.T) {
	tests := []struct {
		name   string
		caseID string
		mutate func(c map[string]any)
		flag   string
		want   bool
	}{
		{
			name:   "definition checksum uses another algorithm",
			caseID: "created-stopped",
			mutate: func(c map[string]any) {
				asObject(asObject(asObject(c["request"])["unit_definition"])["checksum"])["algorithm"] = "sha512"
			},
			flag: "definition_verified",
		},
		{
			name:   "unit reference is not a systemd unit",
			caseID: "created-stopped",
			mutate: func(c map[string]any) {
				asObject(asObject(c["request"])["unit_ref"])["resource_kind"] = "systemd-timer"
			},
			flag: "target_consistent",
		},
		{
			name:   "existing unit belongs to another target",
			caseID: "repeat-same-definition-running",
			mutate: func(c map[string]any) {
				asObject(asObject(c["existing_unit"])["target"])["server_id"] = "another-server"
			},
			flag: "existing_unit_compatible",
		},
		{
			name:   "resolved operation installed another definition",
			caseID: "reconcile-exact-created-unit",
			mutate: func(c map[string]any) {
				resolved := asObject(c["resolved_operation"])
				checksum := map[string]any{}
				for key, value := range asObject(resolved["definition_checksum"]) {
					checksum[key] = value
				}
				checksum["digest"] = "0000000000000000000000000000000000000000000000000000000000000000"
				resolved["definition_checksum"] = checksum
			},
			flag: "reconciliation_confirmed",
		},
		{
			name:   "resolved operation installed another definition requires an operator",
			caseID: "reconcile-exact-created-unit",
			mutate: func(c map[string]any) {
				resolved := asObject(c["resolved_operation"])
				resolved["definition_checksum"] = map[string]any{"algorithm": "sha256", "digest": "00"}
			},
			flag: "operator_required",
			want: true,
		},
		{
			name:   "retry requires the unit to be absent",
			caseID: "reconcile-terminal-no-effect-absence",
			mutate: func(c map[string]any) {
				asObject(c["unit_inspection"])["state"] = "stopped"
			},
			flag: "retry_permitted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg, c := unitCreateFixtureCase(t, test.caseID)
			if got := evaluateUnitCreateCase(pkg, c)[test.flag]; got != !test.want {
				t.Fatalf("baseline %s = %v, want %v", test.flag, got, !test.want)
			}
			test.mutate(c)
			if got := evaluateUnitCreateCase(pkg, c)[test.flag]; got != test.want {
				t.Fatalf("%s = %v, want %v", test.flag, got, test.want)
			}
		})
	}
}
