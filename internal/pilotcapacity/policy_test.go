package pilotcapacity

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestLoadPolicyReservesByConfiguredEnvironmentAndLimit(t *testing.T) {
	policy, err := LoadPolicy("capacity-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RuntimeEnforced {
		t.Fatal("capacity policy did not load its runtime enforcement flag")
	}
	repository, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	request := store.CapacityReservation{
		OperationID: "pilot-staging-create", ServerID: "test-server", EnvironmentID: "staging",
		Provider: "vultr", ResourceKey: "gsc-test-server",
	}
	reservation, created, err := policy.ReserveCapacity(repository, request)
	if err != nil || !created || reservation.CapacityGroup != "shared_nonproduction" {
		t.Fatalf("staging reservation = (%+v, %t, %v)", reservation, created, err)
	}
	request.OperationID = "pilot-production-create"
	request.ServerID = "production-server"
	request.EnvironmentID = "production"
	request.ResourceKey = "gsc-production-server"
	if _, _, err := policy.ReserveCapacity(repository, request); !errors.Is(err, store.ErrCapacityReached) {
		t.Fatalf("second managed VM with configured total limit: got %v, want ErrCapacityReached", err)
	}
}

func TestReserveCapacityRejectsUnsupportedEnvironmentAndMismatchedGroup(t *testing.T) {
	policy, err := LoadPolicy("capacity-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	repository, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	request := store.CapacityReservation{
		OperationID: "pilot-unsupported", ServerID: "test-server", EnvironmentID: "development",
		Provider: "vultr", ResourceKey: "gsc-test-server",
	}
	if _, _, err := policy.ReserveCapacity(repository, request); !errors.Is(err, ErrUnsupportedEnvironment) {
		t.Fatalf("unsupported environment: got %v, want ErrUnsupportedEnvironment", err)
	}
	request.EnvironmentID = "staging"
	request.CapacityGroup = "production"
	if _, _, err := policy.ReserveCapacity(repository, request); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("mismatched group: got %v, want ErrInvalidPolicy", err)
	}
}

func TestDecodePolicyRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	data, err := os.ReadFile("capacity-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(data), "\n")
	unknown = strings.TrimSuffix(unknown, "}") + ",\n  \"unexpected\": true\n}"
	if _, err := DecodePolicy(strings.NewReader(unknown)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown field: got %v, want ErrInvalidPolicy", err)
	}
	if _, err := DecodePolicy(strings.NewReader(string(data) + `{}`)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("trailing JSON object: got %v, want ErrInvalidPolicy", err)
	}
}

func TestEmbeddedPolicyMatchesEditableSourceFile(t *testing.T) {
	source, err := LoadPolicy("capacity-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := LoadEmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(embedded, source) {
		t.Fatalf("embedded policy differs from source policy: embedded=%+v source=%+v", embedded, source)
	}
}
