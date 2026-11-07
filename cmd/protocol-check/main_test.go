package main

import (
	"strings"
	"testing"
)

func TestParseJSONRejectsDuplicateObjectMembers(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "top-level", input: `{"id":1,"id":2}`},
		{name: "nested", input: `{"outer":{"id":1,"id":2}}`},
		{name: "escaped-equivalent-name", input: `{"id":1,"\u0069d":2}`},
		{name: "inside-array", input: `{"items":[{"id":1,"id":2}]}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseJSON([]byte(test.input), "test")
			if err == nil {
				t.Fatal("parseJSON accepted duplicate object member names")
			}
			if !strings.Contains(err.Error(), "duplicate JSON object member: id") {
				t.Fatalf("parseJSON error = %q, want duplicate member error", err)
			}
		})
	}
}

func TestParseJSONEnforcesSafeIntegerRange(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "zero", input: `0`},
		{name: "safe-negative", input: `-9007199254740991`},
		{name: "safe-positive", input: `9007199254740991`},
		{name: "safe-integer-with-decimal", input: `9007199254740991.0`},
		{name: "safe-integer-with-exponent", input: `9.007199254740991e15`},
		{name: "fractional-number", input: `1.25`},
		{name: "unsafe-positive", input: `9007199254740992`, wantErr: true},
		{name: "unsafe-negative", input: `-9007199254740992`, wantErr: true},
		{name: "unsafe-integer-with-decimal", input: `9007199254740992.0`, wantErr: true},
		{name: "unsafe-integer-with-exponent", input: `9.007199254740992e15`, wantErr: true},
		{
			name:    "unsafe-integer-nested-in-array",
			input:   `{"items":[9007199254740992]}`,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseJSON([]byte(test.input), "test")
			if test.wantErr && err == nil {
				t.Fatal("parseJSON accepted an unsafe integer")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("parseJSON returned unexpected error: %v", err)
			}
			if test.wantErr && !strings.Contains(err.Error(), "integer exceeds the interoperable JSON safe-integer range") {
				t.Fatalf("parseJSON error = %q, want safe-integer range error", err)
			}
		})
	}
}

func TestParseTimeRequiresUTCZuluTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "utc-seconds", input: "2026-11-06T12:34:56Z"},
		{name: "utc-nanoseconds", input: "2026-11-06T12:34:56.123456789Z"},
		{name: "numeric-offset", input: "2026-11-06T12:34:56+00:00", wantErr: true},
		{name: "nonzero-offset", input: "2026-11-06T12:34:56-08:00", wantErr: true},
		{name: "invalid-date", input: "2026-02-30T12:34:56Z", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseTime(test.input)
			if test.wantErr && err == nil {
				t.Fatal("parseTime accepted an invalid timestamp")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("parseTime returned unexpected error: %v", err)
			}
		})
	}
}

func TestSecretReferenceSchemaValidation(t *testing.T) {
	pkg, err := loadPackage("../..")
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}

	const schemaID = "urn:gameserver-controller:0.21.0-draft:common:secret-reference"
	tests := []struct {
		name      string
		value     map[string]any
		wantValid bool
	}{
		{
			name: "opaque reference",
			value: map[string]any{
				"secret_ref": "vault://minecraft/production/backup",
				"purpose":    "backup.authentication",
			},
			wantValid: true,
		},
		{
			name: "missing required purpose",
			value: map[string]any{
				"secret_ref": "vault://minecraft/production/backup",
			},
		},
		{
			name: "missing required reference",
			value: map[string]any{
				"purpose": "backup.authentication",
			},
		},
		{
			name: "secret reference must be a string",
			value: map[string]any{
				"secret_ref": true,
				"purpose":    "backup.authentication",
			},
		},
		{
			name: "purpose must be a string",
			value: map[string]any{
				"secret_ref": "vault://minecraft/production/backup",
				"purpose":    true,
			},
		},
		{
			name: "permission scope entries must be strings",
			value: map[string]any{
				"secret_ref":       "vault://minecraft/production/backup",
				"purpose":          "backup.authentication",
				"permission_scope": []any{true},
			},
		},
		{
			name: "unknown critical field is rejected",
			value: map[string]any{
				"secret_ref":         "vault://minecraft/production/backup",
				"purpose":            "backup.authentication",
				"critical_extension": true,
			},
		},
		{
			name: "literal secret is not a reference",
			value: map[string]any{
				"secret_ref": "plaintext-password",
				"purpose":    "backup.authentication",
			},
		},
		{
			name: "raw secret property is rejected",
			value: map[string]any{
				"secret_ref": "vault://minecraft/production/backup",
				"purpose":    "backup.authentication",
				"secret":     "plaintext-password",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := pkg.validateSchema(schemaID, test.value)
			if test.wantValid && err != nil {
				t.Fatalf("secret reference schema rejected valid opaque reference: %v", err)
			}
			if !test.wantValid && err == nil {
				t.Fatal("secret reference schema accepted invalid input")
			}
		})
	}
}
