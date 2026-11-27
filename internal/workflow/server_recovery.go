// Package workflow implements durable operations for server lifecycle.
// This is a stub placeholder for Issue #425: Durable Paper Recovery.
// Full implementation with ServerRecoverDefinition to be added when execution adapter and Velocity integration complete.

package workflow

import (
	"github.com/amibamae/gameserver-controller/internal/workflow"
)

func init() {
	// Stub registration - replace with full implementation when Issue #425 is fully merged
	_ = workflow.RegisterWorkflow("server.recover", NewServerRecoverDefinition)
}

// NewServerRecoverDefinition creates a workflow definition for server recovery.
func NewServerRecoverDefinition(
	store *workflow.Store,
	engine *Engine,
) (*workflow.Definition, error) {
	return &workflow.Definition{
		Type: "server.recover",
	}, nil
}
