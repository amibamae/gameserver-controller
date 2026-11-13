package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// provisioningLogicalServer returns a valid logical server that one recorded
// event moved from unprovisioned to provisioning.
func provisioningLogicalServer() LogicalServer {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return LogicalServer{
		ServerID: "server-1", EnvironmentID: "staging", DisplayName: "Test Server",
		HostingProfile: "vultr-small", StorageProfile: "wasabi-worlds", GameAdapterID: "game.paper",
		ReleaseChannel: "stable", ExecutionAdapterID: "execution.systemd", ExecutionProfile: "paper-default",
		Definition: json.RawMessage(`{"server_id":"server-1"}`), LifecycleState: protocolstate.ServerProvisioning,
		DataGenerationStatus: "unknown", LastOperationID: "provision-1", CreatedAt: created, UpdatedAt: created.Add(10 * time.Minute),
		LifecycleEvents: []LogicalServerStateEvent{{
			Sequence: 1, OperationID: "provision-1", ActorID: "operator-1", OperationType: "server.provision",
			FromState: protocolstate.ServerUnprovisioned, ToState: protocolstate.ServerProvisioning, Cause: "operator request",
			RequestFingerprint: "sha256:" + strings.Repeat("a", 64), EvidenceStepIDs: []string{"reserve"}, OccurredAt: created.Add(time.Minute),
		}},
	}
}

func TestValidatePersistedLogicalServerRules(t *testing.T) {
	if err := validatePersistedLogicalServer(provisioningLogicalServer()); err != nil {
		t.Fatalf("valid logical server rejected: %v", err)
	}
	tests := []struct {
		name    string
		mutate  func(*LogicalServer)
		message string
	}{
		{"missing server ID", func(s *LogicalServer) { s.ServerID = "" }, "invalid logical server record"},
		{"missing execution profile", func(s *LogicalServer) { s.ExecutionProfile = "" }, "invalid logical server record"},
		{"oversized network profile", func(s *LogicalServer) { s.NetworkProfile = strings.Repeat("n", 201) }, "invalid logical server record"},
		{"definition is not an object", func(s *LogicalServer) { s.Definition = json.RawMessage(`[]`) }, "invalid logical server record"},
		{"no creation time", func(s *LogicalServer) { s.CreatedAt = time.Time{} }, "invalid logical server record"},
		{"updated before created", func(s *LogicalServer) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) }, "invalid logical server record"},
		{"unknown lifecycle state", func(s *LogicalServer) { s.LifecycleState = "paused" }, "invalid logical server lifecycle state"},
		{"unknown desired state", func(s *LogicalServer) { s.DesiredState = "parked" }, "invalid logical server desired state"},
		{"unknown data generation status", func(s *LogicalServer) { s.DataGenerationStatus = "maybe" }, "invalid logical server data generation status"},
		{"known generation without an ID", func(s *LogicalServer) { s.DataGenerationStatus = "known" }, "known data generation is missing its ID"},
		{"instance reference is not an object", func(s *LogicalServer) { s.InstanceRef = json.RawMessage(`"instance-1"`) }, "invalid logical server instance reference"},
		{"state changed without an event", func(s *LogicalServer) { s.LifecycleEvents = nil }, "lifecycle state changed without a durable state event"},
		{"event out of sequence", func(s *LogicalServer) { s.LifecycleEvents[0].Sequence = 2 }, "invalid logical server lifecycle event 1"},
		{"event after the last update", func(s *LogicalServer) { s.LifecycleEvents[0].OccurredAt = s.UpdatedAt.Add(time.Second) }, "invalid logical server lifecycle event 1"},
		{"event with a disallowed transition", func(s *LogicalServer) {
			s.LifecycleEvents[0].ToState, s.LifecycleState = protocolstate.ServerReady, protocolstate.ServerReady
		}, "invalid logical server lifecycle event 1 transition"},
		{"repeated evidence step", func(s *LogicalServer) { s.LifecycleEvents[0].EvidenceStepIDs = []string{"reserve", "reserve"} }, "duplicate lifecycle evidence step ID"},
		{"history ends in another state", func(s *LogicalServer) { s.LifecycleState = protocolstate.ServerFailed }, "lifecycle event history does not match current state"},
		{"last operation not in the history", func(s *LogicalServer) { s.LastOperationID = "other-operation" }, "last operation does not match lifecycle history"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := provisioningLogicalServer()
			test.mutate(&server)
			err := validatePersistedLogicalServer(server)
			if !errors.Is(err, ErrInvalidRecord) || !strings.HasSuffix(err.Error(), test.message) {
				t.Fatalf("validatePersistedLogicalServer error = %v, want ErrInvalidRecord ending %q", err, test.message)
			}
		})
	}
}

func TestValidatePersistedLogicalServerAcceptsUnprovisionedWithoutEvents(t *testing.T) {
	server := provisioningLogicalServer()
	server.LifecycleState, server.LifecycleEvents, server.LastOperationID = protocolstate.ServerUnprovisioned, nil, "define-1"
	server.DataGenerationStatus, server.DataGenerationID, server.DesiredState = "known", "generation-1", "stopped"
	if err := validatePersistedLogicalServer(server); err != nil {
		t.Fatalf("valid unprovisioned server rejected: %v", err)
	}
}

func TestValidatePersistedLogicalServerRequiresLastOperationID(t *testing.T) {
	server := provisioningLogicalServer()
	// Without events, only the field rule can reject a missing operation ID.
	server.LifecycleState, server.LifecycleEvents, server.LastOperationID = protocolstate.ServerUnprovisioned, nil, ""
	if err := validatePersistedLogicalServer(server); err == nil || !strings.HasSuffix(err.Error(), "invalid logical server record") {
		t.Fatalf("validatePersistedLogicalServer error = %v, want an invalid record", err)
	}
}
