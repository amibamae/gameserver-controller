package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the managed VM capacity match and claim
// that guard provider destruction, the decoders for recorded step outputs,
// and the observation and optional-capability helpers they rely on.

const (
	destroyR3ExactInstance  = "managed VM capacity requires an exact provider instance reference"
	destroyR3MultipleExact  = "multiple managed VM reservations match one exact server instance"
	destroyR3MultipleActive = "multiple active managed VM reservations match one logical server"
	destroyR3OtherAdapter   = "managed VM reservation belongs to a different hosting adapter"
	destroyR3OtherInstance  = "managed VM reservation does not identify the exact server instance"
)

// destroyR3Capacity is an in-memory capacity store that records claims.
type destroyR3Capacity struct {
	reservations []store.CapacityReservation
	listErr      error
	markErr      error
	marked       []string
}

func (s *destroyR3Capacity) ListCapacityReservations() ([]store.CapacityReservation, error) {
	return s.reservations, s.listErr
}

func (s *destroyR3Capacity) MarkCapacityDestroySent(operationID string) (store.CapacityReservation, error) {
	s.marked = append(s.marked, operationID)
	return store.CapacityReservation{}, s.markErr
}

// destroyR3CapacityServers is a lifecycle store with the destroy capacity policy.
type destroyR3CapacityServers struct {
	store.LifecycleOperationStore
	*destroyR3Capacity
}

func destroyR3Server() store.LogicalServer {
	return store.LogicalServer{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance)}
}

func destroyR3Reservation(operationID, adapterID, instanceID string, state store.CapacityState) store.CapacityReservation {
	return store.CapacityReservation{OperationID: operationID, ServerID: startTestServerID, EnvironmentID: startTestEnvID,
		ProviderAdapterID: adapterID, ProviderInstanceID: instanceID, State: state}
}

func TestDestroyR3MatchingCapacityReservation(t *testing.T) {
	const vm = "vm-start-test"
	otherServer := destroyR3Reservation("other-server", "hosting.fake", vm, store.CapacityActive)
	otherServer.ServerID = "other"
	otherEnvironment := destroyR3Reservation("other-environment", "hosting.fake", vm, store.CapacityActive)
	otherEnvironment.EnvironmentID = "other"
	tests := []struct {
		name         string
		reservations []store.CapacityReservation
		listErr      error
		instance     string
		want         string
		match        string
	}{
		{name: "no reservations"},
		{name: "list fails", listErr: errors.New("offline"), want: "list managed VM capacity reservations: offline"},
		{name: "list failure is reported before the instance", listErr: errors.New("offline"), instance: `[]`, want: "list managed VM capacity reservations: offline"},
		{name: "array instance", instance: `[]`, want: destroyR3ExactInstance},
		{name: "undecodable instance", instance: `{"adapter_id":`, want: destroyR3ExactInstance},
		{name: "instance without adapter", instance: `{"adapter_id":"","resource_kind":"instance","external_id":"vm"}`, want: destroyR3ExactInstance},
		{name: "instance of another kind", instance: `{"adapter_id":"hosting.fake","resource_kind":"volume","external_id":"vm"}`, want: destroyR3ExactInstance},
		{name: "instance without external ID", instance: `{"adapter_id":"hosting.fake","resource_kind":"instance","external_id":""}`, want: destroyR3ExactInstance},
		{name: "other servers and environments are ignored", reservations: []store.CapacityReservation{otherServer, otherEnvironment}},
		{name: "active exact reservation", reservations: []store.CapacityReservation{destroyR3Reservation("active", "hosting.fake", vm, store.CapacityActive)}, match: "active"},
		{name: "active reservation without an adapter", reservations: []store.CapacityReservation{destroyR3Reservation("active", "", vm, store.CapacityActive)}, match: "active"},
		{name: "destroy outcome unknown", reservations: []store.CapacityReservation{destroyR3Reservation("unknown", "hosting.fake", vm, store.CapacityDestroyOutcomeUnknown)}, match: "unknown"},
		{name: "absent exact reservation", reservations: []store.CapacityReservation{destroyR3Reservation("absent", "hosting.fake", vm, store.CapacityAbsent)}, match: "absent"},
		{name: "destroyed exact reservation without an adapter", reservations: []store.CapacityReservation{destroyR3Reservation("destroyed", "", vm, store.CapacityDestroyed)}, match: "destroyed"},
		{name: "absent reservation for another instance is ignored", reservations: []store.CapacityReservation{destroyR3Reservation("absent", "hosting.fake", "vm-other", store.CapacityAbsent)}},
		{name: "destroyed reservation for another adapter is ignored", reservations: []store.CapacityReservation{destroyR3Reservation("destroyed", "hosting.other", vm, store.CapacityDestroyed)}},
		{name: "two retired exact reservations", reservations: []store.CapacityReservation{
			destroyR3Reservation("absent", "hosting.fake", vm, store.CapacityAbsent), destroyR3Reservation("destroyed", "", vm, store.CapacityDestroyed),
		}, want: destroyR3MultipleExact},
		{name: "active reservation for another adapter", reservations: []store.CapacityReservation{destroyR3Reservation("active", "hosting.other", vm, store.CapacityActive)}, want: destroyR3OtherAdapter},
		{name: "reserved reservation without an instance", reservations: []store.CapacityReservation{destroyR3Reservation("reserved", "hosting.fake", "", store.CapacityReserved)}, want: destroyR3OtherInstance},
		{name: "active reservation for another instance", reservations: []store.CapacityReservation{destroyR3Reservation("active", "hosting.fake", "vm-other", store.CapacityActive)}, want: destroyR3OtherInstance},
		{name: "adapter is reported before instance", reservations: []store.CapacityReservation{destroyR3Reservation("active", "hosting.other", "vm-other", store.CapacityActive)}, want: destroyR3OtherAdapter},
		{name: "two active reservations", reservations: []store.CapacityReservation{
			destroyR3Reservation("first", "hosting.fake", vm, store.CapacityActive), destroyR3Reservation("second", "", vm, store.CapacityCreateOutcomeUnknown),
		}, want: destroyR3MultipleActive},
		{name: "retired then active", reservations: []store.CapacityReservation{
			destroyR3Reservation("absent", "hosting.fake", vm, store.CapacityAbsent), destroyR3Reservation("active", "hosting.fake", vm, store.CapacityActive),
		}, want: destroyR3MultipleActive},
		{name: "active then retired", reservations: []store.CapacityReservation{
			destroyR3Reservation("active", "hosting.fake", vm, store.CapacityActive), destroyR3Reservation("absent", "hosting.fake", vm, store.CapacityAbsent),
		}, want: destroyR3MultipleExact},
		{name: "mismatch after a match is still reported", reservations: []store.CapacityReservation{
			destroyR3Reservation("active", "hosting.fake", vm, store.CapacityActive), destroyR3Reservation("other", "hosting.other", vm, store.CapacityActive),
		}, want: destroyR3OtherAdapter},
		{name: "ignored reservations around a match", reservations: []store.CapacityReservation{
			otherServer, destroyR3Reservation("absent", "hosting.fake", "vm-other", store.CapacityAbsent),
			destroyR3Reservation("active", "hosting.fake", vm, store.CapacityActive), otherEnvironment,
		}, match: "active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := destroyR3Server()
			if test.instance != "" {
				server.InstanceRef = json.RawMessage(test.instance)
			}
			capacity := &destroyR3Capacity{reservations: test.reservations, listErr: test.listErr}
			reservation, found, err := matchingDestroyCapacityReservation(capacity, server)
			destroyR3WantError(t, "matchingDestroyCapacityReservation", err, test.want)
			if found != (test.match != "") || reservation.OperationID != test.match {
				t.Fatalf("match = %q found=%v, want %q", reservation.OperationID, found, test.match)
			}
		})
	}
}

func TestDestroyR3MarkCapacityDestroySent(t *testing.T) {
	const vm = "vm-start-test"
	tests := []struct {
		name         string
		reservations []store.CapacityReservation
		markErr      error
		want         string
		marked       []string
	}{
		{name: "no reservation"},
		{name: "active reservation is claimed", reservations: []store.CapacityReservation{destroyR3Reservation("active-op", "hosting.fake", vm, store.CapacityActive)}, marked: []string{"active-op"}},
		{name: "claim fails", reservations: []store.CapacityReservation{destroyR3Reservation("active-op", "hosting.fake", vm, store.CapacityActive)},
			markErr: errors.New("conflict"), want: "claim managed VM capacity before provider destroy: conflict", marked: []string{"active-op"}},
		{name: "prior claim is kept", reservations: []store.CapacityReservation{destroyR3Reservation("prior", "hosting.fake", vm, store.CapacityDestroyOutcomeUnknown)}},
		{name: "reserved reservation is refused", reservations: []store.CapacityReservation{destroyR3Reservation("reserved", "hosting.fake", vm, store.CapacityReserved)},
			want: `managed VM capacity reservation is "reserved"; refusing provider destroy`},
		{name: "absent reservation is refused", reservations: []store.CapacityReservation{destroyR3Reservation("absent", "hosting.fake", vm, store.CapacityAbsent)},
			want: `managed VM capacity reservation is "absent"; refusing provider destroy`},
		{name: "match error is returned", reservations: []store.CapacityReservation{destroyR3Reservation("active", "hosting.other", vm, store.CapacityActive)}, want: destroyR3OtherAdapter},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			capacity := &destroyR3Capacity{reservations: test.reservations, markErr: test.markErr}
			workflow := &ServerDestroyWorkflow{servers: destroyR3CapacityServers{destroyR3Capacity: capacity}}
			destroyR3WantError(t, "markCapacityDestroySent", workflow.markCapacityDestroySent(destroyR3Server()), test.want)
			if len(capacity.marked) != len(test.marked) || (len(test.marked) > 0 && capacity.marked[0] != test.marked[0]) {
				t.Fatalf("claimed %v, want %v", capacity.marked, test.marked)
			}
		})
	}
	// A store without the capacity policy skips the claim.
	workflow := &ServerDestroyWorkflow{servers: struct{ store.LifecycleOperationStore }{}}
	destroyR3WantError(t, "markCapacityDestroySent without capacity", workflow.markCapacityDestroySent(destroyR3Server()), "")
}

func TestDestroyR3ExternalInstanceIdentity(t *testing.T) {
	adapterID, instanceID, err := externalInstanceIdentity(json.RawMessage(startTestInstance))
	if err != nil || adapterID != "hosting.fake" || instanceID != "vm-start-test" {
		t.Fatalf("externalInstanceIdentity = %q, %q, %v", adapterID, instanceID, err)
	}
	for _, raw := range []string{"", `null`, `[]`, `{"adapter_id":7}`, `{"resource_kind":"instance","external_id":"vm"}`,
		`{"adapter_id":"a","resource_kind":"unit","external_id":"vm"}`, `{"adapter_id":"a","resource_kind":"instance"}`} {
		adapterID, instanceID, err := externalInstanceIdentity(json.RawMessage(raw))
		destroyR3WantError(t, "externalInstanceIdentity("+raw+")", err, destroyR3ExactInstance)
		if adapterID != "" || instanceID != "" {
			t.Fatalf("rejected identity %s returned %q, %q", raw, adapterID, instanceID)
		}
	}
}

// destroyR3StepOutputCase is one recorded step output and the decoder errors
// expected for it.
type destroyR3StepOutputCase struct {
	name   string
	change func(*store.OperationStep)
	want   string
}

func destroyR3StepOutputCases(valid json.RawMessage, missing, stale string) []destroyR3StepOutputCase {
	_, parseErr := time.Parse(time.RFC3339Nano, "yesterday")
	return []destroyR3StepOutputCase{
		{name: "valid"},
		{name: "unknown step", change: func(s *store.OperationStep) { s.StepID = "renamed" }, want: missing},
		{name: "not succeeded", change: func(s *store.OperationStep) { s.Status = protocolstate.StepRunning }, want: missing},
		{name: "undecodable", change: func(s *store.OperationStep) { s.Output = json.RawMessage(`{`) }, want: missing},
		{name: "no output", change: func(s *store.OperationStep) { s.Output = nil }, want: missing},
		{name: "unparsable time", change: func(s *store.OperationStep) { s.Output = destroyR3WithObservedAt(valid, "yesterday") }, want: parseErr.Error()},
		{name: "stale", change: func(s *store.OperationStep) {
			s.Output = destroyR3WithObservedAt(valid, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))
		}, want: stale},
		{name: "from the future", change: func(s *store.OperationStep) {
			s.Output = destroyR3WithObservedAt(valid, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))
		}, want: stale},
		{name: "never started", change: func(s *store.OperationStep) { s.StartedAt = nil }, want: stale},
	}
}

func destroyR3WithObservedAt(output json.RawMessage, observedAt string) json.RawMessage {
	var fields map[string]any
	if err := json.Unmarshal(output, &fields); err != nil {
		panic(err)
	}
	fields["observed_at"] = observedAt
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return encoded
}

func TestDestroyR3StepOutputDecoders(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	decoders := []struct {
		name           string
		index          int
		missing, stale string
		decode         func(store.OperationRecord) (string, error)
	}{
		{name: "instance", index: 0, missing: "provider instance inspection output is missing", stale: "provider observation is stale or outside the tolerated clock window",
			decode: func(o store.OperationRecord) (string, error) {
				output, err := destroyInstanceStepOutput(o, destroyInspectInstanceStep, protocolstate.StepSucceeded)
				return output.State + string(output.InstanceRef) + output.ObservedAt, err
			}},
		{name: "runtime", index: 1, missing: "runtime inspection output is missing", stale: "runtime observation is stale or outside the tolerated clock window",
			decode: func(o store.OperationRecord) (string, error) {
				output, err := destroyRuntimeStepOutput(o)
				return output.State + string(output.UnitRef) + output.ObservedAt, err
			}},
		{name: "provider", index: 2, missing: "provider destruction output is missing", stale: "provider destroy result is stale or outside the tolerated clock window",
			decode: func(o store.OperationRecord) (string, error) {
				output, err := destroyProviderOutput(o)
				return output.State + output.Outcome + string(output.InstanceRef) + output.ObservedAt + string(output.ProviderOperationRef), err
			}},
	}
	for _, decoder := range decoders {
		valid := destroyR3CompletedOperation(fixture).Steps[decoder.index].Output
		for _, test := range destroyR3StepOutputCases(valid, decoder.missing, decoder.stale) {
			t.Run(decoder.name+"/"+test.name, func(t *testing.T) {
				operation := destroyR3CompletedOperation(fixture)
				operation.Steps[decoder.index].Output = valid
				if test.change != nil {
					test.change(&operation.Steps[decoder.index])
				}
				got, err := decoder.decode(operation)
				destroyR3WantError(t, decoder.name+" decoder", err, test.want)
				if test.want != "" && got != "" {
					t.Fatalf("rejected %s output decoded to %q, want zero", decoder.name, got)
				}
				if test.want == "" && got == "" {
					t.Fatalf("valid %s output decoded to zero", decoder.name)
				}
			})
		}
	}
	// The instance decoder honours the required status it is given.
	operation := destroyR3CompletedOperation(fixture)
	if _, err := destroyInstanceStepOutput(operation, destroyVerifyInstanceStep, protocolstate.StepPending); err == nil || err.Error() != "provider instance inspection output is missing" {
		t.Fatalf("instance decoder with another required status = %v", err)
	}
	operation.Steps[3].Status = protocolstate.StepPending
	if _, err := destroyInstanceStepOutput(operation, destroyVerifyInstanceStep, protocolstate.StepPending); err != nil {
		t.Fatalf("instance decoder for a pending step = %v", err)
	}
}

func TestDestroyR3ObservationTimes(t *testing.T) {
	started := time.Now().UTC()
	step := store.OperationStep{StartedAt: &started}
	at := func(offset time.Duration) string { return started.Add(offset).Format(time.RFC3339Nano) }
	fresh := []struct {
		value string
		step  store.OperationStep
		want  bool
	}{
		{value: at(0), step: step, want: true},
		{value: at(-29 * time.Second), step: step, want: true},
		{value: at(-31 * time.Second), step: step},
		{value: at(4 * time.Minute), step: step, want: true},
		{value: at(6 * time.Minute), step: step},
		{value: "yesterday", step: step},
		{value: at(0), step: store.OperationStep{}},
	}
	for _, test := range fresh {
		if got := observationIsFreshForStep(test.value, test.step); got != test.want {
			t.Fatalf("observationIsFreshForStep(%s) = %v, want %v", test.value, got, test.want)
		}
	}
	_, parseErr := time.Parse(time.RFC3339Nano, "yesterday")
	ordered := []struct{ before, after, want string }{
		{before: at(0), after: at(time.Second)},
		{before: at(0), after: at(0)},
		{before: at(time.Second), after: at(0), want: "observation timestamp moved backwards"},
		{before: "yesterday", after: at(0), want: parseErr.Error()},
		{before: at(0), after: "yesterday", want: parseErr.Error()},
	}
	for _, test := range ordered {
		destroyR3WantError(t, "observationAfter("+test.before+", "+test.after+")", observationAfter(test.before, test.after), test.want)
	}
}

func TestDestroyR3OptionalExactCapability(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	inputURI, outputURI := destroyR3URN(fixture, "hosting:instance-wait-input"), destroyR3URN(fixture, "hosting:instance-wait-output")
	tests := []struct {
		name       string
		descriptor func(int, adapter.Descriptor, error) (adapter.Descriptor, error)
		category   adapter.Category
		mutating   bool
		want       string
	}{
		{name: "exact capability", category: adapter.CategoryHosting},
		{name: "descriptor fails", category: adapter.CategoryHosting, descriptor: func(int, adapter.Descriptor, error) (adapter.Descriptor, error) {
			return adapter.Descriptor{}, errors.New("offline")
		}, want: `resolve adapter "hosting.fake" for optional hosting.instance.wait capability: offline`},
		{name: "another adapter identity", category: adapter.CategoryHosting, descriptor: func(_ int, d adapter.Descriptor, err error) (adapter.Descriptor, error) {
			d.AdapterID = "hosting.other"
			return d, err
		}, want: `adapter "hosting.fake" identity or category does not match optional hosting.instance.wait capability`},
		{name: "another category", category: adapter.CategoryExecution, want: `adapter "hosting.fake" identity or category does not match optional hosting.instance.wait capability`},
		{name: "incompatible mutating flag", category: adapter.CategoryHosting, mutating: true, want: `adapter "hosting.fake" declares an incompatible hosting.instance.wait capability`},
		{name: "not advertised", category: adapter.CategoryHosting, descriptor: destroyWithoutCapability("hosting.instance.wait"),
			want: `adapter "hosting.fake" does not advertise optional capability hosting.instance.wait`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := &destroyRuntimeOverride{adapterRuntime: fixture.base.registry, descriptor: test.descriptor}
			capability, err := optionalExactCapability(runtime, "hosting.fake", test.category, "hosting.instance.wait", inputURI, outputURI, test.mutating)
			destroyR3WantError(t, "optionalExactCapability", err, test.want)
			if (test.want == "") != (capability.Name == "hosting.instance.wait") {
				t.Fatalf("capability = %#v", capability)
			}
		})
	}
	runtime := &destroyRuntimeOverride{adapterRuntime: fixture.base.registry}
	for _, uris := range [][2]string{{"urn:other", outputURI}, {inputURI, "urn:other"}} {
		_, err := optionalExactCapability(runtime, "hosting.fake", adapter.CategoryHosting, "hosting.instance.wait", uris[0], uris[1], false)
		destroyR3WantError(t, "optionalExactCapability with another schema", err, `adapter "hosting.fake" declares an incompatible hosting.instance.wait capability`)
	}
	if _, err := context.Background(), error(nil); err != nil {
		t.Fatal(err)
	}
}
