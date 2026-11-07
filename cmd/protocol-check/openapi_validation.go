package main

import (
	"bytes"
	"embed"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The versioned OAI schema resources are vendored so protocol-check stays
// deterministic and can run without network access.
//
//go:embed openapi-schema/*.json
var openAPISchemaFiles embed.FS

const (
	oas32SchemaBaseURL = "https://spec.openapis.org/oas/3.2/schema-base/2026-08-30"
	oas32SchemaURL     = "https://spec.openapis.org/oas/3.2/schema/2026-08-30"
	oas32DialectURL    = "https://spec.openapis.org/oas/3.2/dialect/2026-02-26"
	oas32MetaURL       = "https://spec.openapis.org/oas/3.2/meta/2026-02-26"
	oas32BaseVocabURL  = "https://spec.openapis.org/oas/3.2/vocab/base"
)

type openAPIBaseVocabulary struct{}

func (openAPIBaseVocabulary) Validate(*jsonschema.ValidatorContext, any) {}

var (
	openAPISchemaOnce      sync.Once
	openAPISchemaValidator *jsonschema.Schema
	openAPISchemaErr       error
)

func validateOpenAPIStructure(document any) error {
	openAPISchemaOnce.Do(func() {
		openAPISchemaValidator, openAPISchemaErr = compileOpenAPISchema()
	})
	if openAPISchemaErr != nil {
		return openAPISchemaErr
	}
	return openAPISchemaValidator.Validate(document)
}

func compileOpenAPISchema() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.RegisterVocabulary(&jsonschema.Vocabulary{
		URL: oas32BaseVocabURL,
		Compile: func(_ *jsonschema.CompilerContext, object map[string]any) (jsonschema.SchemaExt, error) {
			for _, keyword := range []string{"discriminator", "example", "externalDocs", "xml"} {
				if _, exists := object[keyword]; exists {
					// These OAS vocabulary keywords are annotations. The vendored
					// meta-schema validates their structure; they do not assert
					// constraints against the API document.
					return openAPIBaseVocabulary{}, nil
				}
			}
			return nil, nil
		},
	})

	resources := []struct {
		url  string
		file string
	}{
		{oas32SchemaURL, "openapi-schema/oas-3.2-schema-2026-08-30.json"},
		{oas32SchemaBaseURL, "openapi-schema/oas-3.2-schema-base-2026-08-30.json"},
		{oas32DialectURL, "openapi-schema/oas-3.2-dialect-2026-02-26.json"},
		{oas32MetaURL, "openapi-schema/oas-3.2-meta-2026-02-26.json"},
	}
	for _, resource := range resources {
		data, err := openAPISchemaFiles.ReadFile(resource.file)
		if err != nil {
			return nil, fmt.Errorf("read vendored OpenAPI schema resource %s: %w", resource.file, err)
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parse vendored OpenAPI schema resource %s: %w", resource.file, err)
		}
		if err := compiler.AddResource(resource.url, document); err != nil {
			return nil, fmt.Errorf("register vendored OpenAPI schema resource %s: %w", resource.file, err)
		}
	}

	schema, err := compiler.Compile(oas32SchemaBaseURL)
	if err != nil {
		return nil, fmt.Errorf("compile official OpenAPI 3.2 schema: %w", err)
	}
	return schema, nil
}
