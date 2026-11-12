package store

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
)

func newHandoff(t *testing.T) hosthandoff.Handoff {
	t.Helper()
	public, _, err := sshkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return hosthandoff.New(public, time.Now(), 0)
}

func TestClaimStoresTheKeyOfTheCallerThatWins(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-handoff", "server-handoff", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	keys := make([]hosthandoff.Handoff, callers)
	winners := make(chan int, callers)
	var wait sync.WaitGroup
	for i := range callers {
		keys[i] = newHandoff(t)
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.ClaimCapacityCreateWithHandoff(reservation.OperationID, keys[i]); err == nil {
				winners <- i
			} else if !errors.Is(err, ErrCapacityTransition) {
				t.Errorf("claim %d: %v", i, err)
			}
		}()
	}
	wait.Wait()
	close(winners)
	var won []int
	for i := range winners {
		won = append(won, i)
	}
	if len(won) != 1 {
		t.Fatalf("winners = %v", won)
	}
	record, err := store.GetHostHandoff(reservation.OperationID)
	if err != nil || record.OneTimeHostKey != keys[won[0]].OneTimeHostKey {
		t.Fatalf("stored key is not the winner's: %v", err)
	}
	current, _ := store.GetCapacityReservation(reservation.OperationID)
	if current.State != CapacityCreateOutcomeUnknown {
		t.Fatalf("state = %s", current.State)
	}
}

func TestHostHandoffUpdatesAreMonotonic(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-mono", "server-mono", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	handoff := newHandoff(t)
	if _, err := store.ClaimCapacityCreateWithHandoff(reservation.OperationID, handoff); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateHostHandoff(reservation.OperationID, func(r *HostHandoffRecord) error {
		r.CredentialsSent = true
		return nil
	}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("credentials before pinning: %v", err)
	}
	pinned, _, _ := sshkey.Generate()
	record, err := store.UpdateHostHandoff(reservation.OperationID, func(r *HostHandoffRecord) error {
		r.InstanceID = "instance-1"
		if r.ConnectOneTime(r.OneTimeHostKey, time.Now()) != hosthandoff.Permitted || r.Rotate(sshkey.AuthorizedKey(pinned)+"\n") != hosthandoff.Pinned {
			t.Fatal("rotation refused")
		}
		r.CredentialsSent = true
		return nil
	})
	if err != nil || record.State != hosthandoff.Rotated {
		t.Fatalf("rotate = %+v, %v", record, err)
	}
	other, _, _ := sshkey.Generate()
	for name, change := range map[string]func(*HostHandoffRecord){
		"unpin":           func(r *HostHandoffRecord) { r.State = hosthandoff.PendingRotation; r.PinnedHostKey = "" },
		"repin":           func(r *HostHandoffRecord) { r.PinnedHostKey = sshkey.AuthorizedKey(other) },
		"change instance": func(r *HostHandoffRecord) { r.InstanceID = "instance-2" },
		"forget progress": func(r *HostHandoffRecord) { r.CredentialsSent = false },
		"change one-time": func(r *HostHandoffRecord) { r.OneTimeHostKey = sshkey.AuthorizedKey(other) },
		"extend lifetime": func(r *HostHandoffRecord) { r.ExpiresAt = r.ExpiresAt.Add(time.Hour) },
	} {
		if _, err := store.UpdateHostHandoff(reservation.OperationID, func(r *HostHandoffRecord) error { change(r); return nil }); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := store.GetHostHandoff("op-missing"); !errors.Is(err, ErrHostHandoffNotFound) {
		t.Fatalf("missing record: %v", err)
	}
}
