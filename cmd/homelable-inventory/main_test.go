package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestSnapshotUsesConfirmedLifecycleStateOnly(t *testing.T) {
	base := store.CapacityReservation{
		ServerID: "vanilla", EnvironmentID: "staging",
		ProviderInstanceID: "instance-1", OperationID: strings.Repeat("a", 32),
	}
	active := base
	active.State = store.CapacityActive
	unknown := base
	unknown.State = store.CapacityDestroyOutcomeUnknown
	absent := base
	absent.State = store.CapacityAbsent
	creating := base
	creating.State = store.CapacityCreateOutcomeUnknown
	creating.ProviderInstanceID = ""
	nodes := snapshot("canvas", []store.CapacityReservation{active, unknown, absent, creating}).Nodes
	if len(nodes) != 3 || nodes[0].State != "active" || nodes[1].State != "active" || nodes[2].State != "retired" {
		t.Fatalf("unsafe snapshot: %+v", nodes)
	}
	for _, node := range nodes {
		if node.OperationID != base.OperationID || node.Type != "vm" || strings.Contains(node.Name, "instance-1") {
			t.Fatalf("snapshot leaked resource details: %+v", node)
		}
	}
}

func TestInventoryDefaultsToNoAccess(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"--state-db", "/nonexistent/gsc.db", "--design-id", "canvas"}, &out)
	if err == nil || err.Error() != "pilot database does not exist" || out.Len() != 0 {
		t.Fatalf("unexpected database access result: %v %q", err, out.String())
	}
}

func TestLongServerIdentifierFitsHomelableLabel(t *testing.T) {
	reservation := store.CapacityReservation{
		ServerID: strings.Repeat("x", 64), EnvironmentID: "production",
		ProviderInstanceID: "instance-1", OperationID: strings.Repeat("a", 32),
		State: store.CapacityActive,
	}
	name := snapshot("canvas", []store.CapacityReservation{reservation}).Nodes[0].Name
	if len(name) > 74 || !strings.HasSuffix(name, "(aaaaaaaa)") {
		t.Fatalf("long Homelable name loses identity: %q", name)
	}
}
