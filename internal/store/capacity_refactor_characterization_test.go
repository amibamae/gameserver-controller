package store

// Characterization tests for capacity.go. They pin the exact sentinel errors,
// error messages, returned values, and stored reservation state of every
// capacity transition so that structural refactors stay behaviour-preserving.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	capacityRefactorInvalid     = "invalid operation record"
	capacityRefactorCorrupt     = "corrupt operation store"
	capacityRefactorConflictMsg = "capacity reservation conflicts with stored state"
)

// capacityRefactorAssertErr fails unless err wraps want and, when message is
// non-empty, err.Error() equals message exactly.
func capacityRefactorAssertErr(t *testing.T, err, want error, message string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if message != "" && err.Error() != message {
		t.Fatalf("error message = %q, want %q", err.Error(), message)
	}
}

// capacityRefactorAssertSentinel fails unless err is exactly the sentinel
// (not wrapped).
func capacityRefactorAssertSentinel(t *testing.T, err, want error) {
	t.Helper()
	if err != want { //nolint:errorlint // exact identity is the point.
		t.Fatalf("error = %#v, want exactly %v", err, want)
	}
}

func capacityRefactorJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func capacityRefactorRawGet(t *testing.T, repository *BoltStore, key string) []byte {
	t.Helper()
	var value []byte
	if err := repository.db.View(func(tx *bolt.Tx) error {
		if stored := tx.Bucket(capacityReservationsBucket).Get([]byte(key)); stored != nil {
			value = append([]byte(nil), stored...)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func capacityRefactorRawPut(t *testing.T, repository *BoltStore, key string, value []byte) {
	t.Helper()
	if err := repository.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(capacityReservationsBucket).Put([]byte(key), value)
	}); err != nil {
		t.Fatal(err)
	}
}

func capacityRefactorPut(t *testing.T, repository *BoltStore, reservation CapacityReservation) {
	t.Helper()
	capacityRefactorRawPut(t, repository, reservation.OperationID, []byte(capacityRefactorJSON(t, reservation)))
}

func capacityRefactorLoad(t *testing.T, repository *BoltStore, operationID string) CapacityReservation {
	t.Helper()
	loaded, err := repository.GetCapacityReservation(operationID)
	if err != nil {
		t.Fatalf("load %s: %v", operationID, err)
	}
	return loaded
}

// capacityRefactorAssertStored fails unless the raw stored bytes for key equal
// want (nil means absent).
func capacityRefactorAssertStored(t *testing.T, repository *BoltStore, key string, want []byte) {
	t.Helper()
	if got := capacityRefactorRawGet(t, repository, key); string(got) != string(want) || (got == nil) != (want == nil) {
		t.Fatalf("stored %s = %s, want %s", key, got, want)
	}
}

// capacityRefactorActive reserves and observes an instance as present.
func capacityRefactorActive(t *testing.T, repository *BoltStore, operationID, serverID, instanceID string) CapacityReservation {
	t.Helper()
	reservation, _, err := repository.ReserveCapacity(capacityRequest(operationID, serverID, "staging", "group-"+serverID), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	return observePresent(t, repository, reservation, instanceID, reservation.CreatedAt.Add(time.Second), reservation.CreatedAt)
}

func TestCapacityRefactorReserveCapacityCharacterization(t *testing.T) {
	t.Run("invalid limits", func(t *testing.T) {
		repository := openTestStore(t)
		for _, limits := range [][2]int{{0, 1}, {1, 0}, {-1, -1}} {
			result, created, err := repository.ReserveCapacity(capacityRequest("op", "srv", "staging", "g"), limits[0], limits[1])
			capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid capacity limits")
			if created || result.OperationID != "" {
				t.Fatalf("invalid limits returned %+v, %v", result, created)
			}
		}
		capacityRefactorAssertStored(t, repository, "op", nil)
	})
	t.Run("request validated before limits", func(t *testing.T) {
		repository := openTestStore(t)
		request := capacityRequest("op", "srv", "staging", "g")
		request.State = CapacityActive
		_, _, err := repository.ReserveCapacity(request, 0, 0)
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid capacity reservation request")
	})
	t.Run("new reservation stored", func(t *testing.T) {
		repository := openTestStore(t)
		before := time.Now().UTC()
		request := capacityRequest("op-new", "srv", "staging", "g")
		request.ProviderAdapterID = "hosting.vultr"
		result, created, err := repository.ReserveCapacity(request, 1, 1)
		if err != nil || !created {
			t.Fatalf("reserve = %v, %v", created, err)
		}
		if result.State != CapacityReserved || !result.CreatedAt.Equal(result.UpdatedAt) || result.CreatedAt.Before(before) ||
			result.CreatedAt.Location() != time.UTC || result.ProviderAdapterID != "hosting.vultr" {
			t.Fatalf("reserved = %+v", result)
		}
		capacityRefactorAssertStored(t, repository, "op-new", []byte(capacityRefactorJSON(t, result)))
	})
	t.Run("replay returns stored reservation before scanning", func(t *testing.T) {
		repository := openTestStore(t)
		first := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored := capacityRefactorRawGet(t, repository, "op-a")
		// A corrupt neighbour is not reached by an idempotent replay.
		capacityRefactorRawPut(t, repository, "op-z", []byte("{"))
		replay, created, err := repository.ReserveCapacity(capacityRequest("op-a", "srv", "staging", "g"), 1, 1)
		if err != nil || created || capacityRefactorJSON(t, replay) != capacityRefactorJSON(t, first) {
			t.Fatalf("replay = %+v, %v, %v", replay, created, err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
		// A new operation does scan and hits the corrupt entry.
		_, _, err = repository.ReserveCapacity(capacityRequest("op-b", "srv-b", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
		if !strings.HasPrefix(err.Error(), capacityRefactorCorrupt+": decode capacity reservation: ") {
			t.Fatalf("decode error = %q", err)
		}
	})
	t.Run("replay with different identity conflicts", func(t *testing.T) {
		mutations := map[string]func(*CapacityReservation){
			"server":   func(r *CapacityReservation) { r.ServerID = "other" },
			"env":      func(r *CapacityReservation) { r.EnvironmentID = "other" },
			"group":    func(r *CapacityReservation) { r.CapacityGroup = "other" },
			"provider": func(r *CapacityReservation) { r.Provider = "other" },
			"adapter":  func(r *CapacityReservation) { r.ProviderAdapterID = "hosting.other" },
			"resource": func(r *CapacityReservation) { r.ResourceKey = "other" },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				repository := openTestStore(t)
				reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
				request := capacityRequest("op-a", "srv", "staging", "g")
				mutate(&request)
				result, created, err := repository.ReserveCapacity(request, 9, 9)
				capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
				if created || result.OperationID != "" {
					t.Fatalf("conflict returned %+v", result)
				}
			})
		}
	})
	t.Run("occupied slot", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		_, _, err := repository.ReserveCapacity(capacityRequest("op-b", "srv", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCapacityOccupied, `managed VM slot is occupied: server "srv" in environment "staging"`)
	})
	t.Run("resource key already reserved", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		request := capacityRequest("op-b", "srv-b", "staging", "g")
		request.ResourceKey = "gsc-srv"
		_, _, err := repository.ReserveCapacity(request, 9, 9)
		capacityRefactorAssertErr(t, err, ErrCapacityConflict, capacityRefactorConflictMsg+`: provider resource key "gsc-srv" is already reserved`)
		// Same resource key under another provider does not conflict.
		request.Provider = "other"
		if _, _, err := repository.ReserveCapacity(request, 9, 9); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("occupied checked before resource key", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		_, _, err := repository.ReserveCapacity(capacityRequest("op-b", "srv", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCapacityOccupied, "")
	})
	t.Run("total and group limits", func(t *testing.T) {
		repository := openTestStore(t)
		if _, _, err := repository.ReserveCapacity(capacityRequest("op-a", "srv-a", "staging", "g1"), 2, 1); err != nil {
			t.Fatal(err)
		}
		_, _, err := repository.ReserveCapacity(capacityRequest("op-b", "srv-b", "staging", "g1"), 2, 1)
		capacityRefactorAssertSentinel(t, err, ErrCapacityReached)
		if _, _, err := repository.ReserveCapacity(capacityRequest("op-c", "srv-c", "staging", "g2"), 2, 1); err != nil {
			t.Fatal(err)
		}
		_, _, err = repository.ReserveCapacity(capacityRequest("op-d", "srv-d", "staging", "g3"), 2, 1)
		capacityRefactorAssertSentinel(t, err, ErrCapacityReached)
		capacityRefactorAssertStored(t, repository, "op-b", nil)
		capacityRefactorAssertStored(t, repository, "op-d", nil)
	})
	t.Run("released reservations do not count or occupy", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		if _, err := repository.ObserveCapacity("op-a", capacityObservation(active, "absent", "", active.LastObservation.ObservedAt.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		request := capacityRequest("op-b", "srv", "staging", "group-srv")
		if _, _, err := repository.ReserveCapacity(request, 1, 1); err != nil {
			t.Fatalf("reserve after release: %v", err)
		}
	})
	t.Run("key mismatch", func(t *testing.T) {
		repository := openTestStore(t)
		stored := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		capacityRefactorRawPut(t, repository, "op-alias", []byte(capacityRefactorJSON(t, stored)))
		_, _, err := repository.ReserveCapacity(capacityRequest("op-b", "srv-b", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": capacity reservation key mismatch")
	})
	t.Run("invalid persisted reservation", func(t *testing.T) {
		repository := openTestStore(t)
		stored := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored.State = "bogus"
		capacityRefactorPut(t, repository, stored)
		_, _, err := repository.ReserveCapacity(capacityRequest("op-b", "srv-b", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": invalid capacity reservation: "+capacityRefactorInvalid)
		_, _, err = repository.ReserveCapacity(capacityRequest("op-a", "srv", "staging", "g"), 9, 9)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": invalid capacity reservation: "+capacityRefactorInvalid)
	})
}

func TestCapacityRefactorValidateReservationRequestCharacterization(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]struct {
		mutate func(*CapacityReservation)
		bare   bool
	}{
		"adapter control char": {func(r *CapacityReservation) { r.ProviderAdapterID = "bad\x01" }, true},
		"adapter too long":     {func(r *CapacityReservation) { r.ProviderAdapterID = long(161) }, true},
		"operation empty":      {func(r *CapacityReservation) { r.OperationID = "" }, false},
		"operation too long":   {func(r *CapacityReservation) { r.OperationID = long(201) }, false},
		"server":               {func(r *CapacityReservation) { r.ServerID = long(201) }, false},
		"environment":          {func(r *CapacityReservation) { r.EnvironmentID = "a\x7f" }, false},
		"group":                {func(r *CapacityReservation) { r.CapacityGroup = long(101) }, false},
		"provider":             {func(r *CapacityReservation) { r.Provider = long(101) }, false},
		"resource":             {func(r *CapacityReservation) { r.ResourceKey = long(241) }, false},
		"instance":             {func(r *CapacityReservation) { r.ProviderInstanceID = "vm" }, false},
		"state":                {func(r *CapacityReservation) { r.State = CapacityReserved }, false},
		"created":              {func(r *CapacityReservation) { r.CreatedAt = time.Now() }, false},
		"updated":              {func(r *CapacityReservation) { r.UpdatedAt = time.Now() }, false},
		"provisioned":          {func(r *CapacityReservation) { r.ProvisionedAt = time.Now() }, false},
		"ready":                {func(r *CapacityReservation) { r.ReadyAt = time.Now() }, false},
		"observation":          {func(r *CapacityReservation) { r.LastObservation = &CapacityProviderObservation{} }, false},
		"adoption":             {func(r *CapacityReservation) { r.Adoption = &CapacityAdoption{} }, false},
		"adapter bad and state": {func(r *CapacityReservation) {
			r.ProviderAdapterID = "bad\x01"
			r.State = CapacityActive
		}, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			request := capacityRequest("op", "srv", "staging", "g")
			tc.mutate(&request)
			err := validateCapacityReservationRequest(request)
			if tc.bare {
				capacityRefactorAssertSentinel(t, err, ErrInvalidRecord)
				return
			}
			capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid capacity reservation request")
		})
	}
	valid := capacityRequest(long(200), long(200), long(200), long(100))
	valid.Provider, valid.ResourceKey, valid.ProviderAdapterID = long(100), long(240), long(160)
	if err := validateCapacityReservationRequest(valid); err != nil {
		t.Fatalf("maximum lengths rejected: %v", err)
	}
}

func TestCapacityRefactorReleaseUnsentCharacterization(t *testing.T) {
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("", reserved.ResourceKey), ErrCapacityNotFound)
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("op-a", ""), ErrCapacityNotFound)
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("op-a", strings.Repeat("k", 241)), ErrCapacityNotFound)
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("missing", reserved.ResourceKey), ErrCapacityNotFound)
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("op-a", "other-key"), ErrCapacityConflict)
	stored := capacityRefactorRawGet(t, repository, "op-a")
	if _, err := repository.MarkCapacityCreateSent("op-a"); err != nil {
		t.Fatal(err)
	}
	sent := capacityRefactorRawGet(t, repository, "op-a")
	// Resource key is checked before state.
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("op-a", "other-key"), ErrCapacityConflict)
	capacityRefactorAssertSentinel(t, repository.ReleaseUnsentCapacityReservation("op-a", reserved.ResourceKey), ErrCapacityTransition)
	capacityRefactorAssertStored(t, repository, "op-a", sent)
	if string(stored) == string(sent) {
		t.Fatal("create claim did not change the stored record")
	}

	repository2 := openTestStore(t)
	second := reserve(t, repository2, capacityRequest("op-b", "srv", "staging", "g"))
	if err := repository2.ReleaseUnsentCapacityReservation("op-b", second.ResourceKey); err != nil {
		t.Fatal(err)
	}
	capacityRefactorAssertStored(t, repository2, "op-b", nil)

	capacityRefactorRawPut(t, repository2, "op-c", []byte("not json"))
	err := repository2.ReleaseUnsentCapacityReservation("op-c", "k")
	capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
	capacityRefactorAssertStored(t, repository2, "op-c", []byte("not json"))
}

func TestCapacityRefactorDispatchClaimsCharacterization(t *testing.T) {
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	_, err := repository.MarkCapacityDestroySent("op-a")
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	_, err = repository.MarkCapacityCreateSent("")
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
	_, err = repository.MarkCapacityCreateSent("missing")
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)

	sent, err := repository.MarkCapacityCreateSent("op-a")
	if err != nil {
		t.Fatal(err)
	}
	if sent.State != CapacityCreateOutcomeUnknown || sent.UpdatedAt.Before(reserved.UpdatedAt) || !sent.CreatedAt.Equal(reserved.CreatedAt) {
		t.Fatalf("create sent = %+v", sent)
	}
	capacityRefactorAssertStored(t, repository, "op-a", []byte(capacityRefactorJSON(t, sent)))
	_, err = repository.MarkCapacityCreateSent("op-a")
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	_, err = repository.MarkCapacityDestroySent("op-a")
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)

	active, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "present", "vm-a", reserved.CreatedAt.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	destroying, err := repository.MarkCapacityDestroySent("op-a")
	if err != nil {
		t.Fatal(err)
	}
	if destroying.State != CapacityDestroyOutcomeUnknown || destroying.ProviderInstanceID != "vm-a" ||
		destroying.UpdatedAt.Before(active.UpdatedAt) || destroying.LastObservation == nil {
		t.Fatalf("destroy sent = %+v", destroying)
	}
	capacityRefactorAssertStored(t, repository, "op-a", []byte(capacityRefactorJSON(t, destroying)))
}

func TestCapacityRefactorUpdateCapacityCharacterization(t *testing.T) {
	t.Run("lookup errors", func(t *testing.T) {
		repository := openTestStore(t)
		called := false
		transition := func(r CapacityReservation) (CapacityReservation, error) { called = true; return r, nil }
		_, err := repository.updateCapacity(strings.Repeat("o", 201), transition)
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		_, err = repository.updateCapacity("missing", transition)
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		capacityRefactorRawPut(t, repository, "corrupt", []byte("{}"))
		_, err = repository.updateCapacity("corrupt", transition)
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": invalid capacity reservation: "+capacityRefactorInvalid)
		if called {
			t.Fatal("transition called without a stored reservation")
		}
	})
	t.Run("transition error passthrough", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored := capacityRefactorRawGet(t, repository, "op-a")
		sentinel := errors.New("transition refused")
		result, err := repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
			r.State = CapacityActive
			return r, sentinel
		})
		if err != sentinel || result.OperationID != "" { //nolint:errorlint
			t.Fatalf("update = %+v, %v", result, err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("immutable fields", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		if _, err := repository.RecordCapacityReady("srv", "staging", "vm-a", active.LastObservation.ObservedAt); err != nil {
			t.Fatal(err)
		}
		stored := capacityRefactorRawGet(t, repository, "op-a")
		mutations := map[string]func(*CapacityReservation){
			"operation":   func(r *CapacityReservation) { r.OperationID = "op-b" },
			"created":     func(r *CapacityReservation) { r.CreatedAt = r.CreatedAt.Add(-time.Second) },
			"adapter":     func(r *CapacityReservation) { r.ProviderAdapterID = "hosting.vultr" },
			"provisioned": func(r *CapacityReservation) { r.ProvisionedAt = r.ProvisionedAt.Add(time.Nanosecond) },
			"ready":       func(r *CapacityReservation) { r.ReadyAt = time.Time{} },
		}
		for name, mutate := range mutations {
			t.Run(name, func(t *testing.T) {
				result, err := repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
					mutate(&r)
					return r, nil
				})
				capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": immutable capacity reservation fields changed")
				if result.OperationID != "" {
					t.Fatalf("result = %+v", result)
				}
				capacityRefactorAssertStored(t, repository, "op-a", stored)
			})
		}
		// CreatedAt in another location with the same instant is still a
		// change: the comparison is ==, not Equal.
		_, err := repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
			r.CreatedAt = r.CreatedAt.In(time.FixedZone("x", 3600))
			r.State = CapacityDestroyOutcomeUnknown
			return r, nil
		})
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": immutable capacity reservation fields changed")
	})
	t.Run("untracked change is returned but not stored", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored := capacityRefactorRawGet(t, repository, "op-a")
		result, err := repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
			r.CapacityGroup = "changed"
			r.UpdatedAt = r.UpdatedAt.Add(time.Hour)
			return r, nil
		})
		if err != nil || result.CapacityGroup != "changed" {
			t.Fatalf("update = %+v, %v", result, err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("updated time floors", func(t *testing.T) {
		repository := openTestStore(t)
		capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		repository2 := openTestStore(t)
		reserved, _, err := repository2.ReserveCapacity(capacityRequest("op-p", "srv-p", "staging", "g"), 9, 9)
		if err != nil {
			t.Fatal(err)
		}
		// ProvisionedAt set for the first time in the future floors UpdatedAt.
		future := time.Now().UTC().Add(2 * time.Minute)
		result, err := repository2.updateCapacity("op-p", func(r CapacityReservation) (CapacityReservation, error) {
			r.ProvisionedAt = future
			return r, nil
		})
		if err != nil || !result.UpdatedAt.Equal(future) || !result.CreatedAt.Equal(reserved.CreatedAt) {
			t.Fatalf("provisioned floor = %+v, %v", result, err)
		}
		capacityRefactorAssertStored(t, repository2, "op-p", []byte(capacityRefactorJSON(t, result)))
		// ReadyAt in the future floors UpdatedAt.
		readyFuture := time.Now().UTC().Add(3 * time.Minute)
		result, err = repository2.updateCapacity("op-p", func(r CapacityReservation) (CapacityReservation, error) {
			r.ReadyAt = readyFuture
			return r, nil
		})
		if err != nil || !result.UpdatedAt.Equal(readyFuture) {
			t.Fatalf("ready floor = %+v, %v", result, err)
		}
		// A replaced observation in the future floors UpdatedAt.
		obsFuture := time.Now().UTC().Add(4 * time.Minute)
		result, err = repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
			observation := *r.LastObservation
			observation.ObservedAt = obsFuture
			r.LastObservation = &observation
			return r, nil
		})
		if err != nil || !result.UpdatedAt.Equal(obsFuture) {
			t.Fatalf("observation floor = %+v, %v", result, err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", []byte(capacityRefactorJSON(t, result)))
	})
	t.Run("invalid next state", func(t *testing.T) {
		repository := openTestStore(t)
		reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored := capacityRefactorRawGet(t, repository, "op-a")
		_, err := repository.updateCapacity("op-a", func(r CapacityReservation) (CapacityReservation, error) {
			r.State = "bogus"
			return r, nil
		})
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid updated capacity reservation: "+capacityRefactorInvalid)
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
}

func TestCapacityRefactorObserveCapacityValidationCharacterization(t *testing.T) {
	now := time.Now().UTC()
	base := CapacityProviderObservation{Provider: "vultr", ResourceKey: "gsc-srv", ProviderInstanceID: "vm", State: "present", ObservedAt: now}
	cases := map[string]struct {
		mutate  func(*CapacityProviderObservation)
		message string
	}{
		"provider":           {func(o *CapacityProviderObservation) { o.Provider = "" }, "invalid provider observation"},
		"resource":           {func(o *CapacityProviderObservation) { o.ResourceKey = strings.Repeat("k", 241) }, "invalid provider observation"},
		"zero time":          {func(o *CapacityProviderObservation) { o.ObservedAt = time.Time{} }, "invalid provider observation"},
		"future time":        {func(o *CapacityProviderObservation) { o.ObservedAt = now.Add(6 * time.Minute) }, "invalid provider observation"},
		"state":              {func(o *CapacityProviderObservation) { o.State = "running" }, "unsupported provider observation state"},
		"present no id":      {func(o *CapacityProviderObservation) { o.ProviderInstanceID = "" }, "present observation requires provider_instance_id"},
		"present bad id":     {func(o *CapacityProviderObservation) { o.ProviderInstanceID = "a\x01" }, "present observation requires provider_instance_id"},
		"absent provision":   {func(o *CapacityProviderObservation) { o.State, o.ProvisionedAt = "absent", now.Add(-time.Second) }, "invalid provider provisioning timestamp"},
		"provision after":    {func(o *CapacityProviderObservation) { o.ProvisionedAt = now.Add(time.Second) }, "invalid provider provisioning timestamp"},
		"absent bad id":      {func(o *CapacityProviderObservation) { o.State, o.ProviderInstanceID = "destroyed", "a\x01" }, "invalid provider_instance_id"},
		"bad state and time": {func(o *CapacityProviderObservation) { o.State, o.ObservedAt = "x", time.Time{} }, "invalid provider observation"},
	}
	repository := openTestStore(t)
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			observation := base
			tc.mutate(&observation)
			capacityRefactorAssertErr(t, validateCapacityObservation(observation), ErrInvalidRecord, capacityRefactorInvalid+": "+tc.message)
			// Validation happens before the operation lookup.
			_, err := repository.ObserveCapacity("missing", observation)
			capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": "+tc.message)
			err = repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "missing", observation) })
			capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": "+tc.message)
		})
	}
	for _, state := range []string{"present", "absent", "destroyed"} {
		observation := base
		observation.State = state
		observation.ObservedAt = now.Add(5*time.Minute - time.Second)
		if err := validateCapacityObservation(observation); err != nil {
			t.Fatalf("%s rejected: %v", state, err)
		}
	}
	_, err := repository.ObserveCapacity("", base)
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
}

func TestCapacityRefactorObserveCapacityTransitionsCharacterization(t *testing.T) {
	t.Run("identity and ordering conflicts", func(t *testing.T) {
		repository := openTestStore(t)
		reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		stored := capacityRefactorRawGet(t, repository, "op-a")
		at := reserved.CreatedAt.Add(time.Second)
		wrongProvider := capacityObservation(reserved, "present", "vm", at)
		wrongProvider.Provider = "other"
		wrongKey := capacityObservation(reserved, "present", "vm", at)
		wrongKey.ResourceKey = "other"
		early := capacityObservation(reserved, "present", "vm", reserved.CreatedAt.Add(-time.Nanosecond))
		for _, observation := range []CapacityProviderObservation{wrongProvider, wrongKey, early} {
			result, err := repository.ObserveCapacity("op-a", observation)
			capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
			if result.OperationID != "" {
				t.Fatalf("result = %+v", result)
			}
		}
		earlyProvision := capacityObservation(reserved, "present", "vm", at)
		earlyProvision.ProvisionedAt = reserved.CreatedAt.Add(-time.Nanosecond)
		_, err := repository.ObserveCapacity("op-a", earlyProvision)
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": provider provisioning time precedes reservation")
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("present then replay then ordering", func(t *testing.T) {
		repository := openTestStore(t)
		reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		at := reserved.CreatedAt.Add(time.Second)
		present := capacityObservation(reserved, "present", "vm-a", at)
		present.ProvisionedAt = reserved.CreatedAt
		active, err := repository.ObserveCapacity("op-a", present)
		if err != nil {
			t.Fatal(err)
		}
		if active.State != CapacityActive || active.ProviderInstanceID != "vm-a" || !active.ProvisionedAt.Equal(reserved.CreatedAt) ||
			active.LastObservation == nil || capacityRefactorJSON(t, *active.LastObservation) != capacityRefactorJSON(t, present) ||
			active.UpdatedAt.Before(at) {
			t.Fatalf("active = %+v", active)
		}
		stored := capacityRefactorRawGet(t, repository, "op-a")
		if string(stored) != capacityRefactorJSON(t, active) {
			t.Fatalf("stored %s, returned %s", stored, capacityRefactorJSON(t, active))
		}
		replay, err := repository.ObserveCapacity("op-a", present)
		if err != nil || capacityRefactorJSON(t, replay) != string(stored) {
			t.Fatalf("replay = %+v, %v", replay, err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
		sameTime := present
		sameTime.State = "absent"
		sameTime.ProvisionedAt = time.Time{}
		_, err = repository.ObserveCapacity("op-a", sameTime)
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		older := capacityObservation(reserved, "absent", "", at.Add(-time.Nanosecond))
		_, err = repository.ObserveCapacity("op-a", older)
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		otherInstance := capacityObservation(reserved, "present", "vm-b", at.Add(time.Second))
		_, err = repository.ObserveCapacity("op-a", otherInstance)
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		capacityRefactorAssertStored(t, repository, "op-a", stored)

		// A later present keeps the first provisioning time.
		later := capacityObservation(reserved, "present", "vm-a", at.Add(2*time.Second))
		later.ProvisionedAt = at
		next, err := repository.ObserveCapacity("op-a", later)
		if err != nil || !next.ProvisionedAt.Equal(reserved.CreatedAt) || next.LastObservation.ProvisionedAt != at {
			t.Fatalf("later present = %+v, %v", next, err)
		}
		// Absent without an instance ID keeps the reservation's instance ID.
		absent, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "", at.Add(3*time.Second)))
		if err != nil || absent.State != CapacityAbsent || absent.ProviderInstanceID != "vm-a" || absent.LastObservation.ProviderInstanceID != "" {
			t.Fatalf("absent = %+v, %v", absent, err)
		}
		absentAgain, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "vm-a", at.Add(4*time.Second)))
		if err != nil || absentAgain.State != CapacityAbsent {
			t.Fatalf("absent again = %+v, %v", absentAgain, err)
		}
		releasedStored := capacityRefactorRawGet(t, repository, "op-a")
		_, err = repository.ObserveCapacity("op-a", capacityObservation(reserved, "destroyed", "", at.Add(5*time.Second)))
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		capacityRefactorAssertStored(t, repository, "op-a", releasedStored)
		// A released reservation may be seen present again.
		again, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "present", "vm-a", at.Add(6*time.Second)))
		if err != nil || again.State != CapacityActive {
			t.Fatalf("present after absent = %+v, %v", again, err)
		}
	})
	t.Run("destroyed then absent is a transition error", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		at := active.LastObservation.ObservedAt
		if _, err := repository.ObserveCapacity("op-a", capacityObservation(active, "destroyed", "vm-a", at.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		_, err := repository.ObserveCapacity("op-a", capacityObservation(active, "absent", "vm-a", at.Add(2*time.Second)))
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		// Instance mismatch is checked before the released-state rule.
		_, err = repository.ObserveCapacity("op-a", capacityObservation(active, "absent", "vm-b", at.Add(2*time.Second)))
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	})
	t.Run("observation from create outcome unknown", func(t *testing.T) {
		repository := openTestStore(t)
		reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
		if _, err := repository.MarkCapacityCreateSent("op-a"); err != nil {
			t.Fatal(err)
		}
		absent, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "", reserved.CreatedAt.Add(time.Second)))
		if err != nil || absent.State != CapacityAbsent || absent.ProviderInstanceID != "" {
			t.Fatalf("absent = %+v, %v", absent, err)
		}
	})
}

func TestCapacityRefactorObserveCapacityTxCharacterization(t *testing.T) {
	t.Run("not found and corrupt", func(t *testing.T) {
		repository := openTestStore(t)
		observation := CapacityProviderObservation{Provider: "vultr", ResourceKey: "k", State: "absent", ObservedAt: time.Now().UTC()}
		err := repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "missing", observation) })
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		capacityRefactorRawPut(t, repository, "bad", []byte("["))
		err = repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "bad", observation) })
		capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
	})
	t.Run("apply error and replay", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		stored := capacityRefactorRawGet(t, repository, "op-a")
		err := repository.db.Update(func(tx *bolt.Tx) error {
			return observeCapacityTx(tx, "op-a", capacityObservation(active, "present", "vm-b", active.LastObservation.ObservedAt.Add(time.Second)))
		})
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		err = repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "op-a", *active.LastObservation) })
		if err != nil {
			t.Fatal(err)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("future observation floors updated time", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		future := time.Now().UTC().Add(2 * time.Minute)
		observation := capacityObservation(active, "destroyed", "vm-a", future)
		if err := repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "op-a", observation) }); err != nil {
			t.Fatal(err)
		}
		loaded := capacityRefactorLoad(t, repository, "op-a")
		if loaded.State != CapacityDestroyed || !loaded.UpdatedAt.Equal(future) || loaded.ProviderInstanceID != "vm-a" ||
			capacityRefactorJSON(t, *loaded.LastObservation) != capacityRefactorJSON(t, observation) {
			t.Fatalf("destroyed = %+v", loaded)
		}
	})
	t.Run("future ready time is not a floor", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		readyAt := time.Now().UTC().Add(3 * time.Minute)
		ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt)
		if err != nil || !ready.UpdatedAt.Equal(readyAt) {
			t.Fatalf("ready = %+v, %v", ready, err)
		}
		stored := capacityRefactorRawGet(t, repository, "op-a")
		observation := capacityObservation(active, "present", "vm-a", active.LastObservation.ObservedAt.Add(time.Second))
		err = repository.db.Update(func(tx *bolt.Tx) error { return observeCapacityTx(tx, "op-a", observation) })
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid updated capacity reservation: "+capacityRefactorInvalid)
		capacityRefactorAssertStored(t, repository, "op-a", stored)
		// ObserveCapacity (updateCapacity) does floor on ReadyAt.
		observed, err := repository.ObserveCapacity("op-a", observation)
		if err != nil || !observed.UpdatedAt.Equal(readyAt) {
			t.Fatalf("observe = %+v, %v", observed, err)
		}
	})
}

func TestCapacityRefactorRecordCapacityReadyCharacterization(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		repository := openTestStore(t)
		now := time.Now().UTC()
		for name, call := range map[string][4]any{
			"server":   {"", "staging", "vm", now},
			"env":      {"srv", strings.Repeat("e", 201), "vm", now},
			"instance": {"srv", "staging", strings.Repeat("i", 241), now},
			"zero":     {"srv", "staging", "vm", time.Time{}},
			"future":   {"srv", "staging", "vm", now.Add(6 * time.Minute)},
		} {
			result, err := repository.RecordCapacityReady(call[0].(string), call[1].(string), call[2].(string), call[3].(time.Time))
			if result.OperationID != "" {
				t.Fatalf("%s result = %+v", name, result)
			}
			capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid readiness measurement")
		}
	})
	t.Run("not found returns zero", func(t *testing.T) {
		repository := openTestStore(t)
		capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		for _, call := range [][3]string{{"other", "staging", "vm-a"}, {"srv", "prod", "vm-a"}, {"srv", "staging", "vm-b"}} {
			result, err := repository.RecordCapacityReady(call[0], call[1], call[2], time.Now().UTC())
			capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
			if result.OperationID != "" {
				t.Fatalf("not found result = %+v", result)
			}
		}
	})
	t.Run("transition errors return the matched reservation", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		stored := capacityRefactorRawGet(t, repository, "op-a")
		result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", active.CreatedAt.Add(-time.Nanosecond))
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		if capacityRefactorJSON(t, result) != string(stored) {
			t.Fatalf("result = %+v", result)
		}
		destroying, err := repository.MarkCapacityDestroySent("op-a")
		if err != nil {
			t.Fatal(err)
		}
		result, err = repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		if capacityRefactorJSON(t, result) != capacityRefactorJSON(t, destroying) {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("ready before provisioned", func(t *testing.T) {
		repository := openTestStore(t)
		reserved, _, err := repository.ReserveCapacity(capacityRequest("op-a", "srv", "staging", "g"), 9, 9)
		if err != nil {
			t.Fatal(err)
		}
		provisioned := reserved.CreatedAt.Add(time.Second)
		observePresent(t, repository, reserved, "vm-a", provisioned.Add(time.Second), provisioned)
		result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", provisioned.Add(-time.Nanosecond))
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		if result.OperationID != "op-a" || !result.ReadyAt.IsZero() {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("adopted reservation", func(t *testing.T) {
		repository := openTestStore(t)
		evidence := adoptionEvidence()
		adopted, err := repository.AdoptCapacity(capacityRequest("op-adopt", "srv", "staging", "g"), evidence, 9, 9)
		if err != nil {
			t.Fatal(err)
		}
		result, err := repository.RecordCapacityReady("srv", "staging", evidence.InstanceID, time.Now().UTC())
		capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
		if capacityRefactorJSON(t, result) != capacityRefactorJSON(t, adopted) {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("records once in UTC", func(t *testing.T) {
		repository := openTestStore(t)
		active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		readyAt := active.LastObservation.ObservedAt.In(time.FixedZone("east", 7200))
		ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt)
		if err != nil {
			t.Fatal(err)
		}
		if ready.ReadyAt.Location() != time.UTC || !ready.ReadyAt.Equal(readyAt) || ready.UpdatedAt.Before(ready.ReadyAt) ||
			ready.State != CapacityActive || ready.UpdatedAt.Before(active.UpdatedAt) {
			t.Fatalf("ready = %+v", ready)
		}
		stored := capacityRefactorRawGet(t, repository, "op-a")
		if string(stored) != capacityRefactorJSON(t, ready) {
			t.Fatalf("stored %s, returned %+v", stored, ready)
		}
		again, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt.UTC())
		if err != nil || capacityRefactorJSON(t, again) != string(stored) {
			t.Fatalf("repeat = %+v, %v", again, err)
		}
		different, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt.Add(time.Second))
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		if capacityRefactorJSON(t, different) != string(stored) {
			t.Fatalf("conflict result = %+v", different)
		}
		capacityRefactorAssertStored(t, repository, "op-a", stored)
	})
	t.Run("future ready floors updated time", func(t *testing.T) {
		repository := openTestStore(t)
		capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		future := time.Now().UTC().Add(4 * time.Minute)
		ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", future)
		if err != nil || !ready.UpdatedAt.Equal(future) || !ready.ReadyAt.Equal(future) {
			t.Fatalf("ready = %+v, %v", ready, err)
		}
	})
	t.Run("duplicate match returns first by key", func(t *testing.T) {
		repository := openTestStore(t)
		first := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		if _, err := repository.ObserveCapacity("op-a", capacityObservation(first, "absent", "", first.LastObservation.ObservedAt.Add(time.Second))); err != nil {
			t.Fatal(err)
		}
		capacityRefactorActive(t, repository, "op-b", "srv", "vm-a")
		firstStored := capacityRefactorRawGet(t, repository, "op-a")
		result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		if capacityRefactorJSON(t, result) != string(firstStored) {
			t.Fatalf("conflict result = %+v", result)
		}
	})
	t.Run("corrupt entry after match returns match", func(t *testing.T) {
		repository := openTestStore(t)
		capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
		stored := capacityRefactorRawGet(t, repository, "op-a")
		capacityRefactorRawPut(t, repository, "op-z", []byte("nope"))
		result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
		capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
		if capacityRefactorJSON(t, result) != string(stored) {
			t.Fatalf("corrupt result = %+v", result)
		}
		mismatch := capacityRefactorLoad(t, repository, "op-a")
		capacityRefactorRawPut(t, repository, "op-z", []byte(capacityRefactorJSON(t, mismatch)))
		_, err = repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
		capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": capacity reservation key mismatch")
	})
	t.Run("adapter filter only in tx form", func(t *testing.T) {
		repository := openTestStore(t)
		request := capacityRequest("op-a", "srv", "staging", "g")
		request.ProviderAdapterID = "hosting.vultr"
		reserved := reserve(t, repository, request)
		observePresent(t, repository, reserved, "vm-a", reserved.CreatedAt.Add(time.Second), time.Time{})
		var result CapacityReservation
		err := repository.db.Update(func(tx *bolt.Tx) error {
			var err error
			result, err = recordCapacityReadyTx(tx, "srv", "staging", "vm-a", "hosting.other", time.Now().UTC())
			return err
		})
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		if result.OperationID != "" {
			t.Fatalf("result = %+v", result)
		}
		ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
		if err != nil || ready.ReadyAt.IsZero() {
			t.Fatalf("ready = %+v, %v", ready, err)
		}
	})
}

func TestCapacityRefactorGetAndListCharacterization(t *testing.T) {
	repository := openTestStore(t)
	list, err := repository.ListCapacityReservations()
	if err != nil || list != nil {
		t.Fatalf("empty list = %#v, %v", list, err)
	}
	_, err = repository.GetCapacityReservation("")
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
	_, err = repository.GetCapacityReservation("missing")
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range []struct {
		id     string
		offset time.Duration
	}{{"op-c", 0}, {"op-a", time.Minute}, {"op-b", 0}, {"op-0", 2 * time.Minute}} {
		reservation := capacityRequest(entry.id, "srv-"+entry.id, "staging", "g")
		reservation.State = CapacityReserved
		reservation.CreatedAt = created.Add(entry.offset)
		reservation.UpdatedAt = reservation.CreatedAt
		capacityRefactorPut(t, repository, reservation)
	}
	list, err = repository.ListCapacityReservations()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, reservation := range list {
		order = append(order, reservation.OperationID)
	}
	if strings.Join(order, ",") != "op-b,op-c,op-a,op-0" {
		t.Fatalf("order = %v", order)
	}
	loaded, err := repository.GetCapacityReservation("op-a")
	if err != nil || !loaded.CreatedAt.Equal(created.Add(time.Minute)) {
		t.Fatalf("get = %+v, %v", loaded, err)
	}

	capacityRefactorRawPut(t, repository, "op-alias", []byte(capacityRefactorJSON(t, loaded)))
	list, err = repository.ListCapacityReservations()
	capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": capacity reservation key mismatch")
	if list != nil {
		t.Fatalf("list on error = %#v", list)
	}
	// Get does not check the key.
	if alias, err := repository.GetCapacityReservation("op-alias"); err != nil || alias.OperationID != "op-a" {
		t.Fatalf("alias = %+v, %v", alias, err)
	}
	capacityRefactorRawPut(t, repository, "op-alias", []byte("{"))
	result, err := repository.GetCapacityReservation("op-alias")
	capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
	if !strings.HasPrefix(err.Error(), capacityRefactorCorrupt+": decode capacity reservation: ") || result.OperationID != "" {
		t.Fatalf("get corrupt = %+v, %v", result, err)
	}
	list, err = repository.ListCapacityReservations()
	capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
	if !strings.HasPrefix(err.Error(), capacityRefactorCorrupt+": decode capacity reservation: ") || list != nil {
		t.Fatalf("list corrupt = %#v, %v", list, err)
	}
}

func TestCapacityRefactorObservationConsistencyCharacterization(t *testing.T) {
	cases := map[string]struct {
		mutate func(*CapacityReservation)
		valid  bool
	}{
		"nil observation":          {func(r *CapacityReservation) { r.LastObservation = nil }, true},
		"provider":                 {func(r *CapacityReservation) { r.LastObservation.Provider = "other" }, false},
		"resource":                 {func(r *CapacityReservation) { r.LastObservation.ResourceKey = "other" }, false},
		"before created":           {func(r *CapacityReservation) { r.LastObservation.ObservedAt = r.CreatedAt.Add(-1) }, false},
		"present without instance": {func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "" }, false},
		"bad instance id":          {func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "a\x01" }, false},
		"instance mismatch":        {func(r *CapacityReservation) { r.LastObservation.ProviderInstanceID = "inst-2" }, false},
		"reservation without id":   {func(r *CapacityReservation) { r.State, r.ProviderInstanceID = CapacityCreateOutcomeUnknown, "" }, true},
		"provision on absent":      {func(r *CapacityReservation) { r.State, r.LastObservation.State = CapacityAbsent, "absent" }, false},
		"provision after observed": {func(r *CapacityReservation) { r.LastObservation.ProvisionedAt = r.LastObservation.ObservedAt.Add(1) }, false},
		"unknown state": {func(r *CapacityReservation) {
			r.LastObservation.State, r.LastObservation.ProvisionedAt = "gone", time.Time{}
		}, false},
		"absent matches absent": {func(r *CapacityReservation) {
			r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityAbsent, "absent", time.Time{}
		}, true},
		"absent with destroyed": {func(r *CapacityReservation) {
			r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityAbsent, "destroyed", time.Time{}
		}, false},
		"destroyed matches": {func(r *CapacityReservation) {
			r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityDestroyed, "destroyed", time.Time{}
		}, true},
		"destroyed with absent": {func(r *CapacityReservation) {
			r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityDestroyed, "absent", time.Time{}
		}, false},
		"active with absent": {func(r *CapacityReservation) {
			r.LastObservation.State, r.LastObservation.ProvisionedAt = "absent", time.Time{}
		}, false},
		"destroy unknown with absent": {func(r *CapacityReservation) {
			r.State, r.LastObservation.State, r.LastObservation.ProvisionedAt = CapacityDestroyOutcomeUnknown, "absent", time.Time{}
		}, true},
		"reserved with present": {func(r *CapacityReservation) { r.State = CapacityReserved }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			reservation := persistedActiveReservation()
			tc.mutate(&reservation)
			if got := capacityObservationConsistent(reservation); got != tc.valid {
				t.Fatalf("capacityObservationConsistent = %v, want %v", got, tc.valid)
			}
		})
	}
}
