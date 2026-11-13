package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func (pkg *protocolPackage) validatePackage() error {
	if err := pkg.validateExamples(); err != nil {
		return err
	}
	if err := pkg.validateFixtures(); err != nil {
		return err
	}
	return pkg.validateOpenAPI()
}

// readExampleIndex returns the example-to-schema map from path. Each entry
// needs a path and a schema_id, and each path may appear once.
func readExampleIndex(path string) (map[string]string, error) {
	doc, err := parseJSONFile(path)
	if err != nil {
		return nil, fmt.Errorf("examples/%s: %w", exampleIndexFile, err)
	}
	entries := arrayValue(asObject(doc), "examples")
	if len(entries) == 0 {
		return nil, fmt.Errorf("examples/%s must contain a non-empty examples array", exampleIndexFile)
	}
	listed := map[string]string{}
	for _, raw := range entries {
		entry := asObject(raw)
		name, id := stringValue(entry, "path"), stringValue(entry, "schema_id")
		if _, duplicate := listed[name]; duplicate || name == "" || id == "" {
			return nil, fmt.Errorf("examples/%s: entry %q is empty or duplicated", exampleIndexFile, name)
		}
		listed[name] = id
	}
	return listed, nil
}

func (pkg *protocolPackage) validateExamples() error {
	examplesDir := filepath.Join(pkg.root, "protocol", "examples")
	actual, err := exampleFiles(examplesDir)
	if err != nil {
		return err
	}
	exampleSchemaIDs, err := readExampleIndex(filepath.Join(examplesDir, exampleIndexFile))
	if err != nil {
		return err
	}
	for name := range actual {
		if _, listed := exampleSchemaIDs[name]; !listed {
			return fmt.Errorf("example is not listed in examples/%s: %s", exampleIndexFile, name)
		}
	}
	for name, id := range exampleSchemaIDs {
		if !actual[name] {
			return fmt.Errorf("mapped example does not exist: %s", name)
		}
		doc, err := parseJSONFile(filepath.Join(examplesDir, name))
		if err != nil {
			return err
		}
		if err := pkg.checkCapabilitySchemaURIs(name, doc); err != nil {
			return err
		}
		if err := pkg.validateSchema(id, doc); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	pkg.exampleCount = len(exampleSchemaIDs)
	return nil
}

// exampleFiles lists the example file names in dir, without the index.
func exampleFiles(dir string) (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	actual := make(map[string]bool)
	for _, path := range paths {
		if filepath.Base(path) == exampleIndexFile {
			continue
		}
		actual[filepath.Base(path)] = true
	}
	return actual, nil
}

// checkCapabilitySchemaURIs: every capability an example descriptor lists
// must name input and output schemas that resolve in the local registry.
func (pkg *protocolPackage) checkCapabilitySchemaURIs(name string, doc any) error {
	capabilities, ok := asObject(doc)["capabilities"].([]any)
	if !ok {
		return nil
	}
	for i, rawCapability := range capabilities {
		capability := asObject(rawCapability)
		if capability == nil {
			return fmt.Errorf("%s: capability %d must be an object", name, i)
		}
		for _, field := range []string{"input_schema_uri", "output_schema_uri"} {
			uri, ok := capability[field].(string)
			if !ok {
				return fmt.Errorf("%s: capability %d %s must be a string", name, i, field)
			}
			if _, ok := pkg.validators[uri]; !ok {
				return fmt.Errorf("%s: capability %d %s does not resolve in the local schema registry: %q", name, i, field, uri)
			}
		}
	}
	return nil
}

func (pkg *protocolPackage) validateFixtures() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return fmt.Errorf("no conformance fixtures found")
	}
	seen := make(map[string]bool)
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err != nil {
			return fmt.Errorf("%s: invalid conformance fixture JSON: %w", filepath.Base(path), err)
		}
		doc := asObject(fixture)
		name := filepath.Base(path)
		id, ok := doc["fixture_id"].(string)
		if !ok || id == "" {
			return fmt.Errorf("%s: fixture_id is required", name)
		}
		if seen[id] {
			return fmt.Errorf("%s: duplicate fixture_id: %s", name, id)
		}
		seen[id] = true
		if doc["protocol_version"] != pkg.version {
			return fmt.Errorf("%s: fixture protocol_version must match schema index", name)
		}
		if asObject(doc["expected"]) == nil {
			return fmt.Errorf("%s: expected must be an object", name)
		}
	}
	return nil
}

func (pkg *protocolPackage) validateOpenAPI() error {
	path := filepath.Join(pkg.root, "protocol", "management-api", "openapi.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read OpenAPI document: %w", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse OpenAPI document: %w", err)
	}
	if document["openapi"] != "3.2.1" {
		return fmt.Errorf("OpenAPI document must declare 3.2.1, found %v", document["openapi"])
	}
	if err := validateOpenAPIStructure(document); err != nil {
		return fmt.Errorf("OpenAPI document does not match the OAS 3.2 schema: %w", err)
	}
	if err := checkCanonicalComponents(document); err != nil {
		return err
	}
	var refs []string
	collectRefs(document, &refs)
	for _, ref := range refs {
		if err := pkg.checkOpenAPIReference(path, ref); err != nil {
			return err
		}
	}
	return nil
}

// checkCanonicalComponents: the API's Operation and Server components are
// the canonical protocol schemas, not copies.
func checkCanonicalComponents(document map[string]any) error {
	apiSchemas := asObject(asObject(document["components"])["schemas"])
	expectedRefs := map[string]string{
		"Operation": "../schemas/operations/operation.schema.json",
		"Server":    "../schemas/lifecycle/server.schema.json",
	}
	for name, expected := range expectedRefs {
		component := asObject(apiSchemas[name])
		if component == nil || component["$ref"] != expected {
			return fmt.Errorf("OpenAPI component %s must reference the canonical schema %s", name, expected)
		}
	}
	return nil
}

// checkOpenAPIReference: an external reference from the document at path
// must name an existing, indexed schema inside protocol/schemas. Local
// (#...) references are not checked here.
func (pkg *protocolPackage) checkOpenAPIReference(path, ref string) error {
	if strings.HasPrefix(ref, "#") {
		return nil
	}
	relative := strings.SplitN(ref, "#", 2)[0]
	if relative == "" {
		return nil
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(relative)))
	if !within(pkg.root, target) {
		return fmt.Errorf("OpenAPI reference escapes the repository: %s", ref)
	}
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("OpenAPI reference target does not exist: %s", ref)
	}
	schemaRoot := filepath.Join(pkg.root, "protocol", "schemas")
	if !within(schemaRoot, target) {
		return fmt.Errorf("OpenAPI external reference must point to the local schema package: %s", ref)
	}
	relToSchemas, _ := filepath.Rel(schemaRoot, target)
	if !pkg.pathIsIndexed(filepath.ToSlash(relToSchemas)) {
		return fmt.Errorf("OpenAPI reference target is not registered in schemas/index.json: %s", ref)
	}
	return nil
}

// within reports whether target is base or lies under it.
func within(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (pkg *protocolPackage) pathIsIndexed(path string) bool {
	for _, entry := range pkg.entries {
		if entry.Path == path {
			return true
		}
	}
	return false
}

func (pkg *protocolPackage) validateSchema(id string, value any) error {
	validator, ok := pkg.validators[id]
	if !ok {
		return fmt.Errorf("mapped schema is missing from index.json: %s", id)
	}
	return validator.Validate(value)
}

func (pkg *protocolPackage) fixtureCount() int {
	paths, _ := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	return len(paths)
}
