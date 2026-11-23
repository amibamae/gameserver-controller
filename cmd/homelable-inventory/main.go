// homelable-inventory exports an operator-invoked, privacy-minimal view of
// locally recorded provider state. Nothing is sent over the network.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var designID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type lifecycleNode struct {
	OperationID string `json:"operation_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	State       string `json:"state"`
}

type lifecycleSnapshot struct {
	DesignID string          `json:"design_id"`
	Nodes    []lifecycleNode `json:"nodes"`
}

func snapshot(design string, reservations []store.CapacityReservation) lifecycleSnapshot {
	result := lifecycleSnapshot{DesignID: design, Nodes: []lifecycleNode{}}
	for _, r := range reservations {
		state := capacityLifecycleState(r)
		if state == "" {
			continue
		}
		name := fmt.Sprintf("%s @ %s (%s)", r.ServerID, r.EnvironmentID, r.OperationID[:8])
		result.Nodes = append(result.Nodes, lifecycleNode{
			OperationID: r.OperationID, Name: name, Type: "vm", State: state,
		})
	}
	return result
}

// Uncertain destruction must never be represented as a retired VM.
func capacityLifecycleState(r store.CapacityReservation) string {
	if r.ProviderInstanceID == "" || len(r.OperationID) < 8 {
		return ""
	}
	switch r.State {
	case store.CapacityActive, store.CapacityDestroyOutcomeUnknown:
		return "active"
	case store.CapacityAbsent, store.CapacityDestroyed:
		return "retired"
	default:
		return ""
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("homelable-inventory", flag.ContinueOnError)
	path := flags.String("state-db", "", "existing local pilot database")
	design := flags.String("design-id", "", "existing Homelable design ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || !designID.MatchString(*design) {
		return errors.New("--state-db and a valid --design-id are required")
	}
	if _, err := os.Stat(*path); err != nil {
		return errors.New("pilot database does not exist")
	}
	db, err := store.OpenBolt(*path)
	if err != nil {
		return errors.New("could not read pilot database")
	}
	defer db.Close()
	reservations, err := db.ListCapacityReservations()
	if err != nil {
		return errors.New("could not read capacity reservations")
	}
	return json.NewEncoder(stdout).Encode(snapshot(*design, reservations))
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "homelable inventory export:", err)
		os.Exit(1)
	}
}
