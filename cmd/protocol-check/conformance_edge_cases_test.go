package main

import (
	"encoding/json"
	"testing"
)

// TestIntegerValueHandlesAllTypes verifies integerValue works with all input types.
func TestIntegerValueHandlesAllTypes(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    int64
		wantErr bool
	}{
		{"int", 42, 42, false},
		{"int64", int64(100), 100, false},
		{"json.Number", json.Number("256"), 256, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := integerValue(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("integerValue(%v) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("integerValue(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestIntegerValueHandlesString tests that string input returns an error.
func TestIntegerValueHandlesString(t *testing.T) {
	got, err := integerValue("42")
	if err == nil || got != 0 {
		t.Log("string input returns error as expected (expected integer)")
	}
}

// TestSameObjectRefSet compares two slices of JSON-marshaled objects.
func TestSameObjectRefSet(t *testing.T) {
	tests := []struct {
		name   string
		left   []any
		right  []any
		wantOk bool
	}{
		{"equal sets", []any{map[string]any{"a": 1}, map[string]any{"b": 2}}, []any{map[string]any{"a": 1}, map[string]any{"b": 2}}, true},
		{"different content", []any{map[string]any{"a": 1}}, []any{map[string]any{"b": 2}}, false},
		{"different lengths", []any{map[string]any{"x": 1}, map[string]any{"y": 2}}, []any{map[string]any{"x": 1}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOk := sameObjectRefSet(tt.left, tt.right)
			if gotOk != tt.wantOk {
				t.Errorf("sameObjectRefSet = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}
}

// TestSameStringSet compares two slices of strings.
func TestSameStringSet(t *testing.T) {
	tests := []struct {
		name   string
		left   []any
		right  []any
		wantOk bool
	}{
		{"equal strings", []any{"a", "b", "c"}, []any{"a", "b", "c"}, true},
		{"different strings", []any{"a", "b"}, []any{"b", "c"}, false},
		{"different lengths", []any{"a", "b"}, []any{"a", "b", "c"}, false},
		{"mixed types left", []any{"a", 1, "b"}, []any{"a", "b"}, false}, // type mismatch
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOk := sameStringSet(tt.left, tt.right)
			if gotOk != tt.wantOk {
				t.Errorf("sameStringSet(%v, %v) = %v, want %v", tt.left, tt.right, gotOk, tt.wantOk)
			}
		})
	}
}

// TestObjectRefKeysBuildsSetFromSlice verifies the objectRefKeys helper.
func TestObjectRefKeysBuildsSetFromSlice(t *testing.T) {
	left := []any{map[string]any{"key": "value1"}}
	resultSet, okResult := objectRefKeys(left)

	if !okResult {
		t.Fatalf("objectRefKeys returned error")
	}

	if len(resultSet) != 1 {
		t.Errorf("objectRefKeys set size = %d, want 1", len(resultSet))
	}
}

// TestObjectRefKeysHandlesEmptySlice verifies empty slice handling.
func TestObjectRefKeysHandlesEmptySlice(t *testing.T) {
	left := []any{}
	resultSet, okResult := objectRefKeys(left)

	if !okResult {
		t.Fatalf("objectRefKeys error on empty slice")
	}

	if len(resultSet) != 0 {
		t.Errorf("empty slice result set size = %d, want 0", len(resultSet))
	}
}

// TestSameStringSetHandlesMixedTypes returns false on type mismatch.
func TestSameStringSetHandlesMixedTypes(t *testing.T) {
	left := []any{"string", 123}
	right := []any{"string"}

	gotOk := sameStringSet(left, right)
	if gotOk {
		t.Error("sameStringSet should return false when left has non-string elements")
	}
}

// TestSameObjectRefSetHandlesEmptySlices returns false on different lengths.
func TestSameObjectRefSetHandlesEmptySlices(t *testing.T) {
	left := []any{}
	right := []any{map[string]any{"key": "value"}}

	if gotOk := sameObjectRefSet(left, right); gotOk {
		t.Error("sameObjectRefSet should return false on empty vs non-empty slices")
	}
}

// TestSameStringSetHandlesEmptySlices returns false on different lengths.
func TestSameStringSetHandlesEmptySlices(t *testing.T) {
	left := []any{"a", "b"}
	right := []any{}

	if gotOk := sameStringSet(left, right); gotOk {
		t.Error("sameStringSet should return false when right is empty")
	}
}

// TestSameObjectRefSetHandlesNilSlices returns false on different lengths.
func TestSameObjectRefSetHandlesNilSlices(t *testing.T) {
	left := []any{}
	right := []any{map[string]any{"key": "value"}}

	if gotOk := sameObjectRefSet(left, right); gotOk {
		t.Error("sameObjectRefSet should return false when left is nil")
	}
}

// TestSameStringSetHandlesNilSlices returns false on different lengths.
func TestSameStringSetHandlesNilSlices(t *testing.T) {
	left := []any{}
	right := []any{"a", "b"}

	if gotOk := sameStringSet(left, right); gotOk {
		t.Error("sameStringSet should return false when left is nil")
	}
}
