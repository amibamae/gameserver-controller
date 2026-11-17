package main

import (
	"maps"
	"path/filepath"
	"testing"
)

func TestMonitoringPublishRejectsUnapprovedFieldsAndValues(t *testing.T) {
	root := filepath.Join("..", "..")
	pkg, err := loadPackage(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, direction, field string
		value                  any
	}{
		{"caller destination", "input", "destination", "https://example.invalid"},
		{"player identifier", "input", "player_id", "test-player"},
		{"raw diagnostics", "input", "message", "test diagnostics"},
		{"arbitrary event", "input", "event_type", "player.joined"},
		{"invalid event ID", "output", "event_id", "invalid"},
		{"invented outcome", "output", "outcome", "delivered"},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := "monitoring-event-publish-" + test.direction
			id := "urn:gameserver-controller:" + pkg.version + ":integrations:" + name
			example, err := parseJSONFile(filepath.Join(root, "protocol", "examples", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := pkg.validateSchema(id, example); err != nil {
				t.Fatalf("valid example: %v", err)
			}
			modified := maps.Clone(asObject(example))
			modified[test.field] = test.value
			if err := pkg.validateSchema(id, modified); err == nil {
				t.Fatalf("schema accepted an unapproved value for %s", test.field)
			}
		})
	}
}
