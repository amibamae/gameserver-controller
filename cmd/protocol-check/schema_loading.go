package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func loadPackage(root string) (*protocolPackage, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	version, rawEntries, err := readSchemaIndex(absoluteRoot)
	if err != nil {
		return nil, err
	}
	pkg := &protocolPackage{
		root:       absoluteRoot,
		version:    version,
		schemaDocs: make(map[string]any),
		rawSchemas: make(map[string][]byte),
		validators: make(map[string]*jsonschema.Schema),
	}
	indexedPaths := make(map[string]bool)
	for _, rawEntry := range rawEntries {
		if err := pkg.loadIndexedSchema(rawEntry, indexedPaths); err != nil {
			return nil, err
		}
	}
	if err := pkg.checkSchemaInventory(indexedPaths); err != nil {
		return nil, err
	}
	if err := pkg.compileSchemas(); err != nil {
		return nil, err
	}
	return pkg, nil
}

// readSchemaIndex reads protocol/schemas/index.json and returns its protocol
// version and schema entries. The index must select JSON Schema Draft
// 2020-12 and list at least one schema.
func readSchemaIndex(root string) (string, []any, error) {
	indexBytes, err := os.ReadFile(filepath.Join(root, schemaIndexPath))
	if err != nil {
		return "", nil, fmt.Errorf("read schema index: %w", err)
	}
	indexValue, err := parseJSON(indexBytes, schemaIndexPath)
	if err != nil {
		return "", nil, err
	}
	index := asObject(indexValue)
	if index == nil {
		return "", nil, fmt.Errorf("%s must be a JSON object", schemaIndexPath)
	}
	version, ok := index["protocol_version"].(string)
	if !ok || version == "" {
		return "", nil, fmt.Errorf("schema index must declare protocol_version")
	}
	if index["json_schema_dialect"] != "https://json-schema.org/draft/2020-12/schema" {
		return "", nil, fmt.Errorf("schema index must select JSON Schema Draft 2020-12")
	}
	rawEntries, ok := index["schemas"].([]any)
	if !ok || len(rawEntries) == 0 {
		return "", nil, fmt.Errorf("schema index must contain a non-empty schemas array")
	}
	return version, rawEntries, nil
}

// loadIndexedSchema reads one index entry's schema file. The path must stay
// inside protocol/schemas and appear once in the index, and the file must
// be a Draft 2020-12 schema whose unique $id matches the entry.
func (pkg *protocolPackage) loadIndexedSchema(rawEntry any, indexedPaths map[string]bool) error {
	entryMap := asObject(rawEntry)
	if entryMap == nil {
		return fmt.Errorf("schema index entries must be objects")
	}
	relative, okPath := entryMap["path"].(string)
	id, okID := entryMap["id"].(string)
	if !okPath || !okID || relative == "" || id == "" {
		return fmt.Errorf("schema index entry is missing path or id: %#v", rawEntry)
	}
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe schema path in index: %s", relative)
	}
	if indexedPaths[relative] {
		return fmt.Errorf("duplicate schema path in index: %s", relative)
	}
	indexedPaths[relative] = true
	data, err := os.ReadFile(filepath.Join(pkg.root, "protocol", "schemas", clean))
	if err != nil {
		return fmt.Errorf("read indexed schema %s: %w", relative, err)
	}
	doc, err := parseJSON(data, filepath.Join("protocol", "schemas", relative))
	if err != nil {
		return err
	}
	docMap := asObject(doc)
	if docMap == nil {
		return fmt.Errorf("%s must be a JSON object", relative)
	}
	if docMap["$id"] != id {
		return fmt.Errorf("%s: index id does not match schema $id (%q != %v)", relative, id, docMap["$id"])
	}
	if docMap["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		return fmt.Errorf("%s: schema must declare Draft 2020-12", relative)
	}
	if _, exists := pkg.schemaDocs[id]; exists {
		return fmt.Errorf("duplicate schema $id: %s", id)
	}
	pkg.entries = append(pkg.entries, schemaEntry{Path: relative, ID: id})
	pkg.schemaDocs[id] = docMap
	pkg.rawSchemas[id] = data
	return nil
}

func (pkg *protocolPackage) checkSchemaInventory(indexed map[string]bool) error {
	root := filepath.Join(pkg.root, "protocol", "schemas")
	var actual []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		actual = append(actual, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return fmt.Errorf("discover schema files: %w", err)
	}
	sort.Strings(actual)
	for _, path := range actual {
		if !indexed[path] {
			return fmt.Errorf("schema file is not listed in index.json: %s", path)
		}
	}
	for path := range indexed {
		found := false
		for _, actualPath := range actual {
			if path == actualPath {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("index entry is not a discoverable schema file: %s", path)
		}
	}
	return nil
}

func (pkg *protocolPackage) compileSchemas() error {
	ids := make([]string, 0, len(pkg.rawSchemas))
	for id := range pkg.rawSchemas {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMA)
	for _, id := range ids {
		if err := pkg.checkSchemaReferences(id); err != nil {
			return err
		}
	}
	for _, id := range ids {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(pkg.rawSchemas[id]))
		if err != nil {
			return fmt.Errorf("parse schema %s: %w", id, err)
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return fmt.Errorf("register schema %s: %w", id, err)
		}
	}
	for _, id := range ids {
		compiled, err := compiler.Compile(id)
		if err != nil {
			return fmt.Errorf("compile schema %s: %w", id, err)
		}
		pkg.validators[id] = compiled
	}
	return nil
}

func (pkg *protocolPackage) checkSchemaReferences(id string) error {
	doc := pkg.schemaDocs[id]
	var refs []string
	collectRefs(doc, &refs)
	for _, ref := range refs {
		base := strings.SplitN(ref, "#", 2)[0]
		if strings.HasPrefix(base, "urn:") {
			if _, ok := pkg.schemaDocs[base]; !ok {
				return fmt.Errorf("%s: unresolved local schema reference: %s", id, ref)
			}
		} else if base != "" {
			return fmt.Errorf("%s: external schema reference is not allowed in the closed registry: %s", id, ref)
		}
	}
	return nil
}
