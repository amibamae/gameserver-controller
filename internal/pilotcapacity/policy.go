package pilotcapacity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var (
	ErrInvalidPolicy          = errors.New("invalid pilot capacity policy")
	ErrUnsupportedEnvironment = errors.New("unsupported pilot capacity environment")
)

type Policy struct {
	PolicyID          string
	PolicyVersion     int
	RuntimeEnforced   bool
	MaxInstancesTotal int
	CapacityGroups    map[string]Group
	ReleasedStates    map[string]bool
}

type Group struct {
	MaxInstances         int      `json:"max_instances"`
	AcceptedEnvironments []string `json:"accepted_environments"`
}

type policyDocument struct {
	PolicyID          string            `json:"policy_id"`
	PolicyVersion     int               `json:"policy_version"`
	Enforcement       enforcementPolicy `json:"enforcement"`
	MaxInstancesTotal int               `json:"max_instances_total"`
	OwnershipScope    ownershipScope    `json:"ownership_scope"`
	CapacityGroups    map[string]Group  `json:"capacity_groups"`
	CreatePolicy      createPolicy      `json:"create_policy"`
}

type enforcementPolicy struct {
	Mode            string `json:"mode"`
	RuntimeEnforced *bool  `json:"runtime_enforced"`
	Reason          string `json:"reason"`
}

type ownershipScope struct {
	CapacityCountsOnlyControllerInstances *bool `json:"capacity_counts_only_controller_owned_instances"`
	UnmanagedProductionMayRemainLive      *bool `json:"unmanaged_production_may_remain_live"`
	ProductionAdoptionIsOptional          *bool `json:"production_adoption_is_optional"`
	ProviderCreatedInstancesManaged       *bool `json:"provider_created_instances_are_controller_managed"`
}

type createPolicy struct {
	ReserveBeforeProviderCreate  *bool           `json:"reserve_slot_atomically_before_provider_create"`
	ReservationConsumesCapacity  *bool           `json:"reservation_counts_as_capacity"`
	RejectDuplicateOrOccupied    *bool           `json:"reject_duplicate_or_occupied_slot"`
	RejectOverTotalLimit         *bool           `json:"reject_if_total_would_exceed_limit"`
	RejectUnsupportedEnvironment *bool           `json:"reject_unsupported_environment"`
	Occupancy                    occupancyPolicy `json:"occupancy"`
}

type occupancyPolicy struct {
	CountManagedUntilConfirmed *bool    `json:"count_every_managed_instance_without_provider_confirmed_absence_or_destruction"`
	CountUnknownCreate         *bool    `json:"count_unresolved_create_outcome"`
	CountUnknownDestroy        *bool    `json:"count_unresolved_destroy_outcome"`
	ReleaseOnlyAfter           []string `json:"release_only_after_provider_confirmation"`
}

func LoadPolicy(path string) (Policy, error) {
	file, err := os.Open(path)
	if err != nil {
		return Policy{}, fmt.Errorf("open capacity policy: %w", err)
	}
	defer file.Close()
	return DecodePolicy(file)
}

func DecodePolicy(reader io.Reader) (Policy, error) {
	if reader == nil {
		return Policy{}, fmt.Errorf("%w: input is required", ErrInvalidPolicy)
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var document policyDocument
	if err := decoder.Decode(&document); err != nil {
		return Policy{}, fmt.Errorf("%w: decode: %v", ErrInvalidPolicy, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Policy{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidPolicy)
		}
		return Policy{}, fmt.Errorf("%w: trailing data: %v", ErrInvalidPolicy, err)
	}
	policy, err := policyFromDocument(document)
	if err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func policyFromDocument(document policyDocument) (Policy, error) {
	if document.PolicyID != "supervised-pilot-vm-capacity" || document.PolicyVersion < 2 || document.MaxInstancesTotal < 1 {
		return Policy{}, fmt.Errorf("%w: invalid identity, version, or total limit", ErrInvalidPolicy)
	}
	if document.Enforcement.Mode != "declarative" && document.Enforcement.Mode != "runtime" {
		return Policy{}, fmt.Errorf("%w: enforcement mode must be declarative or runtime", ErrInvalidPolicy)
	}
	if document.Enforcement.RuntimeEnforced == nil || !*document.Enforcement.RuntimeEnforced {
		if document.Enforcement.Mode == "runtime" {
			return Policy{}, fmt.Errorf("%w: runtime enforcement mode requires runtime_enforced=true", ErrInvalidPolicy)
		}
	} else if document.Enforcement.Mode == "declarative" {
		return Policy{}, fmt.Errorf("%w: declarative mode requires runtime_enforced=false", ErrInvalidPolicy)
	}
	ownership := document.OwnershipScope
	if !isTrue(ownership.CapacityCountsOnlyControllerInstances) || !isTrue(ownership.UnmanagedProductionMayRemainLive) ||
		!isTrue(ownership.ProductionAdoptionIsOptional) || !isTrue(ownership.ProviderCreatedInstancesManaged) {
		return Policy{}, fmt.Errorf("%w: ownership scope must preserve controller-only capacity and optional production adoption", ErrInvalidPolicy)
	}
	create := document.CreatePolicy
	if !isTrue(create.ReserveBeforeProviderCreate) || !isTrue(create.ReservationConsumesCapacity) ||
		!isTrue(create.RejectDuplicateOrOccupied) || !isTrue(create.RejectOverTotalLimit) || !isTrue(create.RejectUnsupportedEnvironment) ||
		!isTrue(create.Occupancy.CountManagedUntilConfirmed) || !isTrue(create.Occupancy.CountUnknownCreate) ||
		!isTrue(create.Occupancy.CountUnknownDestroy) || !sameStates(create.Occupancy.ReleaseOnlyAfter, []string{"absent", "destroyed"}) {
		return Policy{}, fmt.Errorf("%w: create and occupancy rules are incomplete", ErrInvalidPolicy)
	}
	if len(document.CapacityGroups) == 0 {
		return Policy{}, fmt.Errorf("%w: capacity groups are required", ErrInvalidPolicy)
	}
	seenEnvironments := map[string]bool{}
	groupTotal := 0
	for groupID, group := range document.CapacityGroups {
		if groupID == "" || group.MaxInstances < 1 || len(group.AcceptedEnvironments) == 0 {
			return Policy{}, fmt.Errorf("%w: invalid capacity group %q", ErrInvalidPolicy, groupID)
		}
		groupTotal += group.MaxInstances
		for _, environment := range group.AcceptedEnvironments {
			if environment == "" || seenEnvironments[environment] {
				return Policy{}, fmt.Errorf("%w: empty or duplicate environment %q", ErrInvalidPolicy, environment)
			}
			seenEnvironments[environment] = true
		}
	}
	if document.MaxInstancesTotal > groupTotal {
		return Policy{}, fmt.Errorf("%w: total limit exceeds the sum of group capacities", ErrInvalidPolicy)
	}
	return Policy{
		PolicyID: document.PolicyID, PolicyVersion: document.PolicyVersion,
		RuntimeEnforced:   document.Enforcement.RuntimeEnforced != nil && *document.Enforcement.RuntimeEnforced,
		MaxInstancesTotal: document.MaxInstancesTotal,
		CapacityGroups:    cloneGroups(document.CapacityGroups),
		ReleasedStates:    map[string]bool{"absent": true, "destroyed": true},
	}, nil
}

func (p Policy) Validate() error {
	if p.PolicyID != "supervised-pilot-vm-capacity" || p.PolicyVersion < 2 || p.MaxInstancesTotal < 1 || len(p.CapacityGroups) == 0 {
		return ErrInvalidPolicy
	}
	groupTotal := 0
	seen := map[string]bool{}
	for groupID, group := range p.CapacityGroups {
		if groupID == "" || group.MaxInstances < 1 || len(group.AcceptedEnvironments) == 0 {
			return ErrInvalidPolicy
		}
		groupTotal += group.MaxInstances
		for _, environment := range group.AcceptedEnvironments {
			if environment == "" || seen[environment] {
				return ErrInvalidPolicy
			}
			seen[environment] = true
		}
	}
	if p.MaxInstancesTotal > groupTotal {
		return ErrInvalidPolicy
	}
	if len(p.ReleasedStates) != 2 || !p.ReleasedStates["absent"] || !p.ReleasedStates["destroyed"] {
		return ErrInvalidPolicy
	}
	return nil
}

// ReserveCapacity applies this policy's environment-to-group mapping and
// limits in the same operation as the durable bbolt reservation.
func (p Policy) ReserveCapacity(repository *store.BoltStore, request store.CapacityReservation) (store.CapacityReservation, bool, error) {
	if err := p.Validate(); err != nil {
		return store.CapacityReservation{}, false, err
	}
	groupID, group, ok := p.GroupForEnvironment(request.EnvironmentID)
	if !ok {
		return store.CapacityReservation{}, false, ErrUnsupportedEnvironment
	}
	if request.CapacityGroup != "" && request.CapacityGroup != groupID {
		return store.CapacityReservation{}, false, fmt.Errorf("%w: environment maps to capacity group %q", ErrInvalidPolicy, groupID)
	}
	request.CapacityGroup = groupID
	return repository.ReserveCapacity(request, p.MaxInstancesTotal, group.MaxInstances)
}

func (p Policy) GroupForEnvironment(environment string) (string, Group, bool) {
	for groupID, group := range p.CapacityGroups {
		for _, candidate := range group.AcceptedEnvironments {
			if candidate == environment {
				return groupID, group, true
			}
		}
	}
	return "", Group{}, false
}

func cloneGroups(groups map[string]Group) map[string]Group {
	cloned := make(map[string]Group, len(groups))
	for name, group := range groups {
		cloned[name] = Group{MaxInstances: group.MaxInstances, AcceptedEnvironments: append([]string(nil), group.AcceptedEnvironments...)}
	}
	return cloned
}

func isTrue(value *bool) bool { return value != nil && *value }

func sameStates(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	a, b := append([]string(nil), actual...), append([]string(nil), expected...)
	sort.Strings(a)
	sort.Strings(b)
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
