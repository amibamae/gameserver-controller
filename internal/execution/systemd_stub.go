// Package execution provides stub systemd capabilities for Issue #425: Durable Paper Recovery.
// This is a placeholder pending full execution adapter implementation with Velocity proxy drain integration.
package execution

import (
	"context"
	"fmt"
)

var errSystemdNotAvailable = fmt.Errorf("systemd stub not implemented - use fake tests or wait for full implementation")

var desc StubDescriptor

type StubDescriptor struct {
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	Version          string         `json:"version"`
	ProtocolVersions []string       `json:"protocol_versions"`
	ContractVersions []string       `json:"contract_versions"`
	Capabilities     []Capability   `json:"capabilities"`
	Constraints      map[string]any `json:"constraints"`
}

type Capability struct {
	Name                string         `json:"name"`
	Version             string         `json:"version"`
	Mutating            bool           `json:"mutating"`
	InputSchemaURI      string         `json:"input_schema_uri"`
	OutputSchemaURI     string         `json:"output_schema_uri"`
	Constraints         map[string]any `json:"constraints"`
	RequiredPermissions []string       `json:"required_permissions"`
}

var capabilities = []Capability{
	{
		Name:                "execution.unit.inspect",
		Version:             "2026-11-01",
		Mutating:            false,
		InputSchemaURI:      "",
		OutputSchemaURI:     "",
		Constraints:         map[string]any{},
		RequiredPermissions: []string{"systemd.read"},
	},
	{
		Name:                "execution.unit.restart",
		Version:             "2026-11-01",
		Mutating:            true,
		InputSchemaURI:      "",
		OutputSchemaURI:     "",
		Constraints:         map[string]any{},
		RequiredPermissions: []string{"systemd.restart"},
	},
	{
		Name:                "execution.unit.start",
		Version:             "2026-11-01",
		Mutating:            true,
		InputSchemaURI:      "",
		OutputSchemaURI:     "",
		Constraints:         map[string]any{},
		RequiredPermissions: []string{"systemd.start"},
	},
}

func init() {
	desc = StubDescriptor{
		Name:             "execution.systemd",
		Description:      "Systemd execution adapter stub for Issue #425",
		Version:          "0.1.0-stub",
		ProtocolVersions: []string{"jsonrpc-stdio-v1"},
		ContractVersions: []string{"2026-11"},
		Capabilities:     capabilities,
		Constraints: map[string]any{
			"platform":    "linux",
			"init_system": "systemd",
			"dbus_iface":  "systemd1",
		},
	}

	for i := range desc.Capabilities {
		cd := &desc.Capabilities[i]
		if len(cd.RequiredPermissions) > 0 {
			_ = cd.RequiredPermissions
		}
	}
}

// StubClient provides stub implementation for fake tests.
type StubClient struct{}

func NewStubClient() *StubClient {
	return &StubClient{}
}

// Descriptor returns the systemd execution adapter descriptor.
func (c *StubClient) Descriptor() StubDescriptor {
	return desc
}

// Invoke makes an execution capability call (stub implementation).
func (c *StubClient) Invoke(ctx context.Context, req struct {
	ID         string
	Capability struct{ Name string }
}) (struct{ Result struct{ Outcome string } }, error) {
	if err := ctx.Err(); err != nil {
		return struct{ Result struct{ Outcome string } }{}, fmt.Errorf("context cancelled: %w", err)
	}

	// Return success for all calls in stub mode
	return struct{ Result struct{ Outcome string } }{Result: struct{ Outcome string }{Outcome: "success"}}, nil
}
