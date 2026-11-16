package schemaregistry

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// LoadFromFS compiles a schema package rooted at index.json. The filesystem is
// read-only from this package's perspective, and references outside the local
// urn registry are rejected before compilation.
func LoadFromFS(root fs.FS) (*Registry, error) {
	index, err := readSchemaIndex(root)
	if err != nil {
		return nil, err
	}
	schemas, err := readIndexedSchemas(root, index.Schemas)
	if err != nil {
		return nil, err
	}
	if err := verifySchemaReferences(schemas); err != nil {
		return nil, err
	}
	validators, err := compileSchemas(schemas)
	if err != nil {
		return nil, err
	}
	return &Registry{protocolVersion: index.ProtocolVersion, validators: validators}, nil
}

func readSchemaIndex(root fs.FS) (indexDocument, error) {
	data, err := fs.ReadFile(root, "index.json")
	if err != nil {
		return indexDocument{}, fmt.Errorf("read schema index: %w", err)
	}
	var index indexDocument
	if err := decodeStrictJSON(data, &index); err != nil {
		return indexDocument{}, fmt.Errorf("decode schema index: %w", err)
	}
	if index.ProtocolVersion == "" || index.JSONSchemaDialect != dialect202012 || len(index.Schemas) == 0 {
		return indexDocument{}, errors.New("schema index has an invalid protocol version, dialect, or empty schema list")
	}
	return index, nil
}

func readIndexedSchemas(root fs.FS, entries []indexEntry) (map[string][]byte, error) {
	schemas := make(map[string][]byte, len(entries))
	paths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !validSchemaIndexEntry(entry) {
			return nil, fmt.Errorf("unsafe or incomplete schema index entry: %#v", entry)
		}
		if _, exists := paths[entry.Path]; exists {
			return nil, fmt.Errorf("duplicate schema path %q", entry.Path)
		}
		paths[entry.Path] = struct{}{}
		data, err := readIndexedSchema(root, entry)
		if err != nil {
			return nil, err
		}
		if _, exists := schemas[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate schema ID %q", entry.ID)
		}
		schemas[entry.ID] = data
	}
	if err := verifySchemaInventory(root, paths); err != nil {
		return nil, err
	}
	return schemas, nil
}

func validSchemaIndexEntry(entry indexEntry) bool {
	relative := !path.IsAbs(entry.Path) && entry.Path != ".." && !strings.HasPrefix(entry.Path, "../")
	return relative && entry.Path != "" && path.Clean(entry.Path) == entry.Path && entry.ID != ""
}

func readIndexedSchema(root fs.FS, entry indexEntry) ([]byte, error) {
	data, err := fs.ReadFile(root, entry.Path)
	if err != nil {
		return nil, fmt.Errorf("read schema %q: %w", entry.Path, err)
	}
	var document map[string]any
	if err := decodeStrictJSON(data, &document); err != nil {
		return nil, fmt.Errorf("decode schema %q: %w", entry.Path, err)
	}
	if document["$id"] != entry.ID || document["$schema"] != dialect202012 {
		return nil, fmt.Errorf("schema %q has an ID or dialect that does not match the index", entry.Path)
	}
	return data, nil
}

func verifySchemaReferences(schemas map[string][]byte) error {
	for id, data := range schemas {
		var document any
		if err := decodeStrictJSON(data, &document); err != nil {
			return fmt.Errorf("decode schema %q: %w", id, err)
		}
		if err := verifyLocalReferences(id, document, schemas); err != nil {
			return err
		}
	}
	return nil
}

func compileSchemas(schemas map[string][]byte) (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMA)
	ids := make([]string, 0, len(schemas))
	for id := range schemas {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemas[id]))
		if err != nil {
			return nil, fmt.Errorf("parse schema %q: %w", id, err)
		}
		if err := compiler.AddResource(id, document); err != nil {
			return nil, fmt.Errorf("register schema %q: %w", id, err)
		}
	}
	validators := make(map[string]*jsonschema.Schema, len(ids))
	for _, id := range ids {
		compiled, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile schema %q: %w", id, err)
		}
		validators[id] = compiled
	}
	return validators, nil
}
