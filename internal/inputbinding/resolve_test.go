package inputbinding

import (
	"encoding/json"
	"testing"
)

func TestResolveCopiesEarlierSucceededOutputIntoAbsentField(t *testing.T) {
	steps := []Step{
		{StepID: "create", Status: "succeeded", Output: json.RawMessage(`{"instance":{"id":"vm-1"}}`)},
		{StepID: "configure", Status: "pending", Input: json.RawMessage(`{"region":"sea"}`), Binding: []Binding{{TargetPointer: "/instance_id", SourceStepID: "create", SourcePointer: "/instance/id"}}},
	}
	got, err := Resolve(steps, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"instance_id":"vm-1","region":"sea"}` {
		t.Fatalf("unexpected resolved input: %s", got)
	}
}

func TestResolveRejectsNonEarlierOrIncompleteSources(t *testing.T) {
	for name, status := range map[string]string{"pending": "pending", "running": "running", "unknown": "outcome_unknown"} {
		t.Run(name, func(t *testing.T) {
			steps := []Step{
				{StepID: "create", Status: status, Output: json.RawMessage(`{"id":"vm-1"}`)},
				{StepID: "configure", Status: "pending", Input: json.RawMessage(`{}`), Binding: []Binding{{TargetPointer: "/id", SourceStepID: "create", SourcePointer: "/id"}}},
			}
			if _, err := Resolve(steps, 1); err == nil {
				t.Fatal("Resolve accepted a source that had not succeeded")
			}
		})
	}
}

func TestResolveRejectsOverwritesOverlapsAndBadPointers(t *testing.T) {
	base := []Step{
		{StepID: "create", Status: "succeeded", Output: json.RawMessage(`{"id":"vm-1","region":"sea"}`)},
		{StepID: "configure", Status: "pending", Input: json.RawMessage(`{"id":"planned"}`), Binding: []Binding{{TargetPointer: "/id", SourceStepID: "create", SourcePointer: "/id"}}},
	}
	if _, err := Resolve(base, 1); err == nil {
		t.Fatal("Resolve overwrote a planned input value")
	}

	base[1].Input = json.RawMessage(`{}`)
	base[1].Binding = []Binding{
		{TargetPointer: "/instance", SourceStepID: "create", SourcePointer: "/id"},
		{TargetPointer: "/instance/id", SourceStepID: "create", SourcePointer: "/id"},
	}
	if _, err := Resolve(base, 1); err == nil {
		t.Fatal("Resolve accepted overlapping destinations")
	}

	base[1].Binding = []Binding{{TargetPointer: "/bad~2escape", SourceStepID: "create", SourcePointer: "/id"}}
	if _, err := Resolve(base, 1); err == nil {
		t.Fatal("Resolve accepted an invalid JSON Pointer escape")
	}
}
