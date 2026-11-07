package pilotvultr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

type testCredentials struct {
	mu        sync.Mutex
	reference string
	token     []byte
	err       error
	calls     int
}

func (c *testCredentials) WithCredential(reference string, use func([]byte) error) error {
	c.mu.Lock()
	c.calls++
	callErr := c.err
	expectedRef := c.reference
	borrowed := append([]byte(nil), c.token...)
	c.mu.Unlock()
	if callErr != nil {
		return callErr
	}
	if reference != expectedRef {
		return errors.New("unexpected credential reference")
	}
	defer func() {
		for i := range borrowed {
			borrowed[i] = 0
		}
	}()
	return use(borrowed)
}

func (c *testCredentials) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type scriptedProvider struct {
	mu          sync.Mutex
	createCalls int
	getCalls    int
	listCalls   int
	create      func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error)
	get         func(context.Context, []byte, string) (vultrapi.Instance, error)
	list        func(context.Context, []byte, string) ([]vultrapi.Instance, error)
}

func (p *scriptedProvider) CreateInstance(ctx context.Context, token []byte, request vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
	p.mu.Lock()
	p.createCalls++
	create := p.create
	p.mu.Unlock()
	return create(ctx, token, request)
}

func (p *scriptedProvider) GetInstance(ctx context.Context, token []byte, id string) (vultrapi.Instance, error) {
	p.mu.Lock()
	p.getCalls++
	get := p.get
	p.mu.Unlock()
	return get(ctx, token, id)
}

func (p *scriptedProvider) ListInstancesByLabel(ctx context.Context, token []byte, label string) ([]vultrapi.Instance, error) {
	p.mu.Lock()
	p.listCalls++
	list := p.list
	p.mu.Unlock()
	return list(ctx, token, label)
}

func (p *scriptedProvider) counts() (create, get, list int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createCalls, p.getCalls, p.listCalls
}

func testPolicy() pilotcapacity.Policy {
	return pilotcapacity.Policy{
		PolicyID: "supervised-pilot-vm-capacity", PolicyVersion: 2, RuntimeEnforced: true, MaxInstancesTotal: 1,
		CapacityGroups: map[string]pilotcapacity.Group{
			"production":           {MaxInstances: 1, AcceptedEnvironments: []string{"production"}},
			"shared_nonproduction": {MaxInstances: 1, AcceptedEnvironments: []string{"staging", "testing"}},
		},
		ReleasedStates: map[string]bool{"absent": true, "destroyed": true},
	}
}

func newTestProvisioner(t *testing.T, provider *scriptedProvider) (*Provisioner, *store.BoltStore, *testCredentials) {
	t.Helper()
	repository, err := store.OpenBolt(t.TempDir() + "/pilot.db")
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	credentials := &testCredentials{reference: "systemd-credential://vultr-token", token: []byte("fake-provider-token")}
	provisioner, err := NewProvisioner(repository, testPolicy(), provider, credentials, credentials.reference)
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}
	return provisioner, repository, credentials
}

func successfulProvider() *scriptedProvider {
	provider := &scriptedProvider{}
	label := ""
	provider.create = func(_ context.Context, token []byte, request vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		if string(token) != "fake-provider-token" {
			return vultrapi.Instance{}, errors.New("provider did not receive the test token")
		}
		label = request.Label
		return vultrapi.Instance{ID: "instance-1", Label: request.Label, Status: "pending"}, nil
	}
	provider.get = func(_ context.Context, _ []byte, id string) (vultrapi.Instance, error) {
		return vultrapi.Instance{ID: id, Label: label, Status: "pending", DateCreated: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}
	provider.list = func(context.Context, []byte, string) ([]vultrapi.Instance, error) { return nil, nil }
	return provider
}

func request(operationID, serverID, environment string) CreateRequest {
	return CreateRequest{
		OperationID: operationID, ServerID: serverID, Environment: environment,
		Region: "ewr", Plan: "vc2-1c-2gb", OSID: "362", Hostname: "pilot.example.invalid",
		SSHKeyIDs: []string{"sshkey-123"},
	}
}

func TestProvisionReservesBeforeCreateAndIsIdempotent(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, credentials := newTestProvisioner(t, provider)
	input := request("0123456789abcdef0123456789abcdef", "pilot-server", "staging")

	reservation, err := provisioner.Provision(context.Background(), input)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID != "instance-1" {
		t.Fatalf("unexpected reservation: %+v", reservation)
	}
	if reservation.ProvisionedAt.IsZero() || reservation.ProvisionedAt.Before(reservation.CreatedAt) || reservation.ProvisionedAt.After(reservation.UpdatedAt) {
		t.Fatalf("provider creation time was not persisted: %+v", reservation)
	}
	if !strings.HasPrefix(reservation.ResourceKey, "gsc-managed-"+input.OperationID+"-") {
		t.Fatalf("unexpected reconciliation label: %q", reservation.ResourceKey)
	}
	created, fetched, listed := provider.counts()
	if created != 1 || fetched != 1 || listed != 0 {
		t.Fatalf("provider calls create/get/list = %d/%d/%d", created, fetched, listed)
	}
	persisted, err := repository.GetCapacityReservation(input.OperationID)
	if err != nil || persisted.State != store.CapacityActive {
		t.Fatalf("persisted reservation = %+v, %v", persisted, err)
	}

	replayed, err := provisioner.Provision(context.Background(), input)
	if err != nil || replayed.State != store.CapacityActive {
		t.Fatalf("idempotent replay = %+v, %v", replayed, err)
	}
	created, _, _ = provider.counts()
	if created != 1 {
		t.Fatalf("idempotent replay sent %d create requests, want 1", created)
	}
	if calls := credentials.callCount(); calls != 1 {
		t.Fatalf("active replay resolved credentials %d times; want only the initial call", calls)
	}
	changed := input
	changed.Plan = "vc2-2c-4gb"
	if _, err := provisioner.Provision(context.Background(), changed); !errors.Is(err, store.ErrCapacityConflict) {
		t.Fatalf("same operation with changed plan returned %v, want ErrCapacityConflict", err)
	}
}

func TestProvisionEnforcesTotalCapacityBeforeProviderCall(t *testing.T) {
	provider := successfulProvider()
	provisioner, _, _ := newTestProvisioner(t, provider)
	if _, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-one", "staging")); err != nil {
		t.Fatal(err)
	}
	_, err := provisioner.Provision(context.Background(), request("abcdef0123456789abcdef0123456789", "pilot-two", "production"))
	if !errors.Is(err, store.ErrCapacityReached) {
		t.Fatalf("second managed VM error = %v, want ErrCapacityReached", err)
	}
	created, _, _ := provider.counts()
	if created != 1 {
		t.Fatalf("provider create calls = %d, want 1", created)
	}
}

func TestProvisionLeavesUnknownWhenProviderCreationTimestampIsInvalid(t *testing.T) {
	provider := successfulProvider()
	get := provider.get
	provider.get = func(ctx context.Context, token []byte, id string) (vultrapi.Instance, error) {
		instance, err := get(ctx, token, id)
		instance.DateCreated = "invalid-provider-timestamp"
		return instance, err
	}
	provisioner, repository, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), request("abcdef0123456789abcdef0123456789", "pilot-timestamp", "staging"))
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if reservation.State != store.CapacityActive || !reservation.ProvisionedAt.IsZero() || reservation.LastObservation == nil ||
		!reservation.LastObservation.ProvisionedAt.IsZero() {
		t.Fatalf("invalid provider timestamp was not preserved as unknown: %+v", reservation)
	}
	loaded, err := repository.GetCapacityReservation(reservation.OperationID)
	if err != nil || loaded.State != store.CapacityActive || !loaded.ProvisionedAt.IsZero() {
		t.Fatalf("reservation with unknown provider time cannot be reloaded: %+v, %v", loaded, err)
	}
}

func TestUnknownCreateIsReconciledWithoutRetry(t *testing.T) {
	provider := successfulProvider()
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrOutcomeUnknown
	}
	provider.list = func(context.Context, []byte, string) ([]vultrapi.Instance, error) { return nil, nil }
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := request("0123456789abcdef0123456789abcdef", "pilot-server", "staging")

	reservation, err := provisioner.Provision(context.Background(), input)
	if !errors.Is(err, ErrOutcomeUnknown) || reservation.State != store.CapacityCreateOutcomeUnknown {
		t.Fatalf("first provision = %+v, %v; want unresolved create", reservation, err)
	}
	reservation, err = provisioner.Provision(context.Background(), input)
	if !errors.Is(err, ErrOutcomeUnknown) || reservation.State != store.CapacityCreateOutcomeUnknown {
		t.Fatalf("reconciliation = %+v, %v; want unresolved create", reservation, err)
	}
	created, _, listed := provider.counts()
	if created != 1 || listed != 2 {
		t.Fatalf("provider create/list calls = %d/%d; want one create and two read-only reconciliations", created, listed)
	}
	if _, _, err := testPolicy().ReserveCapacity(repository, requestReservation("other-operation", "other-server", "testing")); !errors.Is(err, store.ErrCapacityReached) {
		t.Fatalf("unknown create did not consume capacity: %v", err)
	}
}

func TestConcurrentProvisionCallsSendAtMostOneCreate(t *testing.T) {
	provider := successfulProvider()
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrOutcomeUnknown
	}
	provider.list = func(context.Context, []byte, string) ([]vultrapi.Instance, error) { return nil, nil }
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := request("0123456789abcdef0123456789abcdef", "pilot-server", "staging")
	const callers = 2
	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := provisioner.Provision(context.Background(), input)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("concurrent provision returned %v, want unresolved outcome", err)
		}
	}
	created, _, _ := provider.counts()
	if created != 1 {
		t.Fatalf("concurrent calls sent %d provider creates, want exactly 1", created)
	}
	reservation, err := repository.GetCapacityReservation(input.OperationID)
	if err != nil || reservation.State != store.CapacityCreateOutcomeUnknown {
		t.Fatalf("concurrent reservation = %+v, %v", reservation, err)
	}
}

func TestUnknownCreateCanReconcileExactInstance(t *testing.T) {
	provider := successfulProvider()
	expectedLabel := ""
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrOutcomeUnknown
	}
	provider.list = func(_ context.Context, _ []byte, requestedLabel string) ([]vultrapi.Instance, error) {
		expectedLabel = requestedLabel
		return []vultrapi.Instance{{ID: "instance-1", Label: requestedLabel}}, nil
	}
	provider.get = func(_ context.Context, _ []byte, id string) (vultrapi.Instance, error) {
		return vultrapi.Instance{ID: id, Label: expectedLabel, Status: "pending"}, nil
	}
	provisioner, _, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-server", "staging"))
	if err != nil || reservation.State != store.CapacityActive || reservation.ProviderInstanceID != "instance-1" {
		t.Fatalf("reconciled provision = %+v, %v", reservation, err)
	}
	created, _, listed := provider.counts()
	if created != 1 || listed != 1 {
		t.Fatalf("provider create/list calls = %d/%d", created, listed)
	}
}

func TestMultipleLabelMatchesRemainUnknown(t *testing.T) {
	provider := successfulProvider()
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrOutcomeUnknown
	}
	provider.list = func(_ context.Context, _ []byte, label string) ([]vultrapi.Instance, error) {
		return []vultrapi.Instance{{ID: "instance-1", Label: label}, {ID: "instance-2", Label: label}}, nil
	}
	provisioner, _, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-server", "staging"))
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrMultipleMatches) || reservation.State != store.CapacityCreateOutcomeUnknown {
		t.Fatalf("multiple-match result = %+v, %v", reservation, err)
	}
	created, fetched, _ := provider.counts()
	if created != 1 || fetched != 0 {
		t.Fatalf("unsafe calls after duplicate match: create=%d get=%d", created, fetched)
	}
}

func TestProviderRejectionReleasesOnlyItsUncreatedReservation(t *testing.T) {
	provider := successfulProvider()
	label := ""
	provider.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrProviderReject
	}
	provisioner, _, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-server", "staging"))
	if !errors.Is(err, ErrProviderRejected) || reservation.State != store.CapacityAbsent {
		t.Fatalf("provider rejection = %+v, %v", reservation, err)
	}
	provider.create = func(_ context.Context, _ []byte, input vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		label = input.Label
		return vultrapi.Instance{ID: "instance-2", Label: input.Label}, nil
	}
	provider.get = func(_ context.Context, _ []byte, id string) (vultrapi.Instance, error) {
		return vultrapi.Instance{ID: id, Label: label}, nil
	}
	created, err := provisioner.Provision(context.Background(), request("abcdef0123456789abcdef0123456789", "pilot-server-two", "testing"))
	if err != nil || created.State != store.CapacityActive {
		t.Fatalf("provision after definitive rejection = %+v, %v", created, err)
	}
}

func TestInvalidAndUnsupportedRequestsDoNotCallProvider(t *testing.T) {
	provider := successfulProvider()
	provisioner, err := NewProvisioner(nil, testPolicy(), provider, &testCredentials{}, "systemd-credential://vultr-token")
	if err == nil {
		t.Fatal("NewProvisioner accepted a missing store")
	}
	provisioner, _, _ = newTestProvisioner(t, provider)
	if _, err := provisioner.Provision(context.Background(), request("bad-operation-id", "pilot-server", "staging")); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid operation ID error = %v", err)
	}
	if _, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-server", "development")); !errors.Is(err, pilotcapacity.ErrUnsupportedEnvironment) {
		t.Fatalf("unsupported environment error = %v", err)
	}
	created, fetched, listed := provider.counts()
	if created != 0 || fetched != 0 || listed != 0 {
		t.Fatalf("invalid requests reached provider: create/get/list=%d/%d/%d", created, fetched, listed)
	}
}

func TestProvisionerRejectsDeclarativePolicy(t *testing.T) {
	repository, err := store.OpenBolt(t.TempDir() + "/pilot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	policy := testPolicy()
	policy.RuntimeEnforced = false
	credentials := &testCredentials{reference: "systemd-credential://vultr-token", token: []byte("fake-provider-token")}
	if _, err := NewProvisioner(repository, policy, successfulProvider(), credentials, credentials.reference); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("declarative policy constructor error = %v, want ErrInvalidRequest", err)
	}
}

func TestUnavailableCredentialLeavesNoCapacityReservation(t *testing.T) {
	provider := successfulProvider()
	repository, err := store.OpenBolt(t.TempDir() + "/pilot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	credentials := &testCredentials{reference: "systemd-credential://vultr-token", err: errors.New("credential unavailable")}
	provisioner, err := NewProvisioner(repository, testPolicy(), provider, credentials, credentials.reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Provision(context.Background(), request("0123456789abcdef0123456789abcdef", "pilot-server", "staging")); err == nil {
		t.Fatal("missing credential unexpectedly allowed provisioning")
	}
	reservations, err := repository.ListCapacityReservations()
	if err != nil || len(reservations) != 0 {
		t.Fatalf("capacity reservations after unavailable credential = %v, %v", reservations, err)
	}
	created, fetched, listed := provider.counts()
	if created != 0 || fetched != 0 || listed != 0 {
		t.Fatalf("provider calls with unavailable credential: create/get/list=%d/%d/%d", created, fetched, listed)
	}
}

func requestReservation(operationID, serverID, environment string) store.CapacityReservation {
	group := "shared_nonproduction"
	if environment == "production" {
		group = "production"
	}
	return store.CapacityReservation{
		OperationID: operationID, ServerID: serverID, EnvironmentID: environment,
		CapacityGroup: group, Provider: providerID, ResourceKey: "gsc-managed-" + operationID,
	}
}

func TestObservationTimeIsNotBeforeReservation(t *testing.T) {
	reservation := store.CapacityReservation{CreatedAt: time.Now().UTC()}
	provisioner := &Provisioner{now: func() time.Time { return reservation.CreatedAt.Add(-time.Second) }}
	if !provisioner.observationTime(reservation).Equal(reservation.CreatedAt) {
		t.Fatal(fmt.Sprintf("observation time was before reservation: %v", provisioner.observationTime(reservation)))
	}
}
