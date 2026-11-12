package pilotvultr

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

// These tests cover Provision's crash- and race-recovery paths. hookStore
// wraps the real store: a hook can change the stored state just before a call
// (as a racing process would), replace the call's result, or fail it.

const recoveryOperationID = "0123456789abcdef0123456789abcdef"

type hookStore struct {
	*store.BoltStore
	mu        sync.Mutex
	gets      int
	onGet     func(call int, real func() (store.CapacityReservation, error)) (store.CapacityReservation, error)
	onRelease func(real func() error) error
	onClaim   func(real func() (store.CapacityReservation, error)) (store.CapacityReservation, error)
	onObserve func(real func() (store.CapacityReservation, error)) (store.CapacityReservation, error)
}

func (h *hookStore) GetCapacityReservation(id string) (store.CapacityReservation, error) {
	h.mu.Lock()
	h.gets++
	call := h.gets
	h.mu.Unlock()
	real := func() (store.CapacityReservation, error) { return h.BoltStore.GetCapacityReservation(id) }
	if h.onGet != nil {
		return h.onGet(call, real)
	}
	return real()
}

func (h *hookStore) ReleaseUnsentCapacityReservation(id, key string) error {
	real := func() error { return h.BoltStore.ReleaseUnsentCapacityReservation(id, key) }
	if h.onRelease != nil {
		return h.onRelease(real)
	}
	return real()
}

func (h *hookStore) ClaimCapacityCreateWithHandoff(id string, handoff hosthandoff.Handoff) (store.CapacityReservation, error) {
	real := func() (store.CapacityReservation, error) {
		return h.BoltStore.ClaimCapacityCreateWithHandoff(id, handoff)
	}
	if h.onClaim != nil {
		return h.onClaim(real)
	}
	return real()
}

func (h *hookStore) ObserveCapacity(id string, observation store.CapacityProviderObservation) (store.CapacityReservation, error) {
	real := func() (store.CapacityReservation, error) { return h.BoltStore.ObserveCapacity(id, observation) }
	if h.onObserve != nil {
		return h.onObserve(real)
	}
	return real()
}

func newHookedProvisioner(t *testing.T, provider *scriptedProvider) (*Provisioner, *hookStore, *store.BoltStore) {
	t.Helper()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	hooked := &hookStore{BoltStore: repository}
	provisioner.store = hooked
	return provisioner, hooked, repository
}

// legacyRequest is the shape an earlier build reserved: no firewall group.
func legacyRequest() CreateRequest {
	legacy := request(recoveryOperationID, "pilot-server", "staging")
	legacy.FirewallGroupID = ""
	return legacy
}

func reserveFor(t *testing.T, p *Provisioner, repository *store.BoltStore, input CreateRequest) {
	t.Helper()
	_, reservation, err := p.prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ReserveCapacity(reservation, 1, 1); err != nil {
		t.Fatal(err)
	}
}

// moveTo advances a reserved operation to state, as another process would.
func moveTo(t *testing.T, repository *store.BoltStore, state store.CapacityState) {
	t.Helper()
	if state == store.CapacityReserved {
		return
	}
	if _, err := repository.MarkCapacityCreateSent(recoveryOperationID); err != nil {
		t.Fatal(err)
	}
	current, err := repository.GetCapacityReservation(recoveryOperationID)
	if err != nil {
		t.Fatal(err)
	}
	observation := store.CapacityProviderObservation{Provider: providerID, ResourceKey: current.ResourceKey, ObservedAt: time.Now().UTC()}
	switch state {
	case store.CapacityCreateOutcomeUnknown:
		return
	case store.CapacityActive:
		observation.State, observation.ProviderInstanceID = "present", "instance-9"
	case store.CapacityAbsent:
		observation.State = "absent"
	default:
		t.Fatalf("moveTo does not support %s", state)
	}
	if _, err := repository.ObserveCapacity(recoveryOperationID, observation); err != nil {
		t.Fatal(err)
	}
}

func storedState(t *testing.T, repository *store.BoltStore) store.CapacityState {
	t.Helper()
	current, err := repository.GetCapacityReservation(recoveryOperationID)
	if errors.Is(err, store.ErrCapacityNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return current.State
}

func requireNoCreate(t *testing.T, provider *scriptedProvider) {
	t.Helper()
	if created, _, _ := provider.counts(); created != 0 {
		t.Fatalf("sent %d creates", created)
	}
}

func TestProvisionRefusesACanceledContext(t *testing.T) {
	provider := successfulProvider()
	provisioner, _, repository := newHookedProvisioner(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provisioner.Provision(ctx, request(recoveryOperationID, "pilot-server", "staging")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("canceled context = %v", err)
	}
	if storedState(t, repository) != "" {
		t.Fatal("a canceled call reserved capacity")
	}
	requireNoCreate(t, provider)
}

func TestProvisionReturnsStoreReadErrors(t *testing.T) {
	provider := successfulProvider()
	provisioner, hooked, _ := newHookedProvisioner(t, provider)
	disk := errors.New("disk read failed")
	hooked.onGet = func(int, func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
		return store.CapacityReservation{}, disk
	}
	if _, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging")); !errors.Is(err, disk) {
		t.Fatalf("store read failure = %v", err)
	}
	requireNoCreate(t, provider)
}

func TestProvisionRefusesReleasedOperations(t *testing.T) {
	t.Run("same request", func(t *testing.T) {
		provider := successfulProvider()
		provisioner, _, repository := newHookedProvisioner(t, provider)
		input := request(recoveryOperationID, "pilot-server", "staging")
		reserveFor(t, provisioner, repository, input)
		moveTo(t, repository, store.CapacityAbsent)
		result, err := provisioner.Provision(context.Background(), input)
		if !errors.Is(err, ErrOperationClosed) || result.State != store.CapacityAbsent {
			t.Fatalf("released operation = %s, %v", result.State, err)
		}
		requireNoCreate(t, provider)
	})
	t.Run("legacy request", func(t *testing.T) {
		provider := successfulProvider()
		provisioner, _, repository := newHookedProvisioner(t, provider)
		reserveFor(t, provisioner, repository, legacyRequest())
		moveTo(t, repository, store.CapacityAbsent)
		result, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging"))
		if !errors.Is(err, store.ErrCapacityConflict) || result.State != store.CapacityAbsent {
			t.Fatalf("released legacy operation = %s, %v", result.State, err)
		}
		requireNoCreate(t, provider)
	})
}

func TestProvisionRefusesALegacyReservationWithoutAccessReferences(t *testing.T) {
	provider := successfulProvider()
	provisioner, _, repository := newHookedProvisioner(t, provider)
	reserveFor(t, provisioner, repository, legacyRequest())
	current := request(recoveryOperationID, "pilot-server", "staging")
	current.ControllerIP = netip.Addr{}
	result, err := provisioner.Provision(context.Background(), current)
	if !errors.Is(err, ErrInvalidRequest) || result.State != store.CapacityReserved || storedState(t, repository) != store.CapacityReserved {
		t.Fatalf("legacy reservation without access references = %s, %v", result.State, err)
	}
	requireNoCreate(t, provider)
}

// TestProvisionRechecksALegacyReservationThatRacedItsRelease: releasing a
// legacy reservation fails because another process moved it on first.
func TestProvisionRechecksALegacyReservationThatRacedItsRelease(t *testing.T) {
	for _, test := range []struct {
		raced     store.CapacityState
		wantState store.CapacityState
		wantErr   error
	}{
		{store.CapacityCreateOutcomeUnknown, store.CapacityCreateOutcomeUnknown, ErrOutcomeUnknown},
		{store.CapacityActive, store.CapacityActive, nil},
		{store.CapacityAbsent, store.CapacityAbsent, store.ErrCapacityTransition},
	} {
		t.Run(string(test.raced), func(t *testing.T) {
			provider := successfulProvider()
			provisioner, hooked, repository := newHookedProvisioner(t, provider)
			reserveFor(t, provisioner, repository, legacyRequest())
			hooked.onRelease = func(real func() error) error {
				moveTo(t, repository, test.raced)
				return real()
			}
			result, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging"))
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) || result.State != test.wantState {
				t.Fatalf("raced to %s: result %s, %v", test.raced, result.State, err)
			}
			requireNoCreate(t, provider)
		})
	}
}

// TestProvisionRechecksAfterVerificationFailsAndReleaseRaces: pre-dispatch
// verification fails for a resumed reservation, and releasing it fails
// because another process moved it on first. When the new state cannot be
// read or is not one it can finish, the result is an empty reservation and
// ErrOutcomeUnknown.
func TestProvisionRechecksAfterVerificationFailsAndReleaseRaces(t *testing.T) {
	for _, test := range []struct {
		name      string
		raced     store.CapacityState
		readFails bool
		wantState store.CapacityState
		wantErr   error
	}{
		{"read fails", store.CapacityCreateOutcomeUnknown, true, "", ErrOutcomeUnknown},
		{"now unknown", store.CapacityCreateOutcomeUnknown, false, store.CapacityCreateOutcomeUnknown, ErrOutcomeUnknown},
		{"now active", store.CapacityActive, false, store.CapacityActive, nil},
		{"now absent", store.CapacityAbsent, false, "", ErrOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := successfulProvider()
			provider.script = func(context.Context, []byte, string) (vultrapi.StartupScript, error) {
				return vultrapi.StartupScript{}, errors.New("script unavailable")
			}
			provisioner, hooked, repository := newHookedProvisioner(t, provider)
			input := request(recoveryOperationID, "pilot-server", "staging")
			reserveFor(t, provisioner, repository, input)
			hooked.onRelease = func(real func() error) error {
				moveTo(t, repository, test.raced)
				return real()
			}
			if test.readFails {
				hooked.onGet = func(call int, real func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
					if call > 1 {
						return store.CapacityReservation{}, errors.New("disk read failed")
					}
					return real()
				}
			}
			result, err := provisioner.Provision(context.Background(), input)
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) || result.State != test.wantState {
				t.Fatalf("%s: result %s, %v", test.name, result.State, err)
			}
			requireNoCreate(t, provider)
		})
	}
}

func TestProvisionHandlesEachReservationState(t *testing.T) {
	refused := errors.New("capacity refused")
	for _, test := range []struct {
		name     string
		reserved store.CapacityReservation
		err      error
		wantErr  error
	}{
		{"already active", store.CapacityReservation{OperationID: recoveryOperationID, State: store.CapacityActive}, nil, nil},
		{"absent", store.CapacityReservation{OperationID: recoveryOperationID, State: store.CapacityAbsent}, nil, ErrOperationClosed},
		{"destroyed", store.CapacityReservation{OperationID: recoveryOperationID, State: store.CapacityDestroyed}, nil, ErrOperationClosed},
		{"create unknown", store.CapacityReservation{OperationID: recoveryOperationID, State: store.CapacityCreateOutcomeUnknown, ResourceKey: "gsc-managed-x"}, nil, ErrOutcomeUnknown},
		{"destroy unknown", store.CapacityReservation{OperationID: recoveryOperationID, State: store.CapacityDestroyOutcomeUnknown}, nil, store.ErrCapacityTransition},
		{"refused", store.CapacityReservation{}, refused, refused},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := successfulProvider()
			provisioner, _, _ := newHookedProvisioner(t, provider)
			provisioner.reserve = func(store.CapacityReservation) (store.CapacityReservation, bool, error) {
				return test.reserved, false, test.err
			}
			_, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging"))
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("%s: %v", test.name, err)
			}
			requireNoCreate(t, provider)
		})
	}
}

type cancelingCredentials struct {
	*testCredentials
	cancel context.CancelFunc
}

func (c cancelingCredentials) WithCredential(reference string, use func([]byte) error) error {
	c.cancel()
	return c.testCredentials.WithCredential(reference, use)
}

func TestProvisionStopsWhenCanceledMidway(t *testing.T) {
	t.Run("before reserving", func(t *testing.T) {
		provider := successfulProvider()
		provisioner, _, repository := newHookedProvisioner(t, provider)
		ctx, cancel := context.WithCancel(context.Background())
		provisioner.credentials = cancelingCredentials{testCredentials: &testCredentials{reference: provisioner.credential, token: []byte("fake-provider-token")}, cancel: cancel}
		if _, err := provisioner.Provision(ctx, request(recoveryOperationID, "pilot-server", "staging")); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled before reserving = %v", err)
		}
		if storedState(t, repository) != "" {
			t.Fatal("capacity was reserved after cancellation")
		}
		requireNoCreate(t, provider)
	})
	t.Run("after reserving", func(t *testing.T) {
		provider := successfulProvider()
		provisioner, _, repository := newHookedProvisioner(t, provider)
		ctx, cancel := context.WithCancel(context.Background())
		reserve := provisioner.reserve
		provisioner.reserve = func(r store.CapacityReservation) (store.CapacityReservation, bool, error) {
			result, replayed, err := reserve(r)
			cancel()
			return result, replayed, err
		}
		if _, err := provisioner.Provision(ctx, request(recoveryOperationID, "pilot-server", "staging")); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled after reserving = %v", err)
		}
		if storedState(t, repository) != store.CapacityReserved {
			t.Fatal("cancellation after reserving must leave the unsent reservation for the next attempt")
		}
		requireNoCreate(t, provider)
	})
}

// TestProvisionRechecksWhenItsClaimRaces: the one-shot dispatch claim fails
// because another process moved the reservation on first.
func TestProvisionRechecksWhenItsClaimRaces(t *testing.T) {
	readFailure := errors.New("disk read failed")
	claimFailure := errors.New("claim write failed")
	for _, test := range []struct {
		name    string
		raced   store.CapacityState
		onGet   func(int, func() (store.CapacityReservation, error)) (store.CapacityReservation, error)
		claim   error
		wantErr error
	}{
		{name: "now active", raced: store.CapacityActive},
		{name: "now unknown", raced: store.CapacityCreateOutcomeUnknown, wantErr: ErrOutcomeUnknown},
		{name: "now absent", raced: store.CapacityAbsent, wantErr: ErrOperationClosed},
		{name: "read fails", raced: store.CapacityCreateOutcomeUnknown, wantErr: readFailure,
			onGet: func(call int, real func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
				if call > 1 {
					return store.CapacityReservation{}, readFailure
				}
				return real()
			}},
		{name: "unexpected state", raced: store.CapacityCreateOutcomeUnknown, wantErr: store.ErrCapacityTransition,
			onGet: func(call int, real func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
				current, err := real()
				if call > 1 {
					current.State = store.CapacityDestroyOutcomeUnknown
				}
				return current, err
			}},
		{name: "claim write fails", raced: store.CapacityReserved, claim: claimFailure, wantErr: claimFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := successfulProvider()
			provisioner, hooked, repository := newHookedProvisioner(t, provider)
			hooked.onGet = test.onGet
			hooked.onClaim = func(real func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
				if test.claim != nil {
					return store.CapacityReservation{}, test.claim
				}
				moveTo(t, repository, test.raced)
				return real()
			}
			_, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging"))
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("%s: %v", test.name, err)
			}
			requireNoCreate(t, provider)
		})
	}
}

func TestProvisionLeavesARejectionUnknownWhenItCannotRecordIt(t *testing.T) {
	provider := successfulProvider()
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrProviderReject
	}
	provisioner, hooked, repository := newHookedProvisioner(t, provider)
	hooked.onObserve = func(func() (store.CapacityReservation, error)) (store.CapacityReservation, error) {
		return store.CapacityReservation{}, errors.New("disk write failed")
	}
	if _, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging")); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("unrecorded rejection = %v", err)
	}
	if storedState(t, repository) != store.CapacityCreateOutcomeUnknown {
		t.Fatal("an unrecorded rejection must stay unknown, not release capacity")
	}
}

// TestProvisionReconcilesUnexpectedCreateResponses: a create reply or
// inspection that does not name the requested instance is reconciled by label,
// never trusted and never resent.
func TestProvisionReconcilesUnexpectedCreateResponses(t *testing.T) {
	for _, test := range []struct {
		name        string
		create      func(label string) (vultrapi.Instance, error)
		firstGetBad bool
		listed      bool
		wantErr     error
	}{
		{name: "empty instance ID, found by label", create: func(label string) (vultrapi.Instance, error) {
			return vultrapi.Instance{Label: label}, nil
		}, listed: true},
		{name: "wrong label, not found", create: func(string) (vultrapi.Instance, error) {
			return vultrapi.Instance{ID: "instance-1", Label: "other"}, nil
		}, wantErr: ErrOutcomeUnknown},
		{name: "transport error, found by label", create: func(string) (vultrapi.Instance, error) {
			return vultrapi.Instance{}, errors.New("connection reset")
		}, listed: true},
		{name: "transport error, not found", create: func(string) (vultrapi.Instance, error) {
			return vultrapi.Instance{}, errors.New("connection reset")
		}, wantErr: ErrOutcomeUnknown},
		{name: "inspection mismatch, found by label", create: func(label string) (vultrapi.Instance, error) {
			return vultrapi.Instance{ID: "instance-1", Label: label}, nil
		}, firstGetBad: true, listed: true},
		{name: "inspection mismatch, not found", create: func(label string) (vultrapi.Instance, error) {
			return vultrapi.Instance{ID: "instance-1", Label: label}, nil
		}, firstGetBad: true, wantErr: ErrOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := successfulProvider()
			var label string
			gets := 0
			provider.create = func(_ context.Context, _ []byte, r vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
				label = r.Label
				return test.create(r.Label)
			}
			provider.get = func(_ context.Context, _ []byte, id string) (vultrapi.Instance, error) {
				gets++
				if test.firstGetBad && gets == 1 {
					return vultrapi.Instance{ID: id, Label: "other"}, nil
				}
				return vultrapi.Instance{ID: id, Label: label, DateCreated: time.Now().UTC().Format(time.RFC3339Nano)}, nil
			}
			provider.list = func(context.Context, []byte, string) ([]vultrapi.Instance, error) {
				if !test.listed {
					return nil, nil
				}
				return []vultrapi.Instance{{ID: "instance-1", Label: label}}, nil
			}
			provisioner, _, repository := newHookedProvisioner(t, provider)
			result, err := provisioner.Provision(context.Background(), request(recoveryOperationID, "pilot-server", "staging"))
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("%s: %v", test.name, err)
			}
			want := store.CapacityActive
			if test.wantErr != nil {
				want = store.CapacityCreateOutcomeUnknown
			}
			if result.State != want || storedState(t, repository) != want {
				t.Fatalf("%s: state %s, stored %s", test.name, result.State, storedState(t, repository))
			}
			if created, _, _ := provider.counts(); created != 1 {
				t.Fatalf("%s: %d creates, want exactly one", test.name, created)
			}
		})
	}
}
