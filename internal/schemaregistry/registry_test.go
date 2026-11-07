package schemaregistry

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadEmbeddedSchemasAndValidateInputs(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if registry.ProtocolVersion() != "0.21.0-draft" {
		t.Fatalf("protocol version = %q", registry.ProtocolVersion())
	}
	const inputSchema = "urn:gameserver-controller:0.21.0-draft:hosting:instance-inspect-input"
	if !registry.HasSchema(inputSchema) || registry.HasSchema("https://example.invalid/untrusted.schema.json") {
		t.Fatal("registry schema membership is incorrect")
	}
	valid := []byte(`{"instance_ref":{"adapter_id":"hosting.example","environment_id":"test","resource_kind":"instance","external_id":"opaque-1"}}`)
	if err := registry.ValidateJSON(inputSchema, valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	invalid := []byte(`{"instance_ref":{"adapter_id":"hosting.example","environment_id":"test","resource_kind":"instance","external_id":"opaque-1"},"extra":true}`)
	if err := registry.ValidateJSON(inputSchema, invalid); err == nil {
		t.Fatal("unknown request property was accepted")
	}
	if err := registry.ValidateJSON(inputSchema, []byte(`{"instance_ref":{},"instance_ref":{}}`)); !errors.Is(err, ErrDuplicateJSONProperty) {
		t.Fatalf("duplicate JSON property: got %v", err)
	}
}

func TestLoadRejectsExternalSchemaReferences(t *testing.T) {
	root := fstest.MapFS{
		"index.json":          &fstest.MapFile{Data: []byte(`{"protocol_version":"test-v1","json_schema_dialect":"https://json-schema.org/draft/2020-12/schema","schemas":[{"path":"example.schema.json","id":"urn:test:example"}]}`)},
		"example.schema.json": &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:test:example","type":"object","properties":{"value":{"$ref":"https://example.invalid/schema.json"}}}`)},
	}
	if _, err := LoadFromFS(root); err == nil {
		t.Fatal("external schema reference was accepted")
	}
}
