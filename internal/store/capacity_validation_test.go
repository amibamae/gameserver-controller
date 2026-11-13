package store

import (
	"errors"
	"testing"
	"time"
)

// persistedActiveReservation returns a valid active reservation that was
// provisioned, became ready, and was last observed present.
func persistedActiveReservation() CapacityReservation {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return CapacityReservation{
		ProviderAdapterID: "hosting.vultr", OperationID: "op-1", ServerID: "srv", EnvironmentID: "staging",
		CapacityGroup: "pilot", Provider: "vultr", ResourceKey: "key-1", ProviderInstanceID: "inst-1",
		State: CapacityActive, CreatedAt: created, UpdatedAt: created.Add(10 * time.Minute),
		ProvisionedAt: created.Add(2 * time.Minute), ReadyAt: created.Add(5 * time.Minute),
		LastObservation: &CapacityProviderObservation{
			Provider: "vultr", ResourceKey: "key-1", ProviderInstanceID: "inst-1", State: "present",
			ObservedAt: created.Add(3 * time.Minute), ProvisionedAt: created.Add(2 * time.Minute),
		},
	}
}

func TestValidatePersistedCapacityReservationRules(t *testing.T) {
	if err := validatePersistedCapacityReservation(persistedActiveReservation()); err != nil {
		t.Fatalf("valid reservation rejected: %v", err)
	}
	minutes := func(n int) time.Time {
		return persistedActiveReservation().CreatedAt.Add(time.Duration(n) * time.Minute)
	}
	tests := []struct {
		rule   string
		mutate func(*CapacityReservation)
	}{
		{"adoption for another instance", func(r *CapacityReservation) {
			r.ProvisionedAt, r.ReadyAt = time.Time{}, time.Time{}
			r.Adoption = &CapacityAdoption{InstanceID: "inst-2", IP: "149.28.1.2", Region: "ewr", Plan: "vc2-1c-1gb", OSID: 2136, OS: "Debian 12 x64 (bookworm)", ObservedAt: r.CreatedAt}
		}},
		{"adopted reservation with a provision time", func(r *CapacityReservation) {
			r.Adoption = &CapacityAdoption{InstanceID: "inst-1", IP: "149.28.1.2", Region: "ewr", Plan: "vc2-1c-1gb", OSID: 2136, OS: "Debian 12 x64 (bookworm)", ObservedAt: r.CreatedAt}
		}},
		{"invalid provider adapter ID", func(r *CapacityReservation) { r.ProviderAdapterID = "bad\x01" }},
		{"missing operation ID", func(r *CapacityReservation) { r.OperationID = "" }},
		{"missing resource key", func(r *CapacityReservation) { r.ResourceKey = "" }},
		{"no creation time", func(r *CapacityReservation) { r.CreatedAt = time.Time{} }},
		{"updated before created", func(r *CapacityReservation) { r.UpdatedAt = minutes(-1) }},
		{"unknown state", func(r *CapacityReservation) { r.State = "paused" }},
		{"active without a provider instance", func(r *CapacityReservation) { r.ProviderInstanceID = ""; r.LastObservation.ProviderInstanceID = "" }},
		{"provisioned before created", func(r *CapacityReservation) { r.ProvisionedAt = minutes(-1) }},
		{"provisioned after the last update", func(r *CapacityReservation) { r.ProvisionedAt = minutes(11) }},
		{"ready before provisioned", func(r *CapacityReservation) { r.ReadyAt = minutes(1) }},
		{"ready after the last update", func(r *CapacityReservation) { r.ReadyAt = minutes(11) }},
		{"observation of another provider", func(r *CapacityReservation) { r.LastObservation.Provider = "other" }},
		{"observation of another resource key", func(r *CapacityReservation) { r.LastObservation.ResourceKey = "key-2" }},
		{"observation before creation", func(r *CapacityReservation) { r.LastObservation.ObservedAt = minutes(-1) }},
		{"observation of another instance", func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "inst-2" }},
		{"observed provision after the observation", func(r *CapacityReservation) { r.LastObservation.ProvisionedAt = minutes(4) }},
		{"unknown observation state", func(r *CapacityReservation) { r.LastObservation.State = "unknown" }},
		{"active reservation observed absent", func(r *CapacityReservation) {
			r.LastObservation.State, r.LastObservation.ProvisionedAt = "absent", time.Time{}
		}},
		{"destroyed reservation observed present", func(r *CapacityReservation) { r.State = CapacityDestroyed }},
	}
	for _, test := range tests {
		t.Run(test.rule, func(t *testing.T) {
			reservation := persistedActiveReservation()
			test.mutate(&reservation)
			if err := validatePersistedCapacityReservation(reservation); !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("validatePersistedCapacityReservation = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

func TestValidatePersistedCapacityReservationAcceptsAdoption(t *testing.T) {
	reservation := persistedActiveReservation()
	reservation.ProvisionedAt, reservation.ReadyAt, reservation.LastObservation.ProvisionedAt = time.Time{}, time.Time{}, time.Time{}
	reservation.Adoption = &CapacityAdoption{InstanceID: "inst-1", IP: "149.28.1.2", Region: "ewr", Plan: "vc2-1c-1gb", OSID: 2136, OS: "Debian 12 x64 (bookworm)", ObservedAt: reservation.CreatedAt}
	if err := validatePersistedCapacityReservation(reservation); err != nil {
		t.Fatalf("valid adopted reservation rejected: %v", err)
	}
}
