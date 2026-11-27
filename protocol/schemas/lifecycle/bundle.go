// Package lifecycle bundles the protocol schemas for server lifecycle operations.
package lifecycle

import (
	_ "embed"
)

//go:embed server.schema.json
var ServerSchema []byte

//go:embed readiness-assertion.schema.json
var ReadinessAssertionSchema []byte

//go:embed server-recover.schema.json
var ServerRecoverSchema []byte

//go:embed server-recover-result.schema.json
var ServerRecoverResultSchema []byte

func init() {
	_ = ServerSchema
	_ = ReadinessAssertionSchema
	_ = ServerRecoverSchema
	_ = ServerRecoverResultSchema
}
