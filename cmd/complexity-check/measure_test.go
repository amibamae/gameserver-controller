package main

import (
	"go/parser"
	"go/token"
	"testing"
)

func sampleMetrics(t *testing.T) map[string]funcMetrics {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "testdata/sample.go.txt", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]funcMetrics{}
	for _, m := range measureFile(fset, file) {
		byName[m.Name] = m
	}
	return byName
}

func TestMeasureFile(t *testing.T) {
	got := sampleMetrics(t)
	want := map[string]funcMetrics{
		"Simple":   {Complexity: 1},
		"Branchy":  {Complexity: 5, Nesting: 2, BoolOps: 1},
		"Deep":     {Complexity: 5, Nesting: 4},
		"Chain":    {Complexity: 4, Nesting: 1},
		"Wide":     {Complexity: 5, BoolOps: 4},
		"Exempt":   {Complexity: 5, BoolOps: 4, Allowed: true},
		"T.Method": {Complexity: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("measured %d functions, want %d: %v", len(got), len(want), got)
	}
	for name, w := range want {
		g := got[name]
		if g.Complexity != w.Complexity || g.Nesting != w.Nesting || g.BoolOps != w.BoolOps || g.Allowed != w.Allowed {
			t.Errorf("%s = complexity %d, nesting %d, bool %d, allowed %t; want %d, %d, %d, %t",
				name, g.Complexity, g.Nesting, g.BoolOps, g.Allowed, w.Complexity, w.Nesting, w.BoolOps, w.Allowed)
		}
	}
}

func TestOverLimitsNamesEachLimit(t *testing.T) {
	m := funcMetrics{Complexity: 16, Nesting: 4, BoolOps: 4, BoolLine: 9}
	got := overLimits(m, limits{Complexity: 15, Nesting: 3, BoolOps: 3})
	want := []string{"complexity 16 > 15", "nesting 4 > 3", "4 boolean operators in one condition at line 9 > 3"}
	if len(got) != len(want) {
		t.Fatalf("reasons = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reason %d = %q, want %q", i, got[i], want[i])
		}
	}
	if within := overLimits(funcMetrics{Complexity: 15, Nesting: 3, BoolOps: 3}, limits{15, 3, 3}); len(within) != 0 {
		t.Errorf("values at the limits are reported: %q", within)
	}
}
