package main

import "testing"

func TestBackupCaptureAcceptanceRequiresDispatchAndExactBinding(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(map[string]any)
		dispatch bool
	}{
		{name: "valid capture", mutate: func(map[string]any) {}, dispatch: true},
		{name: "another invocation server", mutate: func(e map[string]any) {
			e["request_context_overrides"] = map[string]any{"server_id": "another-server"}
		}},
		{name: "another preparation backup", mutate: func(e map[string]any) {
			e["preparation_result_overrides"] = map[string]any{"backup_id": "another-backup"}
		}},
		{name: "another output capture", mutate: func(e map[string]any) {
			e["output_overrides"] = map[string]any{"capture_id": "another-capture"}
		}, dispatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg, fixture, input, evaluation := backupCaptureFixtureCase(t, "valid-offline-capture")
			tc.mutate(evaluation)
			result := evaluateBackupCaptureCase(pkg, fixture, input, evaluation)
			if result["dispatch_permitted"] != tc.dispatch {
				t.Fatalf("dispatch = %v, want %t", result["dispatch_permitted"], tc.dispatch)
			}
			accepted := tc.name == "valid capture"
			if result["capture_accepted"] != accepted {
				t.Fatalf("accepted = %v, want %t", result["capture_accepted"], accepted)
			}
		})
	}
}
