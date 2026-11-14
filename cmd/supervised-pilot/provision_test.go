package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProvisionValidationPrecedesExecution(t *testing.T) {
	const badController = "--firewall-group-id and an exact --controller-ip address (not a range) are required so SSH is reachable only from the controller"
	tests := []struct {
		name  string
		flags []string
		want  string
	}{
		{"invalid timeout precedes invalid target", []string{"--timeout", "0s", "--server-id", "Bad"}, "--timeout must be greater than zero and no more than 24h"},
		{"invalid access precedes invalid provider fields", []string{"--controller-ip", "203.0.113.0/24", "--os-id", "0"}, badController},
		{"invalid provider fields precede confirmation", []string{"--hostname", "bad/host", "--execute"}, "region, plan, hostname, SSH key ID, or firewall group ID is invalid"},
		{"dry-run confirmation precedes operation ID", []string{"--confirm-create", "pilot-test@staging", "--operation-id", "bad"}, "--confirm-create requires --execute"},
		{"execution confirmation precedes operation ID", []string{"--execute", "--confirm-create", "wrong", "--operation-id", "bad"}, "Vultr create not started: --confirm-create must exactly match \"pilot-test@staging\""},
		{"invalid operation ID precedes audit", []string{"--execute", "--confirm-create", "pilot-test@staging", "--operation-id", "bad"}, "--operation-id must be 32 lowercase hexadecimal characters"},
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "pilot.db")
			out, err := runPilotWithState(t, statePath, append(provisionArgs(), test.flags...)...)
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if out != "" {
				t.Fatalf("unexpected output: %q", out)
			}
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Fatalf("state database stat = %v, want not exists", err)
			}
		})
	}
}
