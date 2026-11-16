package main

import "testing"

func TestSameObjectRefSet(t *testing.T) {
	first := map[string]any{"object_id": "first", "environment_id": "test"}
	second := map[string]any{"object_id": "second", "environment_id": "test"}
	third := map[string]any{"object_id": "third", "environment_id": "test"}
	unsupported := make(chan struct{})
	cases := []struct {
		name        string
		left, right []any
		want        bool
	}{
		{name: "empty", want: true},
		{name: "same refs in different order", left: []any{first, second}, right: []any{second, first}, want: true},
		{name: "different refs", left: []any{first, second}, right: []any{first, third}},
		{name: "different lengths", left: []any{first}, right: []any{first, second}},
		{name: "duplicate left", left: []any{first, first}, right: []any{first, second}},
		{name: "duplicate right", left: []any{first, second}, right: []any{first, first}},
		{name: "invalid left", left: []any{unsupported}, right: []any{first}},
		{name: "invalid right", left: []any{first}, right: []any{unsupported}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameObjectRefSet(tc.left, tc.right); got != tc.want {
				t.Errorf("sameObjectRefSet = %t, want %t", got, tc.want)
			}
		})
	}
}
