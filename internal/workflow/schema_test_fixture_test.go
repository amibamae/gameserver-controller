package workflow

import (
	"sync"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// Share only the immutable schemas embedded in the test binary. Each caller
// still creates its own workflow state and adapters.
var loadWorkflowTestSchemas = sync.OnceValues(schemaregistry.Load)

func workflowTestSchemas(t testing.TB) *schemaregistry.Registry {
	t.Helper()
	schemas, err := loadWorkflowTestSchemas()
	if err != nil {
		t.Fatalf("load embedded workflow test schemas: %v", err)
	}
	return schemas
}

// Exercise both success and failure using the same compiled validator.
func TestWorkflowTestSchemasConcurrentValidation(t *testing.T) {
	for range 16 {
		t.Run("validate", func(t *testing.T) {
			t.Parallel()
			checkWorkflowTestSchemaValidation(t)
		})
	}
}

func checkWorkflowTestSchemaValidation(t *testing.T) {
	t.Helper()
	schemas := workflowTestSchemas(t)
	id := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:identifier"
	for range 25 {
		if err := schemas.ValidateJSON(id, []byte(`"staging-1"`)); err != nil {
			t.Fatalf("valid identifier rejected: %v", err)
		}
		if err := schemas.ValidateJSON(id, []byte(`"staging\u0000id"`)); err == nil {
			t.Fatal("invalid identifier was accepted")
		}
	}
}
