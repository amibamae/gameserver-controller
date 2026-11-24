package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// Help and unknown-action validation need no shell, database or credentials.
func TestPilotObservabilityCLIContract(t *testing.T) {
	var stderr bytes.Buffer
	err := runWithSettings([]string{"destroy"}, io.Discard, &stderr, runtimeSettings{})
	want := `unknown action "destroy"; expected status, backup, history, boot-status, provision, adopt, handoff, observability, register-ssh-key, or capacity`
	if err == nil || err.Error() != want {
		t.Fatalf("unknown action = %v; want %q", err, want)
	}
	err = runWithSettings([]string{"status", "-h"}, io.Discard, &stderr, runtimeSettings{})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help error = %v; want flag.ErrHelp", err)
	}
	pilotR3CheckUsageFlags(t, stderr.String())
}

func pilotR3CheckUsageFlags(t *testing.T, stderr string) {
	t.Helper()
	for _, line := range []string{
		"  -confirm-create string\n    \trepeat <server-id>@<environment> to approve billable Vultr VM creation\n",
		"  -credential-ref string\n    \tsystemd credential reference for the Vultr API key (default \"systemd-credential://vultr-pilot-api-token\")\n",
		"  -ssh-key-id value\n    \tVultr SSH key ID; repeat for multiple keys (required for provisioning)\n",
		"  -timeout duration\n    \tmaximum time to wait for the selected operation (default 1h0m0s)\n",
		"  -execute\n    \texecute the selected action; mutating actions are dry runs by default\n",
		"  -confirm-observability string\n    \trepeat <server-id>@<environment>/<instance-id> to authorize private log settings\n",
		"  -enable-victorialogs\n    \topt in to configure VictoriaLogs during a successful SSH handoff (off by default)\n",
		"  -logs-endpoint string\n    \tHTTPS ingestion reverse proxy with /insert/jsonline path\n",
		"  -logs-privacy string\n    \tlog privacy mode: lifecycle or disabled (never raw player logs) (default \"lifecycle\")\n",
		"  -logs-credential-ref string\n    \tsystemd credential reference for VictoriaLogs ingestion only (default \"systemd-credential://gsc-victorialogs-ingest-token\")\n",
	} {
		if !strings.Contains(stderr, line) {
			t.Fatalf("help %q lacks %q", stderr, line)
		}
	}
	if count := strings.Count(stderr, "\n  -"); count != 34 {
		t.Fatalf("help lists %d flags", count)
	}
}
