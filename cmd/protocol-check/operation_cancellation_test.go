package main

import "testing"

func TestOperationCancellationRejectsUnresolvedFixturePreconditions(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested bool
		initial   map[string]any
	}{
		{name: "not requested"},
		{name: "possible undispatched effects", requested: true, initial: map[string]any{"external_effect_possible": true}},
		{name: "provider already responded", requested: true, initial: map[string]any{"dispatch_recorded": true, "provider_response_received": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := evaluateOperationsCancellation(map[string]any{
				"cancellation_requested": test.requested,
				"initial":                test.initial,
			})
			const want = "cancellation fixture must describe an undispatched step or a dispatched step with unknown outcome"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
			if output != nil {
				t.Errorf("output = %v, want nil", output)
			}
		})
	}
}
