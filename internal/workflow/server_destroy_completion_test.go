package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestServerDestroyCompletionEvidenceOrder(t *testing.T) {
	tests := []struct {
		name   string
		broken int
		output string
		want   string
	}{
		{"missing initial inspection wins over later evidence", 0, `{}`, "server.destroy lacks exact pre-destruction provider evidence"},
		{"runtime must be stopped or absent", 1, `"state":"running"`, "server.destroy lacks exact stopped-runtime evidence"},
		{"provider must complete destruction", 2, `"outcome":"accepted"`, "server.destroy lacks an exact completed provider destruction"},
		{"provider must confirm absence", 3, `"state":"active"`, "server.destroy lacks exact provider-absence verification"},
		{"absence must follow completed destruction", 4, "", "provider absence observation predates destroy completion: observation timestamp moved backwards"},
		{"exact evidence completes", -1, "", ""},
	}
	fixture := newServerDestroyFixture(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			operation := destroyOperationAfter(fixture, 2)
			start := *operation.Steps[0].StartedAt
			operation.Steps[2].StartedAt = &start
			operation.Steps[3].StartedAt = &start
			operation.Steps[2].Status = operation.Steps[0].Status
			operation.Steps[3].Status = operation.Steps[0].Status
			operation.Steps[2].Output = json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":"completed","state":"destroyed","observed_at":%q}`, startTestInstance, start.Add(time.Second).Format(time.RFC3339Nano)))
			operation.Steps[3].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", start.Add(2*time.Second))
			switch tt.broken {
			case 0:
				operation.Steps[0].Output = json.RawMessage(tt.output)
				operation.Steps[1].Output = nil
			case 1, 3:
				step := &operation.Steps[tt.broken]
				old := `"state":"stopped"`
				if tt.broken == 3 {
					old = `"state":"destroyed"`
				}
				step.Output = json.RawMessage(strings.Replace(string(step.Output), old, tt.output, 1))
			case 2:
				step := &operation.Steps[2]
				step.Output = json.RawMessage(strings.Replace(string(step.Output), `"outcome":"completed"`, tt.output, 1))
			case 4:
				operation.Steps[3].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", start)
			}
			err := fixture.workflow.ValidateCompletion(context.Background(), operation)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("ValidateCompletion = %v, want %q", err, tt.want)
			}
		})
	}
}
