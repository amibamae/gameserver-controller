package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/boottiming"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func showBootStatus(serverID, environmentID, statePath string, now time.Time, stdout io.Writer) error {
	if !safePilotServerID.MatchString(serverID) || !validBootEnvironment(environmentID) || now.IsZero() {
		return errors.New("a valid server ID, environment, and current time are required")
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return err
	}
	// Status reports read errors independently of store cleanup.
	defer func() { _ = repository.Close() }()
	reservations, err := repository.ListCapacityReservations()
	if err != nil {
		return err
	}
	current, durations := collectBootSamples(reservations, serverID, environmentID)
	writeBootStatus(stdout, current, durations, bootTarget{serverID, environmentID}, now)
	return nil
}

func validBootEnvironment(environmentID string) bool {
	return environmentID == "production" || environmentID == "staging" || environmentID == "testing"
}

func collectBootSamples(reservations []store.CapacityReservation, serverID, environmentID string) (*store.CapacityReservation, []time.Duration) {
	var current *store.CapacityReservation
	var durations []time.Duration
	for i := range reservations {
		reservation := &reservations[i]
		if reservation.ServerID != serverID || reservation.EnvironmentID != environmentID {
			continue
		}
		if !reservation.ProvisionedAt.IsZero() && !reservation.ReadyAt.IsZero() {
			if duration := reservation.ReadyAt.Sub(reservation.ProvisionedAt); duration > 0 {
				durations = append(durations, duration)
			}
		}
		if reservation.State != store.CapacityAbsent && reservation.State != store.CapacityDestroyed {
			current = reservation
		}
	}
	return current, durations
}

type bootTarget struct {
	serverID, environmentID string
}

func writeBootStatus(stdout io.Writer, current *store.CapacityReservation, durations []time.Duration, target bootTarget, now time.Time) {
	if current == nil {
		fmt.Fprintf(stdout, "No controller-owned VM is currently recorded for %s@%s.\n", target.serverID, target.environmentID)
		writeHistoricalBootEstimate(stdout, durations)
		return
	}
	if current.State != store.CapacityActive {
		if !current.ReadyAt.IsZero() {
			fmt.Fprintf(stdout, "Paper readiness was previously confirmed at %s.\n", current.ReadyAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(stdout, "Managed VM state: %s. Current Paper readiness is not confirmed; boot progress is indeterminate.\n", current.State)
		writeHistoricalBootEstimate(stdout, durations)
		return
	}
	if !current.ReadyAt.IsZero() {
		fmt.Fprintf(stdout, "Paper readiness was confirmed at %s.\n", current.ReadyAt.UTC().Format(time.RFC3339))
		if !current.ProvisionedAt.IsZero() {
			fmt.Fprintf(stdout, "Measured provision-to-Paper-ready time: %s.\n", current.ReadyAt.Sub(current.ProvisionedAt).Round(time.Second))
		}
		fmt.Fprintln(stdout, "Progress: [####################] 100% (initial boot completed)")
		return
	}
	if current.ProvisionedAt.IsZero() {
		fmt.Fprintln(stdout, "Provider presence is confirmed, but no provisioning timestamp is available; Paper boot progress is indeterminate.")
		writeHistoricalBootEstimate(stdout, durations)
		return
	}
	writePendingBootStatus(stdout, durations, current.ProvisionedAt, now)
}

func writePendingBootStatus(stdout io.Writer, durations []time.Duration, provisionedAt, now time.Time) {
	elapsed := now.Sub(provisionedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	fmt.Fprintf(stdout, "Paper readiness is pending. Time since provisioning: %s.\n", elapsed.Round(time.Second))
	if len(durations) < 3 {
		fmt.Fprintf(stdout, "Estimate and progress are indeterminate (%d completed boot sample(s); 3 required).\n", len(durations))
		return
	}
	estimate, err := boottiming.Estimate(durations, boottiming.DefaultSafetyFactor)
	if err != nil {
		fmt.Fprintf(stdout, "Estimate and progress are indeterminate (%d completed boot sample(s)).\n", len(durations))
		return
	}
	remaining := estimate - elapsed
	if remaining < 0 {
		remaining = 0
	}
	percent := boottiming.ProgressPercent(elapsed, estimate, false)
	fmt.Fprintf(stdout, "Estimated total: %s; estimated time remaining: %s.\n", estimate.Round(time.Second), remaining.Round(time.Second))
	fmt.Fprintf(stdout, "Progress: %s (advisory; never reaches 100%% before Paper readiness).\n", bootProgressBar(percent))
	return
}

func writeHistoricalBootEstimate(stdout io.Writer, durations []time.Duration) {
	if len(durations) < 3 {
		fmt.Fprintf(stdout, "Historical boot estimate unavailable (%d completed boot sample(s); 3 required).\n", len(durations))
		return
	}
	estimate, err := boottiming.Estimate(durations, boottiming.DefaultSafetyFactor)
	if err != nil {
		fmt.Fprintf(stdout, "Historical boot estimate unavailable (%d completed boot sample(s)).\n", len(durations))
		return
	}
	fmt.Fprintf(stdout, "Historical boot estimate: %s (75th percentile plus 25%% buffer; %d samples).\n", estimate.Round(time.Second), len(durations))
}

func bootProgressBar(percent int) string {
	const width = 20
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	filled := percent * width / 100
	return fmt.Sprintf("[%s%s] %d%%", strings.Repeat("#", filled), strings.Repeat("-", width-filled), percent)
}
