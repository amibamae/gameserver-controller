package main

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func showCapacity(policy pilotcapacity.Policy, statePath string, stdout io.Writer) error {
	if !policy.RuntimeEnforced {
		return errors.New("capacity policy is not marked for runtime enforcement")
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return err
	}
	// The report returns errors from reading capacity independently of store cleanup.
	defer func() { _ = repository.Close() }()
	reservations, err := repository.ListCapacityReservations()
	if err != nil {
		return err
	}
	writeCapacityTotals(stdout, policy, reservations)
	writeCapacityReservations(stdout, reservations)
	return nil
}

func writeCapacityTotals(stdout io.Writer, policy pilotcapacity.Policy, reservations []store.CapacityReservation) {
	occupied, groupCounts := 0, make(map[string]int)
	for _, reservation := range reservations {
		if policy.ReleasedStates[string(reservation.State)] {
			continue
		}
		occupied++
		groupCounts[reservation.CapacityGroup]++
	}
	fmt.Fprintf(stdout, "Managed VM capacity: %d/%d total.\n", occupied, policy.MaxInstancesTotal)
	groupIDs := make([]string, 0, len(policy.CapacityGroups))
	for groupID := range policy.CapacityGroups {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		group := policy.CapacityGroups[groupID]
		fmt.Fprintf(stdout, "%s: %d/%d slot(s)\n", groupID, groupCounts[groupID], group.MaxInstances)
	}
}

func writeCapacityReservations(stdout io.Writer, reservations []store.CapacityReservation) {
	if len(reservations) == 0 {
		fmt.Fprintln(stdout, "No managed VM reservations are recorded.")
		return
	}
	fmt.Fprintln(stdout, "OPERATION_ID\tSERVER\tENVIRONMENT\tSTATE\tPROVIDER_INSTANCE_ID\tRESOURCE_LABEL")
	for _, reservation := range reservations {
		instanceID := reservation.ProviderInstanceID
		if instanceID == "" {
			instanceID = "-"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", reservation.OperationID, reservation.ServerID, reservation.EnvironmentID, reservation.State, instanceID, reservation.ResourceKey)
	}
}
