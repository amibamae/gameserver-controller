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

func TestBuildReadinessAssertionBindsPassingEvidence(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operation, server, step := readinessFixture(schemas.ProtocolVersion())
	encoded, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, time.Minute, readinessFixtureNow())
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", encoded); err != nil {
		t.Fatalf("generated assertion failed schema validation: %v (%s)", err, encoded)
	}
	var assertion readinessAssertion
	if err := json.Unmarshal(encoded, &assertion); err != nil {
		t.Fatal(err)
	}
	if assertion.ServerID != operation.Target.ServerID || assertion.EnvironmentID != operation.Target.EnvironmentID ||
		!equalJSON(assertion.InstanceRef, server.InstanceRef) || assertion.ReadinessCheckSetID != "game.example:release-1:checks-v1" {
		t.Fatalf("assertion lost target or check-set binding: %+v", assertion)
	}
	if _, err := time.Parse(time.RFC3339Nano, assertion.ObservedAt); err != nil {
		t.Fatal(err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, assertion.ExpiresAt)
	wantExpiry := time.Date(2026, time.November, 7, 9, 0, 59, 0, time.UTC)
	if err != nil || !expiresAt.Equal(wantExpiry) {
		t.Fatalf("assertion expiration = %q, want=%q, observed=%q, err=%v", assertion.ExpiresAt, wantExpiry.Format(time.RFC3339), assertion.ObservedAt, err)
	}
	second, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, time.Minute, readinessFixtureNow())
	if err != nil || string(second) != string(encoded) {
		t.Fatalf("same durable evidence did not produce deterministic assertion: %s, %v", second, err)
	}
	changedObservation := store.OperationRecord{OperationID: operation.OperationID, OperationType: operation.OperationType,
		ProtocolVersion: operation.ProtocolVersion, Target: operation.Target, Steps: append([]store.OperationStep(nil), operation.Steps...)}
	changedObservation.Steps[0].Output = json.RawMessage(strings.Replace(string(step.Output), `"observed_at":"2026-11-07T09:00:00Z"`, `"observed_at":"2026-11-07T09:00:01Z"`, 1))
	third, err := buildReadinessAssertionAt(schemas, changedObservation, server, step.StepID, time.Minute, readinessFixtureNow())
	if err != nil {
		t.Fatal(err)
	}
	var thirdAssertion readinessAssertion
	if err := json.Unmarshal(third, &thirdAssertion); err != nil {
		t.Fatal(err)
	}
	if thirdAssertion.AssertionID == assertion.AssertionID {
		t.Fatal("different durable readiness observations reused the same assertion ID")
	}
}

func TestBuildReadinessAssertionRejectsUnsafeEvidence(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*store.OperationRecord, *store.LogicalServer, *store.OperationStep)
	}{
		{name: "failed required check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"result":"pass"`, `"result":"fail"`, 1))
		}},
		{name: "unknown required check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"result":"pass"`, `"result":"unknown"`, 1))
		}},
		{name: "missing required check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			var output readinessCheckOutput
			if err := json.Unmarshal(step.Output, &output); err != nil {
				t.Fatal(err)
			}
			output.CheckResults = output.CheckResults[:1]
			step.Output, _ = json.Marshal(output)
		}},
		{name: "duplicate check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"check_id":"network"`, `"check_id":"process"`, 1))
		}},
		{name: "extra check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"check_id":"network"`, `"check_id":"unexpected"`, 1))
		}},
		{name: "wrong server", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"server_id":"pilot-test"`, `"server_id":"other-server"`, 1))
		}},
		{name: "wrong instance", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"external_id":"instance-1"`, `"external_id":"instance-2"`, 1))
		}},
		{name: "operation targets wrong instance", mutate: func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep) {
			op.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-2"}`)
		}},
		{name: "wrong release", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"game_release_id":"paper-1.21"`, `"game_release_id":"paper-1.22"`, 1))
		}},
		{name: "invalid timestamp", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), "2026-11-07T09:00:00Z", "not-a-time", 1))
		}},
		{name: "stale aggregate observation", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), "2026-11-07T09:00:00Z", "2026-11-07T08:59:00Z", 1))
		}},
		{name: "future aggregate observation", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), "2026-11-07T09:00:00Z", "2026-11-07T09:02:00Z", 1))
		}},
		{name: "stale individual check", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), "2026-11-07T08:59:59Z", "2026-11-07T08:00:00Z", 1))
		}},
		{name: "non-completed step", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Status = protocolstate.StepOutcomeUnknown
		}},
		{name: "wrong capability", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.CapabilityName = "game.health.check"
		}},
		{name: "missing capability version", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.CapabilityVersion = ""
		}},
		{name: "wrong operation", mutate: func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep) {
			op.OperationType = "server.destroy"
		}},
		{name: "wrong declared check version", mutate: func(_ *store.OperationRecord, _ *store.LogicalServer, step *store.OperationStep) {
			step.Output = json.RawMessage(strings.Replace(string(step.Output), `"check_id":"process","check_version":"1"`, `"check_id":"process","check_version":"2"`, 1))
		}},
		{name: "missing persisted step", mutate: func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep) {
			op.Steps = nil
		}},
		{name: "wrong protocol version", mutate: func(op *store.OperationRecord, _ *store.LogicalServer, _ *store.OperationStep) {
			op.ProtocolVersion = "0.20.0-draft"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation, server, step := readinessFixture(schemas.ProtocolVersion())
			test.mutate(&operation, &server, &step)
			if len(operation.Steps) > 0 {
				operation.Steps[0] = step
			}
			if _, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, time.Minute, readinessFixtureNow()); err == nil {
				t.Fatal("unsafe readiness evidence unexpectedly produced an assertion")
			}
		})
	}
}

func TestBuildReadinessAssertionRequiresBoundedTTL(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operation, server, step := readinessFixture(schemas.ProtocolVersion())
	for _, ttl := range []time.Duration{0, -time.Second, maxReadinessAssertionTTL + time.Nanosecond} {
		if _, err := buildReadinessAssertionAt(schemas, operation, server, step.StepID, ttl, readinessFixtureNow()); err == nil {
			t.Fatalf("accepted invalid assertion TTL %s", ttl)
		}
	}
}

func readinessFixtureNow() time.Time {
	return time.Date(2026, time.November, 7, 9, 0, 30, 0, time.UTC)
}

func readinessFixture(protocolVersion string) (store.OperationRecord, store.LogicalServer, store.OperationStep) {
	instanceRef := json.RawMessage(`{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-1"}`)
	input := json.RawMessage(`{"server_id":"pilot-test","environment_id":"staging","instance_ref":{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-1"},"game_release_id":"paper-1.21","readiness_check_set_id":"game.example:release-1:checks-v1","required_checks":[{"check_id":"process","check_version":"1"},{"check_id":"network","check_version":"2"}]}`)
	output := json.RawMessage(`{"server_id":"pilot-test","environment_id":"staging","instance_ref":{"adapter_id":"hosting.example","environment_id":"staging","resource_kind":"instance","external_id":"instance-1"},"game_release_id":"paper-1.21","observed_at":"2026-11-07T09:00:00Z","check_results":[{"check_id":"process","check_version":"1","result":"pass","observed_at":"2026-11-07T08:59:59Z"},{"check_id":"network","check_version":"2","result":"pass","observed_at":"2026-11-07T08:59:59Z"}]}`)
	operation := store.OperationRecord{
		OperationID: "start-operation", OperationType: "server.start",
		Target: store.OperationTarget{ServerID: "pilot-test", EnvironmentID: "staging"},
	}
	server := store.LogicalServer{
		ServerID: "pilot-test", EnvironmentID: "staging", InstanceRef: instanceRef,
		LifecycleState: protocolstate.ServerStopped,
	}
	step := store.OperationStep{
		StepID: "readiness-step", StepType: "game.readiness.check", Status: protocolstate.StepSucceeded,
		AdapterID: "game.example", CapabilityName: "game.readiness.check", CapabilityVersion: "1",
		Input: input, Output: output,
	}
	operation.ProtocolVersion = protocolVersion
	operation.Steps = []store.OperationStep{step}
	return operation, server, step
}
