package pilotvultr

import (
	"testing"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
)

func TestPreparePreservesCreateIdentityAndErrorOrder(t *testing.T) {
	p := &Provisioner{policy: testPolicy()}
	input := defaultRequest()
	create, reservation, err := p.prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	const wantLabel = "gsc-managed-0123456789abcdef0123456789abcdef-1e80fbcd6054bc05108f6297a35fb6ef7ddf1a093236a46144a184dc20f7d594"
	if create.Label != wantLabel || reservation.ResourceKey != wantLabel || create.OSID != 362 {
		t.Fatalf("create label %q, reservation key %q, OS ID %d", create.Label, reservation.ResourceKey, create.OSID)
	}

	input.OSID = "invalid"
	input.Environment = "unsupported"
	if _, _, err := p.prepare(input); err != pilotcapacity.ErrUnsupportedEnvironment {
		t.Fatalf("prepare for unknown environment and bad OS ID = %v", err)
	}
	input.OperationID = ""
	if _, _, err := p.prepare(input); err != ErrInvalidRequest {
		t.Fatalf("prepare for missing operation ID and unknown environment = %v", err)
	}
}
