package workflow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// TestBuildReadinessAssertionIsExact pins the assertion bytes for the
// fixture, so the evidence binding cannot change silently.
func TestBuildReadinessAssertionIsExact(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operation, server, step := readinessFixture(schemas.ProtocolVersion())
	got, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, time.Minute, readinessFixtureNow())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"assertion_id":"readiness-8cb17dc54201cb2ea75ab8055bbb1de4a358f03ccf1f7838626dbc4c6f48bdee","server_id":"pilot-test","environment_id":"staging","instance_ref":{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-1"},"readiness_check_set_id":"game.example:release-1:checks-v1","observed_at":"2026-11-07T09:00:00Z","expires_at":"2026-11-07T09:00:59Z"}`
	if string(got) != want {
		t.Fatalf("assertion = %s", got)
	}
}

// TestBuildReadinessAssertionReportsWhyEvidenceIsRejected pins the error each
// rule reports, so the order of the checks cannot change silently.
func TestBuildReadinessAssertionReportsWhyEvidenceIsRejected(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	replaceOutput := func(old, new string) func(*store.OperationRecord, *store.LogicalServer, *store.OperationStep, *time.Time) {
		return func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep, _ *time.Time) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), old, new, 1))
		}
	}
	replaceInput := func(old, new string) func(*store.OperationRecord, *store.LogicalServer, *store.OperationStep, *time.Time) {
		return func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep, _ *time.Time) {
			step.Input = json.RawMessage(strings.Replace(string(step.Input), old, new, 1))
		}
	}
	tests := []struct {
		name, want string
		mutate     func(*store.OperationRecord, *store.LogicalServer, *store.OperationStep, *time.Time)
	}{
		{"zero clock", "readiness assertion clock is required", func(_ *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep, now *time.Time) {
			*now = time.Time{}
		}},
		{"wrong operation type", "readiness assertions are only valid for start, provision, and restore operations", func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			op.OperationType = "server.stop"
		}},
		{"protocol version not loaded", "readiness operation protocol version is not loaded by the schema registry", func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			op.ProtocolVersion = ""
		}},
		{"destroyed server", "readiness target does not match an existing managed instance", func(_ *store.OperationRecord, server *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			server.LifecycleState = protocolstate.ServerDestroyed
		}},
		{"server without instance", "readiness target does not match an existing managed instance", func(_ *store.OperationRecord, server *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			server.InstanceRef = nil
		}},
		{"operation names another instance", "readiness target instance does not match the managed instance", func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			op.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-2"}`)
		}},
		{"step not persisted", "readiness evidence step is not part of the persisted operation", func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep, _ *time.Time) {
			op.Steps = nil
		}},
		{"step not succeeded", "readiness evidence must come from a completed game.readiness.check step", func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep, _ *time.Time) {
			step.Status = protocolstate.StepFailed
		}},
		{"request breaks schema", "readiness request does not match protocol schema", replaceInput(`"game_release_id":"paper-1.21",`, ``)},
		{"observation breaks schema", "readiness observation does not match protocol schema", replaceOutput(`"result":"pass"`, `"result":"maybe"`)},
		{"wrong release", "readiness evidence is not bound to the requested server, release, and managed instance", replaceOutput(`"game_release_id":"paper-1.21"`, `"game_release_id":"paper-1.22"`)},
		{"result count differs", "readiness evidence must contain exactly one result for every required check", replaceInput(`,{"check_id":"network","check_version":"2"}`, ``)},
		{"required check repeated", `readiness request repeats required check "process"`, replaceInput(`{"check_id":"network","check_version":"2"}`, `{"check_id":"process","check_version":"2"}`)},
		{"stale observation", "readiness observation is stale or from the future", replaceOutput(`"observed_at":"2026-11-07T09:00:00Z"`, `"observed_at":"2026-11-07T08:58:00Z"`)},
		{"unrequested check", `readiness observation contains unrequested check "unexpected"`, replaceOutput(`"check_id":"network"`, `"check_id":"unexpected"`)},
		{"check failed", `required readiness check "process" did not pass at its declared version`, replaceOutput(`"result":"pass"`, `"result":"fail"`)},
		{"check observed after aggregate", `readiness check "process" has an invalid, stale, or future observation time`, replaceOutput(`"observed_at":"2026-11-07T08:59:59Z"`, `"observed_at":"2026-11-07T09:00:10Z"`)},
		{"check reported twice", `readiness observation contains unrequested check "process"`, replaceOutput(`"check_id":"network","check_version":"2"`, `"check_id":"process","check_version":"1"`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation, server, step := readinessFixture(schemas.ProtocolVersion())
			now := readinessFixtureNow()
			test.mutate(&operation, &server, &step, &now)
			if len(operation.Steps) > 0 {
				operation.Steps[0] = step
			}
			_, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, time.Minute, now)
			if err == nil || !strings.HasPrefix(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
