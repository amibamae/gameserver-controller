package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/amibamae/gameserver-controller/internal/pilothandoff"
)

// validateAutoObservabilityFlags makes both collectors strictly opt-in.
// Unrelated handoffs never read a log credential or contact an ingestion host.
func validateAutoObservabilityFlags(flags *flag.FlagSet, opts handoffOptions) error {
	if !opts.AutoVictoriaLogs {
		if flagSet(flags, "logs-endpoint") || flagSet(flags, "logs-privacy") || flagSet(flags, "logs-credential-ref") {
			return errors.New("VictoriaLogs handoff options require --enable-victorialogs")
		}
		return nil
	}
	if opts.LogsPrivacyMode != "lifecycle" {
		return errors.New("automatic VictoriaLogs handoff supports lifecycle privacy only")
	}
	if !pilothandoff.ValidVictoriaLogsEndpoint(opts.LogsEndpoint) {
		return errors.New("automatic VictoriaLogs endpoint must be HTTPS /insert/jsonline")
	}
	return validateObservabilityOptions(autoObservabilityOptions(opts))
}

func autoObservabilityOptions(opts handoffOptions) observabilityOptions {
	return observabilityOptions{
		Execute: true, Confirmation: opts.Confirmation, OperationID: opts.OperationID,
		VultrCredentialRef: opts.CredentialRef, SSHCredentialRef: opts.SSHCredentialRef,
		LogCredentialRef: opts.LogsCredentialRef, Endpoint: opts.LogsEndpoint,
		PrivacyMode: "lifecycle", Timeout: min(opts.Timeout, 2*time.Minute),
	}
}

// applyAutomaticObservability happens only after the validated, authenticated
// SSH handoff and Wasabi restore. A logging failure cannot block gameplay.
func applyAutomaticObservability(opts handoffOptions, statePath string, stdout, stderr io.Writer,
	apply func(observabilityOptions, string, io.Writer) error) {
	if !opts.Execute || !opts.AutoVictoriaLogs {
		return
	}
	if err := apply(autoObservabilityOptions(opts), statePath, stdout); err != nil {
		// Never include the underlying error: it may contain remote details.
		fmt.Fprintln(stderr, "Warning: optional VictoriaLogs setup failed; Minecraft handoff succeeded. Retry with gsc-pilot observability.")
	}
}
