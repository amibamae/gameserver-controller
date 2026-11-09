// Package pilotvultr wires the two-slot pilot policy to Vultr create and
// reconciliation calls. It does not contain credentials or make calls until
// a caller invokes Provision.
package pilotvultr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const providerID = "vultr"

var (
	ErrInvalidRequest   = errors.New("invalid pilot Vultr create request")
	ErrOutcomeUnknown   = errors.New("pilot Vultr create outcome is unknown")
	ErrMultipleMatches  = errors.New("multiple Vultr instances match the managed resource key")
	ErrProviderRejected = errors.New("Vultr rejected the pilot create request")
	ErrOperationClosed  = errors.New("pilot capacity operation is already released")
	ErrBootstrapScript  = errors.New("Vultr startup script ID or digest does not match the approved bootstrap")
	operationIDPattern  = regexp.MustCompile(`^[a-f0-9]{32}$`)
	sha256Pattern       = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type CredentialResolver interface {
	WithCredential(reference string, use func([]byte) error) error
}

type Provider interface {
	CreateInstance(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error)
	GetInstance(context.Context, []byte, string) (vultrapi.Instance, error)
	ListInstancesByLabel(context.Context, []byte, string) ([]vultrapi.Instance, error)
	GetStartupScript(context.Context, []byte, string) (vultrapi.StartupScript, error)
}

type CreateRequest struct {
	OperationID         string
	ServerID            string
	Environment         string
	Region              string
	Plan                string
	OSID                string
	Hostname            string
	SSHKeyIDs           []string
	StartupScriptID     string
	StartupScriptSHA256 string
}

type Provisioner struct {
	store       *store.BoltStore
	policy      pilotcapacity.Policy
	provider    Provider
	credentials CredentialResolver
	credential  string
	now         func() time.Time
}

func NewProvisioner(repository *store.BoltStore, policy pilotcapacity.Policy, provider Provider, credentials CredentialResolver, credentialRef string) (*Provisioner, error) {
	if repository == nil || policy.Validate() != nil || !policy.RuntimeEnforced || provider == nil || credentials == nil || credentialRef == "" {
		return nil, ErrInvalidRequest
	}
	return &Provisioner{
		store: repository, policy: policy, provider: provider,
		credentials: credentials, credential: credentialRef,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Provision reserves the configured capacity slot, marks the create request as
// dispatched exactly once, and verifies the resulting instance by its exact ID
// and unique controller label. Unknown outcomes are reconciled by label; they
// are never resent and never release capacity merely because a list is empty.
func (p *Provisioner) Provision(ctx context.Context, request CreateRequest) (store.CapacityReservation, error) {
	if p == nil || ctx == nil || ctx.Err() != nil {
		return store.CapacityReservation{}, ErrInvalidRequest
	}
	create, reservation, err := p.prepare(request)
	if err != nil {
		return store.CapacityReservation{}, err
	}
	existing, getErr := p.store.GetCapacityReservation(request.OperationID)
	if getErr == nil {
		if !sameReservationIdentity(existing, reservation) {
			legacyRequest := request
			legacyRequest.StartupScriptID = ""
			legacyRequest.StartupScriptSHA256 = ""
			legacyCreate, legacyReservation, legacyErr := p.prepare(legacyRequest)
			if legacyErr != nil || !sameReservationIdentity(existing, legacyReservation) {
				return existing, store.ErrCapacityConflict
			}
			switch existing.State {
			case store.CapacityActive, store.CapacityCreateOutcomeUnknown:
				// Keep legacy identity for safe replay or reconciliation. Never
				// redispatch an old create with a newly selected bootstrap.
				request, reservation = legacyRequest, legacyReservation
				create = legacyCreate
			case store.CapacityReserved:
				if !hasBootstrapReference(request) {
					return existing, ErrInvalidRequest
				}
				if releaseErr := p.store.ReleaseUnsentCapacityReservation(existing.OperationID, existing.ResourceKey); releaseErr != nil {
					current, currentErr := p.store.GetCapacityReservation(existing.OperationID)
					if currentErr == nil && sameReservationIdentity(current, legacyReservation) && current.State == store.CapacityCreateOutcomeUnknown {
						request, reservation = legacyRequest, legacyReservation
						create = legacyCreate
						existing = current
					} else if currentErr == nil && sameReservationIdentity(current, legacyReservation) && current.State == store.CapacityActive {
						return current, nil
					} else {
						return current, releaseErr
					}
				}
			default:
				return existing, store.ErrCapacityConflict
			}
		}
		switch existing.State {
		case store.CapacityActive:
			return existing, nil
		case store.CapacityAbsent, store.CapacityDestroyed:
			return existing, ErrOperationClosed
		}
	} else if !errors.Is(getErr, store.ErrCapacityNotFound) {
		return store.CapacityReservation{}, getErr
	}
	if !hasBootstrapReference(request) && (getErr != nil || existing.State != store.CapacityCreateOutcomeUnknown) {
		return store.CapacityReservation{}, ErrInvalidRequest
	}
	var previous *store.CapacityReservation
	if getErr == nil {
		previous = &existing
	}

	var result store.CapacityReservation
	err = p.credentials.WithCredential(p.credential, func(token []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if hasBootstrapReference(request) && (previous == nil || previous.State == store.CapacityReserved) {
			if err := p.verifyStartupScript(ctx, token, create.StartupScriptID, request.StartupScriptSHA256); err != nil {
				if previous != nil {
					if releaseErr := p.store.ReleaseUnsentCapacityReservation(previous.OperationID, previous.ResourceKey); releaseErr != nil {
						current, getErr := p.store.GetCapacityReservation(previous.OperationID)
						if getErr != nil {
							return ErrOutcomeUnknown
						}
						switch current.State {
						case store.CapacityCreateOutcomeUnknown:
							reconciled, reconcileErr := p.reconcile(ctx, token, current)
							result = reconciled
							return reconcileErr
						case store.CapacityActive:
							result = current
							return nil
						default:
							return ErrOutcomeUnknown
						}
					}
				}
				return err
			}
		}
		var reserveErr error
		result, _, reserveErr = p.policy.ReserveCapacity(p.store, reservation)
		if reserveErr != nil {
			return reserveErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		switch result.State {
		case store.CapacityActive:
			return nil
		case store.CapacityAbsent, store.CapacityDestroyed:
			return ErrOperationClosed
		case store.CapacityCreateOutcomeUnknown:
			result, reserveErr = p.reconcile(ctx, token, result)
			return reserveErr
		case store.CapacityReserved:
			// A crash before this one-shot claim is known to precede provider
			// dispatch. Concurrent callers can claim it only once.
		default:
			return store.ErrCapacityTransition
		}

		result, reserveErr = p.store.MarkCapacityCreateSent(result.OperationID)
		if errors.Is(reserveErr, store.ErrCapacityTransition) {
			result, reserveErr = p.store.GetCapacityReservation(reservation.OperationID)
			if reserveErr != nil {
				return reserveErr
			}
			switch result.State {
			case store.CapacityActive:
				return nil
			case store.CapacityCreateOutcomeUnknown:
				result, reserveErr = p.reconcile(ctx, token, result)
				return reserveErr
			case store.CapacityAbsent, store.CapacityDestroyed:
				return ErrOperationClosed
			default:
				return store.ErrCapacityTransition
			}
		}
		if reserveErr != nil {
			return reserveErr
		}

		created, createErr := p.provider.CreateInstance(ctx, token, create)
		if errors.Is(createErr, vultrapi.ErrProviderReject) {
			result, reserveErr = p.recordAbsence(result)
			if reserveErr != nil {
				return ErrOutcomeUnknown
			}
			return ErrProviderRejected
		}
		if createErr != nil {
			result, reserveErr = p.reconcile(ctx, token, result)
			if reserveErr == nil {
				return nil
			}
			return reserveErr
		}
		if created.ID == "" || created.Label != create.Label {
			result, reserveErr = p.reconcile(ctx, token, result)
			if reserveErr == nil {
				return nil
			}
			return reserveErr
		}

		observed, inspectErr := p.provider.GetInstance(ctx, token, created.ID)
		if inspectErr == nil && observed.ID == created.ID && observed.Label == create.Label {
			result, reserveErr = p.recordPresent(result, observed)
			return reserveErr
		}
		result, reserveErr = p.reconcile(ctx, token, result)
		if reserveErr == nil {
			return nil
		}
		return reserveErr
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func (p *Provisioner) prepare(request CreateRequest) (vultrapi.CreateInstanceRequest, store.CapacityReservation, error) {
	if !operationIDPattern.MatchString(request.OperationID) || request.ServerID == "" || request.Environment == "" {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	groupID, _, ok := p.policy.GroupForEnvironment(request.Environment)
	if !ok {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, pilotcapacity.ErrUnsupportedEnvironment
	}
	osID, err := vultrapi.ParseOSID(request.OSID)
	if err != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	fingerprint, err := json.Marshal(struct {
		ServerID            string
		Environment         string
		Region              string
		Plan                string
		OSID                int
		Hostname            string
		SSHKeyIDs           []string
		StartupScriptID     string `json:",omitempty"`
		StartupScriptSHA256 string `json:",omitempty"`
	}{
		ServerID: request.ServerID, Environment: request.Environment,
		Region: request.Region, Plan: request.Plan, OSID: osID,
		Hostname: request.Hostname, SSHKeyIDs: request.SSHKeyIDs,
		StartupScriptID: request.StartupScriptID, StartupScriptSHA256: request.StartupScriptSHA256,
	})
	if err != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	digest := sha256.Sum256(fingerprint)
	label := "gsc-managed-" + request.OperationID + "-" + hex.EncodeToString(digest[:])
	tags := []string{"gsc-managed", "gsc-operation-" + request.OperationID}
	serverDigest := sha256.Sum256([]byte(request.Environment + "\x00" + request.ServerID))
	tags = append(tags, "gsc-target-"+hex.EncodeToString(serverDigest[:8]))
	create := vultrapi.CreateInstanceRequest{
		Region: request.Region, Plan: request.Plan, OSID: osID, Label: label,
		Hostname: request.Hostname, SSHKeyIDs: append([]string(nil), request.SSHKeyIDs...),
		StartupScriptID: request.StartupScriptID,
		Tags:            tags,
	}
	if (request.StartupScriptID != "" && !sha256Pattern.MatchString(request.StartupScriptSHA256)) ||
		(request.StartupScriptID == "" && request.StartupScriptSHA256 != "") || vultrapi.ValidateCreateInstanceRequest(create) != nil {
		return vultrapi.CreateInstanceRequest{}, store.CapacityReservation{}, ErrInvalidRequest
	}
	reservation := store.CapacityReservation{
		OperationID: request.OperationID, ServerID: request.ServerID,
		EnvironmentID: request.Environment, CapacityGroup: groupID,
		Provider: providerID, ProviderAdapterID: "hosting.vultr", ResourceKey: label,
	}
	return create, reservation, nil
}

func hasBootstrapReference(request CreateRequest) bool {
	return request.StartupScriptID != "" && request.StartupScriptSHA256 != ""
}

func (p *Provisioner) verifyStartupScript(ctx context.Context, token []byte, scriptID, expectedDigest string) error {
	script, err := p.provider.GetStartupScript(ctx, token, scriptID)
	if err != nil || script.ID != scriptID || script.Type != "boot" {
		return ErrBootstrapScript
	}
	source, err := base64.StdEncoding.DecodeString(script.Script)
	if err != nil || len(source) == 0 || len(source) > 1<<20 || !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 {
		return ErrBootstrapScript
	}
	digest := sha256.Sum256(source)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return ErrBootstrapScript
	}
	return nil
}

func (p *Provisioner) reconcile(ctx context.Context, token []byte, reservation store.CapacityReservation) (store.CapacityReservation, error) {
	instances, err := p.provider.ListInstancesByLabel(ctx, token, reservation.ResourceKey)
	if err != nil {
		return reservation, ErrOutcomeUnknown
	}
	if len(instances) == 0 {
		return reservation, ErrOutcomeUnknown
	}
	if len(instances) != 1 {
		return reservation, fmt.Errorf("%w: %w", ErrOutcomeUnknown, ErrMultipleMatches)
	}
	candidate := instances[0]
	if candidate.ID == "" || candidate.Label != reservation.ResourceKey {
		return reservation, ErrOutcomeUnknown
	}
	observed, err := p.provider.GetInstance(ctx, token, candidate.ID)
	if err != nil || observed.ID != candidate.ID || observed.Label != reservation.ResourceKey {
		return reservation, ErrOutcomeUnknown
	}
	return p.recordPresent(reservation, observed)
}

func (p *Provisioner) recordPresent(reservation store.CapacityReservation, instance vultrapi.Instance) (store.CapacityReservation, error) {
	observedAt := p.observationTime(reservation)
	provisionedAt, _ := time.Parse(time.RFC3339, instance.DateCreated)
	if provisionedAt.IsZero() || provisionedAt.Before(reservation.CreatedAt) || provisionedAt.After(observedAt) {
		provisionedAt = time.Time{}
	} else {
		provisionedAt = provisionedAt.UTC()
	}
	updated, err := p.store.ObserveCapacity(reservation.OperationID, store.CapacityProviderObservation{
		Provider: providerID, ResourceKey: reservation.ResourceKey,
		ProviderInstanceID: instance.ID, State: "present", ObservedAt: observedAt, ProvisionedAt: provisionedAt,
	})
	if err != nil {
		return reservation, ErrOutcomeUnknown
	}
	return updated, nil
}

func (p *Provisioner) recordAbsence(reservation store.CapacityReservation) (store.CapacityReservation, error) {
	updated, err := p.store.ObserveCapacity(reservation.OperationID, store.CapacityProviderObservation{
		Provider: providerID, ResourceKey: reservation.ResourceKey,
		State: "absent", ObservedAt: p.observationTime(reservation),
	})
	if err != nil {
		return reservation, ErrOutcomeUnknown
	}
	return updated, nil
}

func (p *Provisioner) observationTime(reservation store.CapacityReservation) time.Time {
	observedAt := p.now().UTC()
	if observedAt.Before(reservation.CreatedAt) {
		return reservation.CreatedAt
	}
	return observedAt
}

func sameReservationIdentity(existing, requested store.CapacityReservation) bool {
	return existing.OperationID == requested.OperationID && existing.ServerID == requested.ServerID &&
		existing.EnvironmentID == requested.EnvironmentID && existing.CapacityGroup == requested.CapacityGroup &&
		existing.Provider == requested.Provider && existing.ResourceKey == requested.ResourceKey
}
