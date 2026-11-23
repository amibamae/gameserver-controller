package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestObservabilityRefusesRawLogsBeforeAccessingCredentials(t *testing.T) {
	for _, tc := range []struct {
		privacy, endpoint, want string
	}{
		{"raw", "https://logs.example/insert/jsonline", "--logs-privacy must be lifecycle or disabled"},
		{"lifecycle", "", "--logs-endpoint is required for lifecycle log export"},
		{"disabled", "https://logs.example/insert/jsonline", "disabled log export must not have an endpoint"},
	} {
		err := runObservability(observabilityOptions{
			OperationID: "0123456789abcdef0123456789abcdef",
			PrivacyMode: tc.privacy, Endpoint: tc.endpoint,
			VultrCredentialRef: defaultVultrCredential,
			SSHCredentialRef: defaultSSHCredential,
			LogCredentialRef: "systemd-credential://gsc-victorialogs-ingest-token",
			Timeout: time.Minute,
		}, "/nonexistent-pilot-state.db", io.Discard)
		if err == nil || err.Error() != tc.want {
			t.Fatalf("privacy %q: error = %v, want %q", tc.privacy, err, tc.want)
		}
	}
}

func TestObservabilityAuditOnlyAcceptsExactInstanceTarget(t *testing.T) {
	record := auditRecord{OperationID: "0123456789abcdef0123456789abcdef",
		Action: "observability", Event: "observability_completed",
		Target: "pilot-test@staging/instance-9", Service: "vm.ssh.observability"}
	if !validAuditRecord(record) {
		t.Fatal("rejected valid observability audit record")
	}
	for _, target := range []string{"pilot-test@staging", "pilot-test@staging/instance-9;whoami", "pilot-test@production@staging/instance-9"} {
		record.Target = target
		if validAuditRecord(record) {
			t.Fatalf("accepted malformed target %q", target)
		}
	}
	record.Target = "pilot-test@staging/instance-9"
	record.Event = "provision_confirmed"
	if validAuditRecord(record) {
		t.Fatal("accepted event from another action")
	}
}

func TestObservabilityActionListed(t *testing.T) {
	if !knownAction("observability") ||
		!strings.Contains(unknownActionError("invalid").Error(), "observability") {
		t.Fatal("observability must appear in the supported command catalog")
	}
}
