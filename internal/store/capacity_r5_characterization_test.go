package store

// Round-5 characterization tests for capacity.go. They pin, on the code
// before the round-5 refactor, every predicate and branch that refactor
// restructures: the truth tables of the identity, change and consistency
// predicates, the reserve scan and limits, the observation validation order,
// and the destroy and start evidence checks, with exact errors and bytes.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// capacityR5Base is a fixed reservation time far enough in the past for any
// observation derived from it to be plausible.
var capacityR5Base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// capacityR5Case is one row of a predicate truth table: mutate is applied to
// a copy of the table's valid value and want is the predicate's result.
type capacityR5Case[T any] struct {
	name   string
	mutate func(*T)
	want   bool
}

// capacityR5Table runs every case of a predicate truth table against a fresh
// copy of base.
func capacityR5Table[T any](t *testing.T, base func() T, predicate func(T) bool, cases []capacityR5Case[T]) {
	t.Helper()
	for _, tc := range cases {
		value := base()
		tc.mutate(&value)
		if got := predicate(value); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// capacityR5Pair adapts a predicate over (current, next) to a truth table over
// next, with current fixed to base().
func capacityR5Pair(base func() CapacityReservation, predicate func(current, next CapacityReservation) bool) func(CapacityReservation) bool {
	return func(next CapacityReservation) bool { return predicate(base(), next) }
}

func capacityR5Unchanged(*CapacityReservation) {}

// capacityR5Adopted returns a valid adopted reservation: no provision or
// ready time, the adoption observed when the reservation was created.
func capacityR5Adopted() CapacityReservation {
	r := persistedActiveReservation()
	r.ProvisionedAt, r.ReadyAt, r.LastObservation.ProvisionedAt = time.Time{}, time.Time{}, time.Time{}
	r.Adoption = &CapacityAdoption{InstanceID: "inst-1", IP: "149.28.1.2", Region: "ewr", Plan: "vc2-1c-1gb", OSID: 2136, OS: "Debian 12 x64 (bookworm)", ObservedAt: r.CreatedAt}
	return r
}

func TestCapacityR5SameCapacityRequest(t *testing.T) {
	base := func() CapacityReservation {
		r := capacityRequest("op", "srv", "staging", "g")
		r.ProviderAdapterID = "hosting.vultr"
		return r
	}
	capacityR5Table(t, base, capacityR5Pair(base, sameCapacityRequest), []capacityR5Case[CapacityReservation]{
		{"identical", capacityR5Unchanged, true},
		{"operation", func(r *CapacityReservation) { r.OperationID = "op-2" }, false},
		{"server", func(r *CapacityReservation) { r.ServerID = "srv-2" }, false},
		{"environment", func(r *CapacityReservation) { r.EnvironmentID = "prod" }, false},
		{"group", func(r *CapacityReservation) { r.CapacityGroup = "g-2" }, false},
		{"provider", func(r *CapacityReservation) { r.Provider = "other" }, false},
		{"adapter", func(r *CapacityReservation) { r.ProviderAdapterID = "" }, false},
		{"resource key", func(r *CapacityReservation) { r.ResourceKey = "key-2" }, false},
		{"store fields ignored", func(r *CapacityReservation) {
			r.State, r.ProviderInstanceID, r.CreatedAt = CapacityActive, "vm", capacityR5Base
		}, true},
	})
}

func TestCapacityR5SameCapacityObservation(t *testing.T) {
	base := func() CapacityProviderObservation {
		return CapacityProviderObservation{Provider: "vultr", ResourceKey: "k", ProviderInstanceID: "vm", State: "present", ObservedAt: capacityR5Base.Add(time.Minute), ProvisionedAt: capacityR5Base}
	}
	same := func(o CapacityProviderObservation) bool { return sameCapacityObservation(base(), o) }
	capacityR5Table(t, base, same, []capacityR5Case[CapacityProviderObservation]{
		{"identical", func(*CapacityProviderObservation) {}, true},
		{"same instants in another zone", func(o *CapacityProviderObservation) {
			o.ObservedAt, o.ProvisionedAt = o.ObservedAt.In(time.FixedZone("x", 3600)), o.ProvisionedAt.In(time.FixedZone("x", 3600))
		}, true},
		{"provider", func(o *CapacityProviderObservation) { o.Provider = "other" }, false},
		{"resource key", func(o *CapacityProviderObservation) { o.ResourceKey = "k2" }, false},
		{"instance", func(o *CapacityProviderObservation) { o.ProviderInstanceID = "" }, false},
		{"state", func(o *CapacityProviderObservation) { o.State = "absent" }, false},
		{"observed", func(o *CapacityProviderObservation) { o.ObservedAt = o.ObservedAt.Add(time.Nanosecond) }, false},
		{"provisioned", func(o *CapacityProviderObservation) { o.ProvisionedAt = time.Time{} }, false},
	})
}

func TestCapacityR5ImmutableFieldsChanged(t *testing.T) {
	base := persistedActiveReservation
	unprovisioned := func() CapacityReservation {
		r := base()
		r.ProvisionedAt, r.ReadyAt = time.Time{}, time.Time{}
		return r
	}
	zone := time.FixedZone("x", 3600)
	capacityR5Table(t, base, capacityR5Pair(base, capacityImmutableFieldsChanged), []capacityR5Case[CapacityReservation]{
		{"identical", capacityR5Unchanged, false},
		{"tracked fields only", func(r *CapacityReservation) {
			r.State, r.ProviderInstanceID, r.UpdatedAt, r.LastObservation = CapacityDestroyed, "vm-2", r.UpdatedAt.Add(time.Hour), nil
		}, false},
		{"operation", func(r *CapacityReservation) { r.OperationID = "op-2" }, true},
		{"adapter", func(r *CapacityReservation) { r.ProviderAdapterID = "" }, true},
		{"created", func(r *CapacityReservation) { r.CreatedAt = r.CreatedAt.Add(time.Second) }, true},
		{"created same instant other zone", func(r *CapacityReservation) { r.CreatedAt = r.CreatedAt.In(zone) }, true},
		{"provisioned", func(r *CapacityReservation) { r.ProvisionedAt = r.ProvisionedAt.Add(time.Second) }, true},
		{"provisioned cleared", func(r *CapacityReservation) { r.ProvisionedAt = time.Time{} }, true},
		{"provisioned other zone", func(r *CapacityReservation) { r.ProvisionedAt = r.ProvisionedAt.In(zone) }, false},
		{"ready", func(r *CapacityReservation) { r.ReadyAt = r.ReadyAt.Add(time.Second) }, true},
		{"ready cleared", func(r *CapacityReservation) { r.ReadyAt = time.Time{} }, true},
		{"ready other zone", func(r *CapacityReservation) { r.ReadyAt = r.ReadyAt.In(zone) }, false},
	})
	capacityR5Table(t, base, capacityR5Pair(unprovisioned, capacityImmutableFieldsChanged), []capacityR5Case[CapacityReservation]{
		{"first provision and ready times", capacityR5Unchanged, false},
	})
}

func TestCapacityR5TrackedFieldsChanged(t *testing.T) {
	base := persistedActiveReservation
	shared := base()
	sharedBase := func() CapacityReservation { return shared }
	capacityR5Table(t, sharedBase, capacityR5Pair(sharedBase, capacityTrackedFieldsChanged), []capacityR5Case[CapacityReservation]{
		{"identical", capacityR5Unchanged, false},
		{"untracked fields", func(r *CapacityReservation) {
			r.UpdatedAt, r.CapacityGroup, r.Adoption = r.UpdatedAt.Add(time.Hour), "other", &CapacityAdoption{}
		}, false},
		{"times in another zone", func(r *CapacityReservation) {
			r.ProvisionedAt, r.ReadyAt = r.ProvisionedAt.In(time.FixedZone("x", 1)), r.ReadyAt.In(time.FixedZone("x", 1))
		}, false},
		{"state", func(r *CapacityReservation) { r.State = CapacityDestroyed }, true},
		{"instance", func(r *CapacityReservation) { r.ProviderInstanceID = "vm-2" }, true},
		{"observation pointer", func(r *CapacityReservation) { copied := *r.LastObservation; r.LastObservation = &copied }, true},
		{"provisioned", func(r *CapacityReservation) { r.ProvisionedAt = time.Time{} }, true},
		{"ready", func(r *CapacityReservation) { r.ReadyAt = r.ReadyAt.Add(time.Second) }, true},
	})
}

func TestCapacityR5ControllerFieldsUnset(t *testing.T) {
	base := func() CapacityReservation { return capacityRequest("op", "srv", "staging", "g") }
	capacityR5Table(t, base, capacityControllerFieldsUnset, []capacityR5Case[CapacityReservation]{
		{"request", capacityR5Unchanged, true},
		{"instance", func(r *CapacityReservation) { r.ProviderInstanceID = "vm" }, false},
		{"state", func(r *CapacityReservation) { r.State = CapacityReserved }, false},
		{"created", func(r *CapacityReservation) { r.CreatedAt = capacityR5Base }, false},
		{"updated", func(r *CapacityReservation) { r.UpdatedAt = capacityR5Base }, false},
		{"provisioned", func(r *CapacityReservation) { r.ProvisionedAt = capacityR5Base }, false},
		{"ready", func(r *CapacityReservation) { r.ReadyAt = capacityR5Base }, false},
		{"observation", func(r *CapacityReservation) { r.LastObservation = &CapacityProviderObservation{} }, false},
		{"adoption", func(r *CapacityReservation) { r.Adoption = &CapacityAdoption{} }, false},
	})
}

func TestCapacityR5IdentifiersValid(t *testing.T) {
	base := func() CapacityReservation { return capacityRequest("op", "srv", "staging", "g") }
	cases := []capacityR5Case[CapacityReservation]{{"request", capacityR5Unchanged, true}}
	fields := []struct {
		name string
		max  int
		set  func(*CapacityReservation, string)
	}{
		{"operation", 200, func(r *CapacityReservation, v string) { r.OperationID = v }},
		{"server", 200, func(r *CapacityReservation, v string) { r.ServerID = v }},
		{"environment", 200, func(r *CapacityReservation, v string) { r.EnvironmentID = v }},
		{"group", 100, func(r *CapacityReservation, v string) { r.CapacityGroup = v }},
		{"provider", 100, func(r *CapacityReservation, v string) { r.Provider = v }},
		{"resource key", 240, func(r *CapacityReservation, v string) { r.ResourceKey = v }},
	}
	for _, field := range fields {
		set, max := field.set, field.max
		cases = append(cases,
			capacityR5Case[CapacityReservation]{field.name + " empty", func(r *CapacityReservation) { set(r, "") }, false},
			capacityR5Case[CapacityReservation]{field.name + " at bound", func(r *CapacityReservation) { set(r, strings.Repeat("a", max)) }, true},
			capacityR5Case[CapacityReservation]{field.name + " over bound", func(r *CapacityReservation) { set(r, strings.Repeat("a", max+1)) }, false},
			capacityR5Case[CapacityReservation]{field.name + " control", func(r *CapacityReservation) { set(r, "a\x7f") }, false},
		)
	}
	capacityR5Table(t, base, capacityIdentifiersValid, cases)
	capacityR5Table(t, base, capacityIdentityValid, []capacityR5Case[CapacityReservation]{
		{"no times", capacityR5Unchanged, false},
		{"created only", func(r *CapacityReservation) { r.CreatedAt = capacityR5Base }, false},
		{"created and updated", func(r *CapacityReservation) { r.CreatedAt, r.UpdatedAt = capacityR5Base, capacityR5Base }, true},
		{"bad adapter", func(r *CapacityReservation) {
			r.CreatedAt, r.UpdatedAt, r.ProviderAdapterID = capacityR5Base, capacityR5Base, strings.Repeat("a", 161)
		}, false},
		{"adapter at bound", func(r *CapacityReservation) {
			r.CreatedAt, r.UpdatedAt, r.ProviderAdapterID = capacityR5Base, capacityR5Base, strings.Repeat("a", 160)
		}, true},
		{"bad identifier", func(r *CapacityReservation) {
			r.CreatedAt, r.UpdatedAt, r.ServerID = capacityR5Base, capacityR5Base, ""
		}, false},
	})
}

func TestCapacityR5AdoptionConsistent(t *testing.T) {
	capacityR5Table(t, capacityR5Adopted, capacityAdoptionConsistent, []capacityR5Case[CapacityReservation]{
		{"valid adoption", capacityR5Unchanged, true},
		{"no adoption", func(r *CapacityReservation) { r.Adoption, r.LastObservation = nil, nil }, true},
		{"invalid adoption", func(r *CapacityReservation) { r.Adoption.IP = "10.0.0.1" }, false},
		{"other instance", func(r *CapacityReservation) { r.ProviderInstanceID = "inst-2" }, false},
		{"created at another time", func(r *CapacityReservation) { r.Adoption.ObservedAt = r.CreatedAt.Add(time.Second) }, false},
		{"created same instant other zone", func(r *CapacityReservation) { r.Adoption.ObservedAt = r.CreatedAt.In(time.FixedZone("x", 1)) }, true},
		{"no observation", func(r *CapacityReservation) { r.LastObservation = nil }, false},
		{"provisioned", func(r *CapacityReservation) { r.ProvisionedAt = r.CreatedAt }, false},
		{"ready", func(r *CapacityReservation) { r.ReadyAt = r.CreatedAt }, false},
	})
}

func TestCapacityR5TimesOrdered(t *testing.T) {
	minutes := func(n int) time.Time { return capacityR5Base.Add(time.Duration(n) * time.Minute) }
	capacityR5Table(t, persistedActiveReservation, capacityTimesOrdered, []capacityR5Case[CapacityReservation]{
		{"ordered", capacityR5Unchanged, true},
		{"no provision or ready", func(r *CapacityReservation) { r.ProvisionedAt, r.ReadyAt = time.Time{}, time.Time{} }, true},
		{"provisioned at creation and update", func(r *CapacityReservation) {
			r.ProvisionedAt, r.ReadyAt, r.UpdatedAt = minutes(0), time.Time{}, minutes(0)
		}, true},
		{"provisioned before creation", func(r *CapacityReservation) { r.ProvisionedAt, r.ReadyAt = minutes(-1), time.Time{} }, false},
		{"provisioned after update", func(r *CapacityReservation) { r.ProvisionedAt, r.ReadyAt = minutes(11), time.Time{} }, false},
		{"ready without provision", func(r *CapacityReservation) { r.ProvisionedAt = time.Time{} }, true},
		{"ready at provision", func(r *CapacityReservation) { r.ReadyAt = r.ProvisionedAt }, true},
		{"ready at update", func(r *CapacityReservation) { r.ReadyAt = r.UpdatedAt }, true},
		{"ready before creation", func(r *CapacityReservation) { r.ProvisionedAt, r.ReadyAt = time.Time{}, minutes(-1) }, false},
		{"ready before provision", func(r *CapacityReservation) { r.ReadyAt = minutes(1) }, false},
		{"ready after update", func(r *CapacityReservation) { r.ReadyAt = minutes(11) }, false},
	})
}

func TestCapacityR5ObservationMatchesReservation(t *testing.T) {
	base := persistedActiveReservation
	matches := func(r CapacityReservation) bool { return capacityObservationMatchesReservation(r, *r.LastObservation) }
	capacityR5Table(t, base, matches, []capacityR5Case[CapacityReservation]{
		{"matching", capacityR5Unchanged, true},
		{"provider", func(r *CapacityReservation) { r.LastObservation.Provider = "other" }, false},
		{"resource key", func(r *CapacityReservation) { r.LastObservation.ResourceKey = "other" }, false},
		{"observed before creation", func(r *CapacityReservation) { r.LastObservation.ObservedAt = r.CreatedAt.Add(-time.Nanosecond) }, false},
		{"observed at creation", func(r *CapacityReservation) { r.LastObservation.ObservedAt = r.CreatedAt }, true},
		{"present without instance", func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "" }, false},
		{"present invalid instance", func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "a\x01" }, false},
		{"absent without instance", func(r *CapacityReservation) {
			r.LastObservation.State, r.LastObservation.ProviderInstanceID = "absent", ""
		}, true},
		{"absent invalid instance", func(r *CapacityReservation) {
			r.LastObservation.State, r.LastObservation.ProviderInstanceID = "absent", "a\x01"
		}, false},
		{"other instance", func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "inst-2" }, false},
		{"reservation without instance", func(r *CapacityReservation) { r.ProviderInstanceID = "" }, true},
	})
}

func TestCapacityR5ReadyAllowed(t *testing.T) {
	type input struct {
		reservation CapacityReservation
		readyAt     time.Time
	}
	base := func() input {
		r := persistedActiveReservation()
		return input{r, r.ProvisionedAt}
	}
	allowed := func(in input) bool { return capacityReadyAllowed(in.reservation, in.readyAt) }
	capacityR5Table(t, base, allowed, []capacityR5Case[input]{
		{"at provision", func(*input) {}, true},
		{"adopted", func(in *input) { in.reservation.Adoption = &CapacityAdoption{} }, false},
		{"not active", func(in *input) { in.reservation.State = CapacityDestroyOutcomeUnknown }, false},
		{"no observation", func(in *input) { in.reservation.LastObservation = nil }, false},
		{"observed absent", func(in *input) { in.reservation.LastObservation.State = "absent" }, false},
		{"before provision", func(in *input) { in.readyAt = in.reservation.ProvisionedAt.Add(-time.Nanosecond) }, false},
		{"before creation without provision", func(in *input) {
			in.reservation.ProvisionedAt, in.readyAt = time.Time{}, in.reservation.CreatedAt.Add(-time.Nanosecond)
		}, false},
		{"at creation without provision", func(in *input) { in.reservation.ProvisionedAt, in.readyAt = time.Time{}, in.reservation.CreatedAt }, true},
	})
}

// capacityR5DestroyRecord is the server.destroy result and absence step
// output in the shape the store decodes, so a case can break one field.
type capacityR5DestroyRecord struct {
	resultType string
	data       map[string]any
	absence    map[string]any
	stepID     string
	stepStatus string
	stepOutput json.RawMessage
}

func capacityR5DestroyInput(t *testing.T, record capacityR5DestroyRecord) OperationRecord {
	t.Helper()
	operation, _, _, _ := capacityRefactorDestroyFixture(t, capacityR5Base)
	operation.Result = capacityRefactorMarshal(t, map[string]any{"operation_type": record.resultType, "operation_data": record.data})
	output := record.stepOutput
	if output == nil {
		output = capacityRefactorMarshal(t, record.absence)
	}
	operation.Steps[1] = OperationStep{StepID: record.stepID, Status: protocolstate.StepStatus(record.stepStatus), Output: output}
	return operation
}

func capacityR5DestroyBase(t *testing.T) func() capacityR5DestroyRecord {
	return func() capacityR5DestroyRecord {
		_, _, result, absence := capacityRefactorDestroyFixture(t, capacityR5Base)
		return capacityR5DestroyRecord{
			resultType: "server.destroy", data: result["operation_data"].(map[string]any), absence: absence,
			stepID: "verify-instance-absence", stepStatus: "succeeded",
		}
	}
}

func TestCapacityR5DestroyResultHasEvidence(t *testing.T) {
	_, server, _, _ := capacityRefactorDestroyFixture(t, capacityR5Base)
	has := func(record capacityR5DestroyRecord) bool {
		return destroyResultHasEvidence(capacityR5DestroyInput(t, record), server)
	}
	capacityR5Table(t, capacityR5DestroyBase(t), has, []capacityR5Case[capacityR5DestroyRecord]{
		{"exact", func(*capacityR5DestroyRecord) {}, true},
		{"result type", func(r *capacityR5DestroyRecord) { r.resultType = "server.stop" }, false},
		{"instance ref", func(r *capacityR5DestroyRecord) { r.data["instance_ref"] = json.RawMessage(`{"external_id":"vm-b"}`) }, false},
		{"instance ref reordered", func(r *capacityR5DestroyRecord) {
			r.data["instance_ref"] = json.RawMessage(`{"external_id":"vm-a","resource_kind":"instance","environment_id":"staging","adapter_id":"hosting.vultr"}`)
		}, true},
		{"no backup", func(r *capacityR5DestroyRecord) { r.data["verified_backup_id"] = "" }, false},
		{"other generation", func(r *capacityR5DestroyRecord) { r.data["data_generation_id"] = "gen-2" }, false},
		{"absence unconfirmed", func(r *capacityR5DestroyRecord) { r.data["provider_absence_confirmed"] = false }, false},
		{"final state", func(r *capacityR5DestroyRecord) { r.data["final_lifecycle_state"] = "stopped" }, false},
	})
	operation := capacityR5DestroyInput(t, capacityR5DestroyBase(t)())
	operation.Result = json.RawMessage(`{"operation_type":`)
	if destroyResultHasEvidence(operation, server) {
		t.Fatal("undecodable result accepted")
	}
}

func TestCapacityR5DestroyAbsenceObservedAt(t *testing.T) {
	observedAt := func(record capacityR5DestroyRecord) bool {
		at, ok := destroyAbsenceObservedAt(capacityR5DestroyInput(t, record))
		if ok && !at.Equal(capacityR5Base) {
			t.Fatalf("observed at %v, want %v", at, capacityR5Base)
		}
		if !ok && !at.IsZero() {
			t.Fatalf("rejected absence returned time %v", at)
		}
		return ok
	}
	capacityR5Table(t, capacityR5DestroyBase(t), observedAt, []capacityR5Case[capacityR5DestroyRecord]{
		{"exact", func(*capacityR5DestroyRecord) {}, true},
		{"no step", func(r *capacityR5DestroyRecord) { r.stepID = "verify-other" }, false},
		{"step failed", func(r *capacityR5DestroyRecord) { r.stepStatus = "failed" }, false},
		{"bad output", func(r *capacityR5DestroyRecord) { r.stepOutput = json.RawMessage("[") }, false},
		{"instance ref", func(r *capacityR5DestroyRecord) {
			r.absence["instance_ref"] = json.RawMessage(`{"external_id":"vm-b"}`)
		}, false},
		{"state", func(r *capacityR5DestroyRecord) { r.absence["state"] = "absent" }, false},
		{"zero time", func(r *capacityR5DestroyRecord) { r.absence["observed_at"] = time.Time{} }, false},
	})
	// Only the first step with the ID counts, even when a later one is good.
	record := capacityR5DestroyBase(t)()
	operation := capacityR5DestroyInput(t, record)
	good := operation.Steps[1]
	operation.Steps[1].Status = "failed"
	operation.Steps = append(operation.Steps, good)
	if _, ok := destroyAbsenceObservedAt(operation); ok {
		t.Fatal("later absence step used")
	}
}

func TestCapacityR5StartedCapacityReadiness(t *testing.T) {
	type input struct {
		assertion map[string]any
		result    json.RawMessage
		server    LogicalServer
	}
	ref := json.RawMessage(capacityRefactorInstanceRef)
	at := capacityR5Base.Add(time.Minute)
	base := func() input {
		return input{
			assertion: map[string]any{"server_id": "srv", "environment_id": "staging", "instance_ref": ref, "observed_at": at},
			server:    LogicalServer{ServerID: "srv", EnvironmentID: "staging", InstanceRef: ref, LifecycleState: "ready"},
		}
	}
	ready := func(in input) bool {
		result := in.result
		if result == nil {
			result = capacityRefactorMarshal(t, map[string]any{"operation_data": map[string]any{"readiness_assertion": in.assertion}})
		}
		instance, observedAt, ok := startedCapacityReadiness(OperationRecord{Result: result}, in.server)
		if !ok && (instance != startedCapacityInstance{} || !observedAt.IsZero()) {
			t.Fatalf("rejected readiness returned %+v, %v", instance, observedAt)
		}
		if ok && (instance != startedCapacityInstance{ExternalID: "vm-a", AdapterID: "hosting.vultr"} || !observedAt.Equal(at)) {
			t.Fatalf("readiness = %+v, %v", instance, observedAt)
		}
		return ok
	}
	bare := json.RawMessage(`{"external_id":"vm-a"}`)
	capacityR5Table(t, base, ready, []capacityR5Case[input]{
		{"exact", func(*input) {}, true},
		{"bad result", func(in *input) { in.result = json.RawMessage("x") }, false},
		{"server", func(in *input) { in.assertion["server_id"] = "other" }, false},
		{"environment", func(in *input) { in.assertion["environment_id"] = "prod" }, false},
		{"instance ref", func(in *input) { in.assertion["instance_ref"] = bare }, false},
		{"zero time", func(in *input) { in.assertion["observed_at"] = time.Time{} }, false},
		{"server ref not an object", func(in *input) {
			in.server.InstanceRef, in.assertion["instance_ref"] = json.RawMessage(`[]`), json.RawMessage(`[]`)
		}, false},
		{"no adapter", func(in *input) { in.server.InstanceRef, in.assertion["instance_ref"] = bare, bare }, false},
		{"no external", func(in *input) {
			ref := json.RawMessage(`{"adapter_id":"hosting.vultr"}`)
			in.server.InstanceRef, in.assertion["instance_ref"] = ref, ref
		}, false},
		{"external not a string", func(in *input) {
			ref := json.RawMessage(`{"adapter_id":"hosting.vultr","external_id":7}`)
			in.server.InstanceRef, in.assertion["instance_ref"] = ref, ref
		}, false},
	})
}

func TestCapacityR5FindDestroyedCapacityReservation(t *testing.T) {
	const (
		mismatch = capacityRefactorConflictMsg + ": managed VM reservation does not match the destroyed instance"
		multiple = capacityRefactorConflictMsg + ": multiple active managed VM reservations match one destroyed server"
	)
	server := LogicalServer{ServerID: "srv", EnvironmentID: "staging"}
	instance := destroyedCapacityInstance{AdapterID: "hosting.vultr", ResourceKind: "instance", ExternalID: "vm-a"}
	find := func(t *testing.T, repository *BoltStore) (*CapacityReservation, error) {
		t.Helper()
		var match *CapacityReservation
		err := repository.db.View(func(tx *bolt.Tx) error {
			var err error
			match, err = findDestroyedCapacityReservation(tx.Bucket(capacityReservationsBucket), server, instance)
			return err
		})
		return match, err
	}
	put := func(t *testing.T, repository *BoltStore, operationID string, mutate func(*CapacityReservation)) {
		t.Helper()
		r := persistedActiveReservation()
		r.OperationID, r.ResourceKey, r.ProviderInstanceID = operationID, "key-"+operationID, "vm-a"
		r.LastObservation.ResourceKey, r.LastObservation.ProviderInstanceID = r.ResourceKey, "vm-a"
		mutate(&r)
		capacityRefactorPut(t, repository, r)
	}
	t.Run("candidates and matches", func(t *testing.T) {
		for name, tc := range map[string]struct {
			mutate  func(*CapacityReservation)
			found   bool
			message string
		}{
			"exact":        {func(*CapacityReservation) {}, true, ""},
			"no adapter":   {func(r *CapacityReservation) { r.ProviderAdapterID = "" }, true, ""},
			"other server": {func(r *CapacityReservation) { r.ServerID, r.ProviderAdapterID = "other", "hosting.x" }, false, ""},
			"other env": {func(r *CapacityReservation) {
				r.EnvironmentID, r.ProviderInstanceID, r.LastObservation.ProviderInstanceID = "prod", "vm-x", "vm-x"
			}, false, ""},
			"released": {func(r *CapacityReservation) {
				r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityDestroyed, "destroyed", time.Time{}
				r.ProviderAdapterID = "hosting.x"
			}, false, ""},
			"other adapter": {func(r *CapacityReservation) { r.ProviderAdapterID = "hosting.x" }, false, mismatch},
			"other instance": {func(r *CapacityReservation) {
				r.ProviderInstanceID, r.LastObservation.ProviderInstanceID = "vm-x", "vm-x"
			}, false, mismatch},
		} {
			t.Run(name, func(t *testing.T) {
				repository := openTestStore(t)
				put(t, repository, "op-a", tc.mutate)
				match, err := find(t, repository)
				if tc.message != "" {
					capacityRefactorAssertErr(t, err, ErrCapacityConflict, tc.message)
					if match != nil {
						t.Fatalf("conflict returned %+v", match)
					}
					return
				}
				if err != nil || (match != nil) != tc.found {
					t.Fatalf("find = %+v, %v; want found %v", match, err, tc.found)
				}
				if tc.found && match.OperationID != "op-a" {
					t.Fatalf("match = %+v", match)
				}
			})
		}
	})
	t.Run("second match", func(t *testing.T) {
		repository := openTestStore(t)
		put(t, repository, "op-a", func(*CapacityReservation) {})
		put(t, repository, "op-b", func(*CapacityReservation) {})
		match, err := find(t, repository)
		capacityRefactorAssertErr(t, err, ErrCapacityConflict, multiple)
		if match != nil {
			t.Fatalf("conflict returned %+v", match)
		}
	})
	t.Run("corrupt entry", func(t *testing.T) {
		repository := openTestStore(t)
		put(t, repository, "op-a", func(*CapacityReservation) {})
		capacityRefactorRawPut(t, repository, "op-b", []byte("{"))
		match, err := find(t, repository)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
		if !strings.HasPrefix(err.Error(), capacityRefactorCorrupt+": decode capacity reservation: ") || match != nil {
			t.Fatalf("corrupt = %+v, %v", match, err)
		}
	})
}

func TestCapacityR5ReserveScanAndLimits(t *testing.T) {
	const (
		occupied = "managed VM slot is occupied: server \"srv\" in environment \"staging\""
		keyTaken = capacityRefactorConflictMsg + ": provider resource key \"gsc-srv\" is already reserved"
	)
	reserveIn := func(t *testing.T, repository *BoltStore, request CapacityReservation, total, group int) error {
		t.Helper()
		before := capacityRefactorRawGet(t, repository, request.OperationID)
		result, created, err := repository.ReserveCapacity(request, total, group)
		if err != nil && (created || result != CapacityReservation{}) {
			t.Fatalf("rejected reserve returned %+v, %v", result, created)
		}
		if err != nil {
			capacityRefactorAssertStored(t, repository, request.OperationID, before)
		}
		return err
	}
	t.Run("occupied and resource key", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		capacityRefactorAssertErr(t, reserveIn(t, repository, capacityRequest("op-b", "srv", "staging", "g"), 9, 9), ErrCapacityOccupied, occupied)
		other := capacityRequest("op-c", "srv-2", "staging", "g")
		other.ResourceKey = "gsc-srv"
		capacityRefactorAssertErr(t, reserveIn(t, repository, other, 9, 9), ErrCapacityConflict, keyTaken)
		other.Provider = "other-provider"
		if err := reserveIn(t, repository, other, 9, 9); err != nil {
			t.Fatalf("same key at another provider: %v", err)
		}
		sameServerOtherEnv := capacityRequest("op-d", "srv", "prod", "g")
		sameServerOtherEnv.ResourceKey = "gsc-srv-prod"
		if err := reserveIn(t, repository, sameServerOtherEnv, 9, 9); err != nil {
			t.Fatalf("same server in another environment: %v", err)
		}
	})
	t.Run("total and group limits", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv-a", "staging", "g1"))
		capacityRefactorAssertSentinel(t, reserveIn(t, repository, capacityRequest("op-b", "srv-b", "staging", "g2"), 1, 9), ErrCapacityReached)
		capacityRefactorAssertSentinel(t, reserveIn(t, repository, capacityRequest("op-b", "srv-b", "staging", "g1"), 9, 1), ErrCapacityReached)
		if err := reserveIn(t, repository, capacityRequest("op-b", "srv-b", "staging", "g2"), 2, 1); err != nil {
			t.Fatalf("other group under the total: %v", err)
		}
		capacityRefactorAssertSentinel(t, reserveIn(t, repository, capacityRequest("op-c", "srv-c", "staging", "g3"), 2, 9), ErrCapacityReached)
	})
	t.Run("replay of a corrupt reservation", func(t *testing.T) {
		repository := openTestStore(t)
		capacityRefactorRawPut(t, repository, "op-a", []byte(`{"operation_id":"op-a"}`))
		err := reserveIn(t, repository, capacityRequest("op-a", "srv", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": invalid capacity reservation: "+capacityRefactorInvalid)
	})
	t.Run("replay is checked before the scan", func(t *testing.T) {
		repository := openTestStore(t)
		reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		capacityRefactorRawPut(t, repository, "op-b", []byte("{"))
		result, created, err := repository.ReserveCapacity(capacityRequest("op-a", "srv", "staging", "g"), 1, 1)
		if err != nil || created || capacityRefactorJSON(t, result) != capacityRefactorJSON(t, reserved) {
			t.Fatalf("replay = %+v, %v, %v", result, created, err)
		}
	})
}

func TestCapacityR5DispatchClaimsAndUpdates(t *testing.T) {
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	before := capacityRefactorRawGet(t, repository, "op-a")
	_, err := repository.MarkCapacityDestroySent("op-a")
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	capacityRefactorAssertStored(t, repository, "op-a", before)
	claimed, err := repository.MarkCapacityCreateSent("op-a")
	if err != nil || claimed.State != CapacityCreateOutcomeUnknown || claimed.UpdatedAt.Before(reserved.UpdatedAt) {
		t.Fatalf("create claim = %+v, %v", claimed, err)
	}
	if loaded := capacityRefactorLoad(t, repository, "op-a"); capacityRefactorJSON(t, loaded) != capacityRefactorJSON(t, claimed) {
		t.Fatalf("stored %+v, returned %+v", loaded, claimed)
	}
	for _, claim := range []func(string) (CapacityReservation, error){repository.MarkCapacityCreateSent, repository.MarkCapacityDestroySent} {
		result, err := claim("op-a")
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		if (result != CapacityReservation{}) {
			t.Fatalf("rejected claim returned %+v", result)
		}
		result, err = claim(strings.Repeat("o", 201))
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		if (result != CapacityReservation{}) {
			t.Fatalf("invalid claim returned %+v", result)
		}
	}
	result, err := repository.GetCapacityReservation("missing")
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
	if (result != CapacityReservation{}) {
		t.Fatalf("missing get returned %+v", result)
	}
}

func TestCapacityR5RecordCapacityReadyReturnsMatchWithError(t *testing.T) {
	repository := openTestStore(t)
	active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	readyAt := active.LastObservation.ObservedAt
	first, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt)
	if err != nil || !first.ReadyAt.Equal(readyAt) {
		t.Fatalf("first ready = %+v, %v", first, err)
	}
	stored := capacityRefactorRawGet(t, repository, "op-a")
	again, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt.Add(time.Second))
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	if capacityRefactorJSON(t, again) != capacityRefactorJSON(t, first) {
		t.Fatalf("conflict returned %+v, want %+v", again, first)
	}
	capacityRefactorAssertStored(t, repository, "op-a", stored)
	for _, args := range [][3]string{{"", "staging", "vm-a"}, {"srv", strings.Repeat("e", 201), "vm-a"}, {"srv", "staging", strings.Repeat("v", 241)}} {
		result, err := repository.RecordCapacityReady(args[0], args[1], args[2], readyAt)
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid readiness measurement")
		if (result != CapacityReservation{}) {
			t.Fatalf("invalid readiness returned %+v", result)
		}
	}
}
