package pilotvultr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

var ErrAdoptionMismatch = errors.New("Vultr adoption inventory or exact target metadata does not match")

// AdoptionProvider intentionally exposes no provider mutations.
type AdoptionProvider interface {
	ListInstances(context.Context, []byte) ([]vultrapi.Instance, error)
	GetInstance(context.Context, []byte, string) (vultrapi.Instance, error)
}

// AdoptionRequest comes from an explicitly approved target/profile. No
// deployment identities, addresses, provider plans, or regions are hardcoded.
type AdoptionRequest struct {
	OperationID string
	ServerID    string
	Environment string
	InstanceID  string
	IP          string
	Region      string
	Plan        string
	OSID        int
	OS          string
}

type Adopter struct {
	store       *store.BoltStore
	policy      pilotcapacity.Policy
	provider    AdoptionProvider
	credentials CredentialResolver
	credential  string
}

func NewAdopter(repository *store.BoltStore, policy pilotcapacity.Policy, provider AdoptionProvider, credentials CredentialResolver, credentialRef string) (*Adopter, error) {
	if repository == nil || policy.Validate() != nil || !policy.RuntimeEnforced || provider == nil || credentials == nil || credentialRef == "" {
		return nil, ErrInvalidRequest
	}
	return &Adopter{repository, policy, provider, credentials, credentialRef}, nil
}

func ValidateAdoptionRequest(request AdoptionRequest) error {
	ip, err := netip.ParseAddr(request.IP)
	if !operationIDPattern.MatchString(request.OperationID) || !safeAdoptionID(request.ServerID) || !safeAdoptionID(request.InstanceID) ||
		err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || request.IP != ip.String() ||
		len(request.OS) < 1 || len(request.OS) > 500 || strings.TrimSpace(request.OS) != request.OS {
		return ErrInvalidRequest
	}
	for _, c := range request.OS {
		if c < 0x20 || c == 0x7f {
			return ErrInvalidRequest
		}
	}
	if vultrapi.ValidateCreateInstanceRequest(vultrapi.CreateInstanceRequest{Region: request.Region, Plan: request.Plan, OSID: request.OSID, Label: "adoption-validation"}) != nil {
		return ErrInvalidRequest
	}
	return nil
}

func safeAdoptionID(value string) bool {
	if len(value) < 1 || len(value) > 200 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// Adopt audits existing ownership against complete live inventory and verifies
// the exact requested instance twice (list and direct lookup). Only local
// ownership/capacity is changed. It never creates, relabels, starts, stops, or
// destroys a VM and never infers game readiness from provider existence.
func (a *Adopter) Adopt(ctx context.Context, request AdoptionRequest) (store.CapacityReservation, error) {
	if a == nil || ctx == nil || ctx.Err() != nil || ValidateAdoptionRequest(request) != nil {
		return store.CapacityReservation{}, ErrInvalidRequest
	}
	groupID, group, ok := a.policy.GroupForEnvironment(request.Environment)
	if !ok {
		return store.CapacityReservation{}, pilotcapacity.ErrUnsupportedEnvironment
	}
	var result store.CapacityReservation
	err := a.credentials.WithCredential(a.credential, func(token []byte) error {
		inventory, err := a.provider.ListInstances(ctx, token)
		if err != nil {
			return ErrAdoptionMismatch
		}
		live := make(map[string]vultrapi.Instance, len(inventory))
		for _, instance := range inventory {
			if !safeAdoptionID(instance.ID) {
				return ErrAdoptionMismatch
			}
			if _, exists := live[instance.ID]; exists {
				return ErrAdoptionMismatch
			}
			live[instance.ID] = instance
		}
		reservations, err := a.store.ListCapacityReservations()
		if err != nil {
			return err
		}
		for _, reservation := range reservations {
			if reservation.Provider != providerID || a.policy.ReleasedStates[string(reservation.State)] {
				continue
			}
			instance, exists := live[reservation.ProviderInstanceID]
			if reservation.State != store.CapacityActive || !exists {
				return ErrAdoptionMismatch
			}
			if reservation.Adoption != nil {
				if !matchesAdoption(instance, *reservation.Adoption) {
					return ErrAdoptionMismatch
				}
			} else if instance.Label != reservation.ResourceKey {
				return ErrAdoptionMismatch
			}
		}
		expected := store.CapacityAdoption{InstanceID: request.InstanceID, IP: request.IP, Region: request.Region,
			Plan: request.Plan, OSID: request.OSID, OS: request.OS}
		candidate, exists := live[request.InstanceID]
		// A managed create label may belong to an unresolved operation. That
		// operation must be reconciled instead of bypassed through adoption.
		if !exists || strings.HasPrefix(candidate.Label, "gsc-managed-") || !matchesAdoption(candidate, expected) {
			return ErrAdoptionMismatch
		}
		observed, err := a.provider.GetInstance(ctx, token, request.InstanceID)
		if err != nil || !matchesAdoption(observed, expected) || observed.Label != candidate.Label || ctx.Err() != nil {
			return ErrAdoptionMismatch
		}
		expected.ObservedAt = time.Now().UTC()
		// Hash identity into the resource key so unrelated labels cannot collide.
		digest := sha256.Sum256([]byte(request.InstanceID))
		result, err = a.store.AdoptCapacity(store.CapacityReservation{OperationID: request.OperationID, ServerID: request.ServerID,
			EnvironmentID: request.Environment, CapacityGroup: groupID, Provider: providerID, ProviderAdapterID: "hosting.vultr",
			ResourceKey: "adopted-vultr-" + hex.EncodeToString(digest[:])}, expected, a.policy.MaxInstancesTotal, group.MaxInstances)
		return err
	})
	return result, err
}

func matchesAdoption(instance vultrapi.Instance, expected store.CapacityAdoption) bool {
	return instance.ID == expected.InstanceID && instance.MainIP == expected.IP && instance.Region == expected.Region &&
		instance.Plan == expected.Plan && instance.OSID == expected.OSID && instance.OS == expected.OS
}
