package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateOpenAPIStructureAcceptsManagementDocument(t *testing.T) {
	pkg, err := loadPackage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("load protocol package: %v", err)
	}
	if err := pkg.validateOpenAPI(); err != nil {
		t.Fatalf("management OpenAPI document failed validation: %v", err)
	}
}

func TestValidateOpenAPIStructureRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "missing required info object",
			yaml: `openapi: "3.2.1"
paths: {}
`,
		},
		{
			name: "responses must be an object",
			yaml: `openapi: "3.2.1"
info:
  title: Example
  version: "1.0"
paths:
  /health:
    get:
      responses: invalid
`,
		},
		{
			name: "schema object must use a valid JSON Schema type",
			yaml: `openapi: "3.2.1"
info:
  title: Example
  version: "1.0"
paths: {}
components:
  schemas:
    Invalid:
      type: 7
`,
		},
		{
			name: "unknown top-level fields are rejected",
			yaml: `openapi: "3.2.1"
info:
  title: Example
  version: "1.0"
paths: {}
notOpenAPI: true
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]any
			if err := yaml.Unmarshal([]byte(test.yaml), &document); err != nil {
				t.Fatalf("parse test YAML: %v", err)
			}
			if err := validateOpenAPIStructure(document); err == nil {
				t.Fatal("validator accepted malformed OpenAPI document")
			}
		})
	}
}

func TestVendoredOpenAPISchemaResourcesArePresent(t *testing.T) {
	for _, name := range []string{
		"oas-3.2-schema-2026-08-30.json",
		"oas-3.2-schema-base-2026-08-30.json",
		"oas-3.2-dialect-2026-02-26.json",
		"oas-3.2-meta-2026-02-26.json",
	} {
		if _, err := openAPISchemaFiles.ReadFile(filepath.Join("openapi-schema", name)); err != nil {
			t.Fatalf("read embedded resource %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join("openapi-schema", "README.md")); err != nil {
		t.Fatalf("OpenAPI schema attribution is missing: %v", err)
	}
}
