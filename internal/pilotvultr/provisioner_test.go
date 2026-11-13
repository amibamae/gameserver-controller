package pilotvultr

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
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
	scriptCalls int
	create      func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error)
	get         func(context.Context, []byte, string) (vultrapi.Instance, error)
	list        func(context.Context, []byte, string) ([]vultrapi.Instance, error)
	script      func(context.Context, []byte, string) (vultrapi.StartupScript, error)
	sshKeys     map[string]string
	firewall    []vultrapi.FirewallRule
	created     []vultrapi.CreateInstanceRequest
}

func (p *scriptedProvider) GetSSHKey(_ context.Context, _ []byte, id string) (vultrapi.SSHKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keys := p.sshKeys
	if keys == nil {
		keys = map[string]string{"sshkey-123": testControllerKey}
	}
	key, ok := keys[id]
	if !ok {
		return vultrapi.SSHKey{}, vultrapi.ErrProviderReject
	}
	return vultrapi.SSHKey{ID: id, Name: id, PublicKey: key}, nil
}

func (p *scriptedProvider) ListFirewallRules(context.Context, []byte, string) ([]vultrapi.FirewallRule, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.firewall == nil {
		return []vultrapi.FirewallRule{{ID: 1, Action: "accept", Protocol: "tcp", Port: "22", Subnet: "203.0.113.10", SubnetSize: 32}}, nil
	}
	return p.firewall, nil
}

func (p *scriptedProvider) CreateInstance(ctx context.Context, token []byte, request vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
	p.mu.Lock()
	p.createCalls++
	p.created = append(p.created, request)
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

func (p *scriptedProvider) GetStartupScript(ctx context.Context, token []byte, id string) (vultrapi.StartupScript, error) {
	p.mu.Lock()
	p.scriptCalls++
	get := p.script
	p.mu.Unlock()
	return get(ctx, token, id)
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
	repository := openRepository(t)
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
	provider.script = func(_ context.Context, _ []byte, _ string) (vultrapi.StartupScript, error) {
		return vultrapi.StartupScript{ID: "script-123", Type: "boot", Script: base64.StdEncoding.EncodeToString([]byte("bootstrap"))}, nil
	}
	return provider
}

// defaultOperationID is the operation most tests provision.
const defaultOperationID = "0123456789abcdef0123456789abcdef"

// defaultRequest is a valid staging create for defaultOperationID.
func defaultRequest() CreateRequest {
	return request(defaultOperationID, "pilot-server", "staging")
}

// olderBuildRequest is defaultRequest as an older build recorded it.
func olderBuildRequest(withoutBootstrap bool) CreateRequest {
	legacy := defaultRequest()
	legacy.FirewallGroupID = ""
	if withoutBootstrap {
		legacy.StartupScriptID, legacy.StartupScriptSHA256 = "", ""
	}
	return legacy
}

// createOutcomeUnknown makes every create report a lost response.
func (p *scriptedProvider) createOutcomeUnknown() {
	p.create = func(context.Context, []byte, vultrapi.CreateInstanceRequest) (vultrapi.Instance, error) {
		return vultrapi.Instance{}, vultrapi.ErrOutcomeUnknown
	}
}

// seedReservation stores the reservation the provisioner would make for
// input, marked as create-sent when sent is true, as a previous run left it.
func seedReservation(t *testing.T, provisioner *Provisioner, repository *store.BoltStore, input CreateRequest, sent bool) store.CapacityReservation {
	t.Helper()
	_, reservation, err := provisioner.prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.ReserveCapacity(reservation, 1, 1); err != nil {
		t.Fatal(err)
	}
	if sent {
		if _, err := repository.MarkCapacityCreateSent(reservation.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	return reservation
}

// expectUntouched fails if the provider was asked to create, get or list an
// instance, or if any capacity was reserved; why names the scenario.
func expectUntouched(t *testing.T, provider *scriptedProvider, repository *store.BoltStore, why string) {
	t.Helper()
	if created, fetched, listed := provider.counts(); created != 0 || fetched != 0 || listed != 0 {
		t.Fatalf("%s reached the provider: create/get/list=%d/%d/%d", why, created, fetched, listed)
	}
	if repository == nil {
		return
	}
	if reservations, err := repository.ListCapacityReservations(); err != nil || len(reservations) != 0 {
		t.Fatalf("%s reserved capacity: reservations=%+v err=%v", why, reservations, err)
	}
}

// openRepository opens a fresh pilot store that closes with the test.
func openRepository(t *testing.T) *store.BoltStore {
	t.Helper()
	repository, err := store.OpenBolt(t.TempDir() + "/pilot.db")
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	return repository
}

// testControllerKey is the RFC 8032 test 1 public key.
const testControllerKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINdamAGCsQq31Uv+08lkBzoO4XLz2qYjJa8CGmj3B1Ea"

func request(operationID, serverID, environment string) CreateRequest {
	scriptDigest := sha256.Sum256([]byte("bootstrap"))
	return CreateRequest{
		OperationID: operationID, ServerID: serverID, Environment: environment,
		Region: "ewr", Plan: "vc2-1c-2gb", OSID: "362", Hostname: "pilot.example.invalid",
		SSHKeyIDs:       []string{"sshkey-123"},
		StartupScriptID: "script-123", StartupScriptSHA256: hex.EncodeToString(scriptDigest[:]),
		FirewallGroupID: "firewall-123", ControllerIP: netip.MustParseAddr("203.0.113.10"),
		ControllerSSHPublicKey: testControllerKey + " gameserver-controller",
	}
}

func TestProvisionReservesBeforeCreateAndIsIdempotent(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, credentials := newTestProvisioner(t, provider)
	input := defaultRequest()

	reservation, err := provisioner.Provision(context.Background(), input)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID != "instance-1" {
		t.Fatalf("unexpected reservation: %+v", reservation)
	}
	if provider.scriptCalls != 1 {
		t.Fatalf("verified startup script %d times; want once before the create", provider.scriptCalls)
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
	changed = input
	changed.StartupScriptID = "script-other"
	if _, err := provisioner.Provision(context.Background(), changed); !errors.Is(err, store.ErrCapacityConflict) {
		t.Fatalf("same operation with changed startup-script ID returned %v, want ErrCapacityConflict", err)
	}
}

func TestProvisionRejectsUnverifiedStartupScriptBeforeCapacityOrCreate(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
	input.StartupScriptSHA256 = strings.Repeat("0", 64)
	if _, err := provisioner.Provision(context.Background(), input); !errors.Is(err, ErrBootstrapScript) {
		t.Fatalf("mismatched startup script digest returned %v", err)
	}
	expectUntouched(t, provider, repository, "unverified bootstrap")
}

func TestProvisionRejectsWrongStartupScriptIDBeforeCapacityOrCreate(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
	input.StartupScriptID = "script-other"
	if _, err := provisioner.Provision(context.Background(), input); !errors.Is(err, ErrBootstrapScript) {
		t.Fatalf("wrong startup script ID returned %v", err)
	}
	expectUntouched(t, provider, repository, "wrong startup script")
}

func TestProvisionReconcilesLegacyUnknownCreateWithoutRecreatingIt(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	seedReservation(t, provisioner, repository, olderBuildRequest(true), true)
	result, err := provisioner.Provision(context.Background(), defaultRequest())
	if !errors.Is(err, ErrOutcomeUnknown) || result.State != store.CapacityCreateOutcomeUnknown {
		t.Fatalf("legacy create reconciliation = %+v, %v", result, err)
	}
	created, _, listed := provider.counts()
	if created != 0 || listed != 1 || provider.scriptCalls != 0 {
		t.Fatalf("legacy unknown create was not reconciled safely: create=%d list=%d script-check=%d", created, listed, provider.scriptCalls)
	}
}

func TestProvisionReleasesLegacyReservedOperationBeforeBootstrappedCreate(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	reservation := seedReservation(t, provisioner, repository, olderBuildRequest(true), false)
	result, err := provisioner.Provision(context.Background(), defaultRequest())
	if err != nil || result.State != store.CapacityActive {
		t.Fatalf("bootstrapped create after releasing legacy reservation = %+v, %v", result, err)
	}
	if result.ResourceKey == reservation.ResourceKey {
		t.Fatal("new create reused the legacy fingerprint without the bootstrap ID and digest")
	}
}

func TestProvisionReleasesUnsentReservationWhenBootstrapDigestFails(t *testing.T) {
	provider := successfulProvider()
	provider.script = func(_ context.Context, _ []byte, id string) (vultrapi.StartupScript, error) {
		return vultrapi.StartupScript{ID: id, Type: "boot", Script: base64.StdEncoding.EncodeToString([]byte("changed"))}, nil
	}
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
	seedReservation(t, provisioner, repository, input, false)
	if _, err := provisioner.Provision(context.Background(), input); !errors.Is(err, ErrBootstrapScript) {
		t.Fatalf("mismatched script returned %v", err)
	}
	if _, err := repository.GetCapacityReservation(input.OperationID); !errors.Is(err, store.ErrCapacityNotFound) {
		t.Fatalf("unsent reservation was not released: %v", err)
	}
	created, _, _ := provider.counts()
	if created != 0 {
		t.Fatalf("mismatched script dispatched %d VM creates", created)
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
	provider.createOutcomeUnknown()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()

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
	provider.createOutcomeUnknown()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
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
	provider.createOutcomeUnknown()
	provider.list = func(_ context.Context, _ []byte, requestedLabel string) ([]vultrapi.Instance, error) {
		expectedLabel = requestedLabel
		return []vultrapi.Instance{{ID: "instance-1", Label: requestedLabel}}, nil
	}
	provider.get = func(_ context.Context, _ []byte, id string) (vultrapi.Instance, error) {
		return vultrapi.Instance{ID: id, Label: expectedLabel, Status: "pending"}, nil
	}
	provisioner, _, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), defaultRequest())
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
	provider.createOutcomeUnknown()
	provider.list = func(_ context.Context, _ []byte, label string) ([]vultrapi.Instance, error) {
		return []vultrapi.Instance{{ID: "instance-1", Label: label}, {ID: "instance-2", Label: label}}, nil
	}
	provisioner, _, _ := newTestProvisioner(t, provider)
	reservation, err := provisioner.Provision(context.Background(), defaultRequest())
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
	reservation, err := provisioner.Provision(context.Background(), defaultRequest())
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
	expectUntouched(t, provider, nil, "invalid requests")
}

func TestProvisionerRejectsDeclarativePolicy(t *testing.T) {
	repository := openRepository(t)
	policy := testPolicy()
	policy.RuntimeEnforced = false
	credentials := &testCredentials{reference: "systemd-credential://vultr-token", token: []byte("fake-provider-token")}
	if _, err := NewProvisioner(repository, policy, successfulProvider(), credentials, credentials.reference); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("declarative policy constructor error = %v, want ErrInvalidRequest", err)
	}
}

func TestUnavailableCredentialLeavesNoCapacityReservation(t *testing.T) {
	provider := successfulProvider()
	repository := openRepository(t)
	credentials := &testCredentials{reference: "systemd-credential://vultr-token", err: errors.New("credential unavailable")}
	provisioner, err := NewProvisioner(repository, testPolicy(), provider, credentials, credentials.reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Provision(context.Background(), defaultRequest()); err == nil {
		t.Fatal("missing credential unexpectedly allowed provisioning")
	}
	expectUntouched(t, provider, repository, "unavailable credential")
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

// A reservation from the build before firewall groups (script pinned, no
// firewall) is reconciled, never recreated with the new identity.
func TestProvisionReconcilesPreFirewallUnknownCreate(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	reservation := seedReservation(t, provisioner, repository, olderBuildRequest(false), true)
	result, err := provisioner.Provision(context.Background(), defaultRequest())
	if !errors.Is(err, ErrOutcomeUnknown) || result.ResourceKey != reservation.ResourceKey {
		t.Fatalf("pre-firewall reconciliation = %+v, %v", result, err)
	}
	if created, _, _ := provider.counts(); created != 0 {
		t.Fatal("a pre-firewall create was sent again")
	}
}

func TestProvisionSendsAOneTimeHostKeyAndStoresOnlyItsPublicHalf(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
	if _, err := provisioner.Provision(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	sent := provider.created[0]
	if sent.FirewallGroupID != "firewall-123" {
		t.Fatalf("firewall group not attached: %+v", sent)
	}
	userData, err := base64.StdEncoding.DecodeString(sent.UserData)
	if err != nil || !strings.HasPrefix(string(userData), "#cloud-config\nssh_deletekeys: true\nssh_genkeytypes: []\n") {
		t.Fatalf("user data = %q, %v", userData, err)
	}
	record, err := repository.GetHostHandoff(input.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userData), "ed25519_public: "+record.OneTimeHostKey+" ") {
		t.Fatal("stored public key is not the key that was sent")
	}
	start := strings.Index(string(userData), "-----BEGIN")
	end := strings.Index(string(userData), "-----END OPENSSH PRIVATE KEY-----")
	var pemLines []string
	for _, line := range strings.Split(string(userData)[start:end+len("-----END OPENSSH PRIVATE KEY-----")], "\n") {
		pemLines = append(pemLines, strings.TrimSpace(line))
	}
	private, err := sshkey.ParsePrivateKey([]byte(strings.Join(pemLines, "\n") + "\n"))
	if err != nil {
		t.Fatalf("user data private key: %v", err)
	}
	if sshkey.AuthorizedKey(private.Public().(ed25519.PublicKey)) != record.OneTimeHostKey {
		t.Fatal("private and stored public halves differ")
	}
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatal("the store holds the private key")
	}
}

func TestProvisionRefusesWrongSSHKeysOrAnOpenFirewall(t *testing.T) {
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE"
	for name, test := range map[string]struct {
		keys     map[string]string
		firewall []vultrapi.FirewallRule
		want     error
	}{
		"another ed25519 key": {keys: map[string]string{"sshkey-123": other}, want: ErrSSHKeyMismatch},
		"two ed25519 keys":    {keys: map[string]string{"sshkey-123": testControllerKey, "sshkey-456": other}, want: ErrSSHKeyMismatch},
		"open firewall":       {firewall: []vultrapi.FirewallRule{{ID: 1, Action: "accept", Protocol: "tcp", Port: "22", Subnet: "0.0.0.0"}}, want: ErrFirewall},
	} {
		t.Run(name, func(t *testing.T) {
			provider := successfulProvider()
			provider.sshKeys, provider.firewall = test.keys, test.firewall
			provisioner, repository, _ := newTestProvisioner(t, provider)
			input := defaultRequest()
			if len(test.keys) == 2 {
				input.SSHKeyIDs = []string{"sshkey-123", "sshkey-456"}
			}
			if _, err := provisioner.Provision(context.Background(), input); !errors.Is(err, test.want) {
				t.Fatalf("Provision = %v, want %v", err, test.want)
			}
			if created, _, _ := provider.counts(); created != 0 {
				t.Fatalf("create=%d", created)
			}
			if reservations, err := repository.ListCapacityReservations(); err != nil || len(reservations) != 0 {
				t.Fatalf("reservations=%+v err=%v", reservations, err)
			}
		})
	}
	provider := successfulProvider()
	provisioner, _, _ := newTestProvisioner(t, provider)
	input := defaultRequest()
	input.FirewallGroupID = ""
	if _, err := provisioner.Provision(context.Background(), input); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("create without a firewall group: %v", err)
	}
}
