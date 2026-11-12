package pilotvultr

import (
	"context"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestProvisionCancelledBeforeProviderOrReservation(t *testing.T) {
	provider := successfulProvider()
	provisioner, repository, _ := newTestProvisioner(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	input := request("abcdef0123456789abcdef0123456789", "pilot-cancel", "staging")
	_, err := provisioner.Provision(ctx, input)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cancelled provision error = %v, want invalid request", err)
	}
	if _, err := repository.GetCapacityReservation(input.OperationID); !errors.Is(err, store.ErrCapacityNotFound) {
		t.Fatalf("cancelled request created a reservation: %v", err)
	}
	created, _, _ := provider.counts()
	if created != 0 {
		t.Fatalf("cancelled request dispatched %d creates", created)
	}
}
