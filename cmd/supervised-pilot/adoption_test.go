package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func adoptionArgs() []string {
	return []string{"adopt", "--server-id", "example-server", "--environment", "production", "--instance-id", "example-instance",
		"--expected-ip", "203.0.113.10", "--region", "example-region", "--plan", "example-plan", "--os-id", "123", "--os-name", "Example Linux"}
}

func TestAdoptionDryRunDoesNotReadCredentialsOrWriteCapacity(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", filepath.Join(t.TempDir(), "missing-credentials"))
	statePath := filepath.Join(t.TempDir(), "state.db")
	output, err := runPilotWithState(t, statePath, adoptionArgs()...)
	if err != nil || !strings.Contains(output, "Dry run: no provider API call, credential read, or capacity change") || !strings.Contains(output, "example-server@production/example-instance") {
		t.Fatalf("dry run output=%q error=%v", output, err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("dry run created state", err)
	}
}

func TestAdoptionRequiresExactConfirmationBeforeLocalChanges(t *testing.T) {
	for _, extra := range [][]string{
		{"--execute"},
		{"--execute", "--confirm-adopt", "example-server@production/other-instance"},
		{"--confirm-adopt", "example-server@production/example-instance"},
		{"--execute", "--confirm-adopt", "example-server@production/example-instance", "--operation-id", "invalid"},
		{"--confirm-create", "example-server@production"},
		{"--state-db", "alternate.db"},
		{"--capacity-policy", "alternate.json"},
	} {
		statePath := filepath.Join(t.TempDir(), "state.db")
		args := append(adoptionArgs(), extra...)
		if _, err := runPilotWithState(t, statePath, args...); err == nil {
			t.Fatalf("accepted %v", extra)
		}
		if _, err := os.Stat(statePath); !os.IsNotExist(err) {
			t.Fatal("rejected adoption created state", err)
		}
	}
}

func TestAdoptionHistoryAcceptsOnlyAdoptionEventsAndService(t *testing.T) {
	record := auditRecord{OperationID: strings.Repeat("a", 32), Action: "adopt", Target: "example-server@production", Service: "vultr.instance.adopt", Event: "adoption_confirmed"}
	if !validAuditRecord(record) {
		t.Fatal("adoption history rejected")
	}
	record.Service = "vultr.instance.create"
	if validAuditRecord(record) {
		t.Fatal("adoption accepted create service")
	}
	record.Service = "vultr.instance.adopt"
	record.Event = "provision_confirmed"
	if validAuditRecord(record) {
		t.Fatal("adoption accepted create event")
	}
}
