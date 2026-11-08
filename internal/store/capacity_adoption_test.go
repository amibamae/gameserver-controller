package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func adoptionEvidence() CapacityAdoption {
	return CapacityAdoption{InstanceID: "example-instance", IP: "203.0.113.10", Region: "example-region", Plan: "example-plan",
		OSID: 123, OS: "Example Linux", ObservedAt: time.Now().UTC()}
}

func TestAdoptionCommitsExactEvidenceAndIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	request := capacityRequest("adopt-example", "example-server", "production", "production")
	evidence := adoptionEvidence()
	first, err := s.AdoptCapacity(request, evidence, 2, 1)
	if err != nil || first.State != CapacityActive || first.Adoption == nil || first.ProviderInstanceID != evidence.InstanceID || first.LastObservation == nil {
		t.Fatalf("adoption = %#v, %v", first, err)
	}
	evidence.ObservedAt = time.Now().UTC()
	second, err := s.AdoptCapacity(request, evidence, 2, 1)
	if err != nil || !second.CreatedAt.Equal(first.CreatedAt) || !second.Adoption.ObservedAt.Equal(first.Adoption.ObservedAt) {
		t.Fatalf("repeat changed committed adoption: %#v, %v", second, err)
	}
	evidence.IP = "203.0.113.11"
	if _, err := s.AdoptCapacity(request, evidence, 2, 1); !errors.Is(err, ErrCapacityConflict) {
		t.Fatal("changed adoption identity accepted", err)
	}
	if _, err := s.RecordCapacityReady(request.ServerID, request.EnvironmentID, first.ProviderInstanceID, time.Now().UTC()); !errors.Is(err, ErrCapacityTransition) {
		t.Fatal("adoption generated a controller provisioning sample", err)
	}
}

func TestAdoptionRejectsDuplicateProviderIDAndRollsBack(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.AdoptCapacity(capacityRequest("first", "production", "production", "production"), adoptionEvidence(), 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptCapacity(capacityRequest("second", "testing", "testing", "nonproduction"), adoptionEvidence(), 2, 1); !errors.Is(err, ErrCapacityConflict) {
		t.Fatal("same VM counted twice", err)
	}
	if _, err := s.GetCapacityReservation("second"); !errors.Is(err, ErrCapacityNotFound) {
		t.Fatal("failed adoption left a reservation", err)
	}
}

func TestConcurrentAdoptionAndCreateShareAtomicCapacity(t *testing.T) {
	s := openTestStore(t)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := s.AdoptCapacity(capacityRequest("adoption", "existing", "production", "production"), adoptionEvidence(), 1, 1)
		results <- err
	}()
	go func() {
		defer wg.Done()
		_, _, err := s.ReserveCapacity(capacityRequest("creation", "new", "testing", "testing"), 1, 1)
		results <- err
	}()
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrCapacityReached) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestInvalidAdoptionEvidenceNeverClaimsCapacity(t *testing.T) {
	for _, scenario := range []string{"ip", "private", "os", "future", "duplicate-role"} {
		t.Run(scenario, func(t *testing.T) {
			s := openTestStore(t)
			evidence := adoptionEvidence()
			request := capacityRequest("adopt", "example", "production", "production")
			switch scenario {
			case "ip":
				evidence.IP = "not-an-ip"
			case "private":
				evidence.IP = "192.168.1.1"
			case "os":
				evidence.OSID = 0
			case "future":
				evidence.ObservedAt = time.Now().Add(time.Hour)
			case "duplicate-role":
				if _, _, err := s.ReserveCapacity(capacityRequest("reserved", "example", "production", "production"), 2, 1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.AdoptCapacity(request, evidence, 2, 1); err == nil {
				t.Fatal("invalid adoption accepted")
			}
			if _, err := s.GetCapacityReservation("adopt"); !errors.Is(err, ErrCapacityNotFound) {
				t.Fatal("failed adoption left state", err)
			}
		})
	}
}
