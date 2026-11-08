package pilotvultr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

type adoptionProvider struct {
	list func() ([]vultrapi.Instance, error)
	get  func() (vultrapi.Instance, error)
}

func (p adoptionProvider) ListInstances(context.Context, []byte) ([]vultrapi.Instance, error) {
	return p.list()
}
func (p adoptionProvider) GetInstance(context.Context, []byte, string) (vultrapi.Instance, error) {
	return p.get()
}

func adoptionInput() AdoptionRequest {
	return AdoptionRequest{OperationID: strings.Repeat("a", 32), ServerID: "example-server", Environment: "production",
		InstanceID: "example-instance", IP: "203.0.113.10", Region: "example-region", Plan: "example-plan", OSID: 123, OS: "Example Linux"}
}
func adoptionInstance(r AdoptionRequest) vultrapi.Instance {
	return vultrapi.Instance{ID: r.InstanceID, MainIP: r.IP, Region: r.Region, Plan: r.Plan, OSID: r.OSID, OS: r.OS, Label: "existing-example"}
}

func TestAdoptionUsesExactInventoryAndPreservesUnmanagedVMs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	request := adoptionInput()
	vm := adoptionInstance(request)
	provider := adoptionProvider{list: func() ([]vultrapi.Instance, error) {
		return []vultrapi.Instance{vm, {ID: "unmanaged-a"}, {ID: "unmanaged-b"}}, nil
	}, get: func() (vultrapi.Instance, error) { return vm, nil }}
	credentials := &testCredentials{reference: "systemd-credential://example", token: []byte("test-only-token")}
	a, err := NewAdopter(s, testPolicy(), provider, credentials, credentials.reference)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.Adopt(context.Background(), request)
	if err != nil || first.State != store.CapacityActive || first.Adoption == nil || !first.ProvisionedAt.IsZero() {
		t.Fatalf("adoption=%#v error=%v", first, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, _ = NewAdopter(s, testPolicy(), provider, credentials, credentials.reference)
	second, err := a.Adopt(context.Background(), request)
	if err != nil || !first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("restart repeat=%#v error=%v", second, err)
	}
	all, err := s.ListCapacityReservations()
	if err != nil || len(all) != 1 {
		t.Fatalf("unexpected ownership=%#v error=%v", all, err)
	}
}

func TestAdoptionRejectsMismatchesWithoutWritingOwnership(t *testing.T) {
	for _, scenario := range []string{"list-error", "duplicate", "missing", "region", "ip", "plan", "os", "os-id", "direct-mismatch", "direct-error", "managed-label", "occupied", "unknown-create", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			s, err := store.OpenBolt(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			request := adoptionInput()
			vm := adoptionInstance(request)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "occupied" || scenario == "unknown-create" {
				_, _, err := s.ReserveCapacity(store.CapacityReservation{OperationID: "previous", ServerID: "previous", EnvironmentID: "production", CapacityGroup: "production", Provider: "vultr", ResourceKey: "gsc-managed-previous"}, 1, 1)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "unknown-create" {
					if _, err := s.MarkCapacityCreateSent("previous"); err != nil {
						t.Fatal(err)
					}
				}
			}
			provider := adoptionProvider{list: func() ([]vultrapi.Instance, error) {
				switch scenario {
				case "list-error":
					return nil, errors.New("sensitive provider detail")
				case "duplicate":
					return []vultrapi.Instance{vm, vm}, nil
				case "missing":
					return nil, nil
				case "region":
					vm.Region = "other"
				case "ip":
					vm.MainIP = "203.0.113.11"
				case "plan":
					vm.Plan = "other"
				case "os":
					vm.OS = "other"
				case "os-id":
					vm.OSID = 456
				case "managed-label":
					vm.Label = "gsc-managed-unknown"
				case "canceled":
					cancel()
				}
				return []vultrapi.Instance{vm}, nil
			}, get: func() (vultrapi.Instance, error) {
				if scenario == "direct-error" {
					return vultrapi.Instance{}, errors.New("private token")
				}
				if scenario == "direct-mismatch" {
					vm.MainIP = "203.0.113.11"
				}
				return vm, nil
			}}
			credentials := &testCredentials{reference: "systemd-credential://example", token: []byte("test-token")}
			a, _ := NewAdopter(s, testPolicy(), provider, credentials, credentials.reference)
			_, err = a.Adopt(ctx, request)
			if err == nil || strings.Contains(err.Error(), "private token") || strings.Contains(err.Error(), "sensitive provider detail") {
				t.Fatal("unsafe adoption result", err)
			}
			if _, err := s.GetCapacityReservation(request.OperationID); !errors.Is(err, store.ErrCapacityNotFound) {
				t.Fatal("failed adoption wrote ownership", err)
			}
		})
	}
}

func TestAdoptionThroughHTTPClientUsesOnlyReads(t *testing.T) {
	request := adoptionInput()
	vm := adoptionInstance(request)
	calls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected provider mutation or authentication: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/v2/instances":
			_ = json.NewEncoder(w).Encode(map[string]any{"instances": []vultrapi.Instance{vm}, "meta": map[string]any{"total": 1, "links": map[string]string{"next": ""}}})
		case "/v2/instances/" + vm.ID:
			_ = json.NewEncoder(w).Encode(map[string]any{"instance": vm})
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	transport := adoptionTransport(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})
	client, err := vultrapi.NewClientAt("https://api.example.invalid/v2", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenBolt(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	credentials := &testCredentials{reference: "systemd-credential://example", token: []byte("test-token")}
	a, err := NewAdopter(s, testPolicy(), client, credentials, credentials.reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Adopt(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("provider requests=%d, want inventory and exact lookup", calls)
	}
}

type adoptionTransport func(*http.Request) (*http.Response, error)

func (f adoptionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
