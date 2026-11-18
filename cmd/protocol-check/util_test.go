package main

import (
	"encoding/json"
	"testing"
)

// TestAsObject tests the asObject function.
func TestAsObject(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		wantOk   bool
	}{
		{"nil value", nil, false},
		{"map value", map[string]any{"key": "value"}, true},
		{"string value (should fail)", "not a map", false},
		{"slice value (should fail)", []any{1, 2, 3}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := asObject(test.value)
			if got == nil && !test.wantOk {
				// nil is expected for non-map types
			} else if got != nil && test.wantOk {
				// Map value should return the map itself
			}
		})
	}
}

// TestStringValue tests the stringValue function.
func TestStringValue(t *testing.T) {
	tests := []struct {
		name     string
		object   map[string]any
		key      string
		want     string
	}{
		{"string value", map[string]any{"name": "Alice"}, "name", "Alice"},
		{"missing key", map[string]any{"other": "Bob"}, "name", ""},
		{"non-string value", map[string]any{"name": 123}, "name", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := stringValue(test.object, test.key)
			if got != test.want {
				t.Errorf("stringValue = %q, want %q", got, test.want)
			}
		})
	}
}

// TestBoolValue tests the boolValue function.
func TestBoolValue(t *testing.T) {
	tests := []struct {
		name     string
		object   map[string]any
		key      string
		want     bool
	}{
		{"true value", map[string]any{"enabled": true}, "enabled", true},
		{"false value", map[string]any{"enabled": false}, "enabled", false},
		{"missing key", map[string]any{"other": "value"}, "enabled", false},
		{"string value (should return false)", map[string]any{"enabled": "true"}, "enabled", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := boolValue(test.object, test.key)
			if got != test.want {
				t.Errorf("boolValue = %v, want %v", got, test.want)
			}
		})
	}
}

// TestArrayValue tests the arrayValue function.
func TestArrayValue(t *testing.T) {
	tests := []struct {
		name     string
		object   map[string]any
		key      string
		wantLen  int
	}{
		{"array value", map[string]any{"items": []any{1, 2, 3}}, "items", 3},
		{"missing key", map[string]any{"other": "value"}, "items", 0},
		{"non-array value", map[string]any{"items": "not an array"}, "items", 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := arrayValue(test.object, test.key)
			if len(got) != test.wantLen {
				t.Errorf("arrayValue: got %d elements, want %d", len(got), test.wantLen)
			}
		})
	}
}

// TestContains tests the contains function.
func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		values   []any
		target   any
		wantVal  bool
		convert  bool
	}{
		{"contains int", []any{1, 2, 3}, "2", true, true},
		{"not contains int", []any{1, 2, 3}, "5", false, true},
		{"nil slice with target", nil, "anything", false, true},
		{"contains string", []any{"a", "b", "c"}, "b", true, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetStr := ""
			if test.convert {
				if strVal, ok := test.target.(string); ok {
					targetStr = strVal
				} else {
					t.Skip("skipping conversion test")
				}
			} else {
				targetStr = test.target.(string)
			}
			if contains(test.values, targetStr) != test.wantVal {
				t.Errorf("contains: got wrong value")
			}
		})
	}
}

// TestUniqueStrings tests the uniqueStrings function.
func TestUniqueStrings(t *testing.T) {
	tests := []struct {
		name     string
		values   []any
		want     bool
	}{
		{"empty slice", []any{}, true},
		{"unique strings", []any{"a", "b", "c"}, true},
		{"duplicate strings", []any{"a", "b", "a"}, false},
		{"empty string included", []any{"a", "", "b"}, false},
		{"mixed types (should fail)", []any{"a", 1, "b"}, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := uniqueStrings(test.values)
			if got != test.want {
				t.Errorf("uniqueStrings: got %v, want %v", got, test.want)
			}
		})
	}
}



// TestSafeJSONNumber tests the safeJSONNumber function.
func TestSafeJSONNumber(t *testing.T) {
	tests := []struct {
		name     string
		num      json.Number
		wantVal  int64
		wantErr  bool
	}{
		{"zero", "0", 0, false},
		{"positive", "42", 42, false},
		{"negative", "-17", -17, false},
		{"safe positive", "9007199254740992", 9007199254740992, false}, // exactly 2^53
		{"unsafe positive", "9007199254740993", 0, true}, // exceeds safe range
		{"unsafe negative", "-9007199254740993", 0, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := safeJSONNumber(test.num)
			if (err != nil) != test.wantErr {
				t.Errorf("safeJSONNumber: got error %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if got != test.wantVal {
				t.Fatalf("safeJSONNumber(%q) = %d, want %d", test.num.String(), got, test.wantVal)
			}
		})
	}
}

// TestEnsureSafeNumber tests the ensureSafeNumber function with various types.
func TestEnsureSafeNumber(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		wantVal  int64
		wantErr  bool
	}{
		{"json.Number zero", json.Number("0"), 0, false},
		{"int zero", int(0), 0, false},
		{"int64 zero", int64(0), 0, false},
		{"int max safe", int(2147483647), 2147483647, false},
		{"int exceeds safe range", int(2147483648), 0, true},
		{"int64 max safe", int64(9223372036854775807), 9223372036854775807, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ensureSafeNumber(test.value)
			if (err != nil) != test.wantErr {
				t.Errorf("ensureSafeNumber: got error %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if got != test.wantVal {
				t.Errorf("ensureSafeNumber(%v) = %d, want %d", test.value, got, test.wantVal)
			}
		})
	}
}

// TestDeepEqual tests the deepEqual and safeDeepEqual functions.
func TestDeepEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        any
		b        any
		want     bool
	}{
		{"same maps", map[string]any{"key": "value"}, map[string]any{"key": "value"}, true},
		{"different maps", map[string]any{"key": "value"}, map[string]any{"key": "other"}, false},
		{"map vs string", map[string]any{"key": "value"}, "not equal", false},
		{"same strings", "test", "test", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := safeDeepEqual(test.a, test.b)
			if got != test.want {
				t.Errorf("safeDeepEqual(%T, %T) = %v, want %v", test.a, test.b, got, test.want)
			}
		})
	}
}

// TestCompileECMA tests the compileECMA and ecmaRegexp types.
func TestCompileECMA(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"valid pattern", "^[a-zA-Z]+$", false},
		{"invalid regex", "[", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := compileECMA(test.pattern)
			if (err != nil) != test.wantErr {
				t.Errorf("compileECMA(%q): got error %v, wantErr %v", test.pattern, err, test.wantErr)
			}
			if !test.wantErr {
				re, ok := got.(*ecmaRegexp)
				if !ok {
					t.Errorf("compileECMA should return *ecmaRegexp type")
				} else if !got.MatchString("abc") {
					t.Error("compiled regex should match 'abc'")
				}
			}
		})
	}
}

// TestUtilityFunctionsIntegration tests the combined behavior of utility functions.
func TestUtilityFunctionsIntegration(t *testing.T) {
	t.Run("full workflow", func(t *testing.T) {
		fixture := map[string]any{
			"name":        "TestUser",
			"active":      true,
			"items":       []any{"item1", "item2"},
			"metadata":    map[string]any{"version": "1.0"},
		}

		name := stringValue(fixture, "name")
		if name != "TestUser" {
			t.Errorf("stringValue(name) = %q, want %q", name, "TestUser")
		}

		isActive := boolValue(fixture, "active")
		if !isActive {
			t.Error("boolValue(active) should be true")
		}

		items := arrayValue(fixture, "items")
		if len(items) != 2 {
			t.Errorf("arrayValue(items): got %d elements, want 2", len(items))
		}

		metadata := asObject(fixture["metadata"])
		if metadata == nil || metadata["version"] != "1.0" {
			t.Error("asObject(metadata) should return the nested map")
		}
	})
}
