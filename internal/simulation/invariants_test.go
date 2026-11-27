package simulation

import (
	"errors"
	"slices"
	"testing"
)

func TestInvariantsCheckRunsInOrderAndNamesFirstFailure(t *testing.T) {
	var v Invariants
	if err := v.Check("empty"); err != nil {
		t.Fatalf("Check with no invariants = %v", err)
	}
	var ran []string
	broken := errors.New("two leases for one server")
	register := func(name string, err error) {
		t.Helper()
		if regErr := v.Register(name, func() error { ran = append(ran, name); return err }); regErr != nil {
			t.Fatalf("Register(%q): %v", name, regErr)
		}
	}
	register("backup-before-destroy", nil)
	register("single-lease", broken)
	register("never-reached", errors.New("unused"))

	err := v.Check("provision")
	want := `simulation: invariant "single-lease" failed after step "provision": two leases for one server`
	if errText(err) != want {
		t.Fatalf("Check = %q, want %q", errText(err), want)
	}
	var ie *InvariantError
	if !errors.As(err, &ie) || ie.Invariant != "single-lease" || ie.Step != "provision" {
		t.Fatalf("Check = %#v, want *InvariantError naming single-lease", err)
	}
	if !errors.Is(err, broken) {
		t.Fatal("InvariantError does not unwrap to the check error")
	}
	if !slices.Equal(ran, []string{"backup-before-destroy", "single-lease"}) {
		t.Fatalf("ran = %v", ran)
	}
}

func TestInvariantsRegisterRejectsInvalidInvariants(t *testing.T) {
	var v Invariants
	ok := func() error { return nil }
	if err := v.Register("held", ok); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cases := []struct {
		name  string
		check func() error
		want  string
	}{
		{"", ok, "simulation: invariant name is empty"},
		{"nil-check", nil, `simulation: invariant "nil-check" has no check`},
		{"held", ok, `simulation: invariant "held" is already registered`},
	}
	for _, tc := range cases {
		if got := errText(v.Register(tc.name, tc.check)); got != tc.want {
			t.Errorf("Register(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if len(v.list) != 1 {
		t.Fatalf("registered %d invariants, want 1", len(v.list))
	}
}
