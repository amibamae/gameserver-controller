// Package schemaregistry compiles and validates the repository's closed,
// versioned JSON Schema package.
package schemaregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/amibamae/gameserver-controller/protocol"
)

const dialect202012 = "https://json-schema.org/draft/2020-12/schema"

var ErrDuplicateJSONProperty = errors.New("duplicate JSON object property")

type indexDocument struct {
	ProtocolVersion   string       `json:"protocol_version"`
	JSONSchemaDialect string       `json:"json_schema_dialect"`
	Schemas           []indexEntry `json:"schemas"`
}

type indexEntry struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

type Registry struct {
	protocolVersion string
	validators      map[string]*jsonschema.Schema
}

type ecmaRegexp regexp2.Regexp

func (re *ecmaRegexp) MatchString(value string) bool {
	matched, err := (*regexp2.Regexp)(re).MatchString(value)
	return err == nil && matched
}

func (re *ecmaRegexp) String() string {
	return (*regexp2.Regexp)(re).String()
}

func compileECMA(expression string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(expression, regexp2.ECMAScript)
	if err != nil {
		return nil, err
	}
	return (*ecmaRegexp)(re), nil
}

// Load compiles the schemas embedded in this executable.
func Load() (*Registry, error) {
	root, err := fs.Sub(protocol.SchemaFiles(), "schemas")
	if err != nil {
		return nil, fmt.Errorf("open embedded protocol schemas: %w", err)
	}
	return LoadFromFS(root)
}

// LoadFromFS compiles a schema package rooted at index.json. The filesystem is
// read-only from this package's perspective, and references outside the local
// urn registry are rejected before compilation.
func LoadFromFS(root fs.FS) (*Registry, error) {
	indexBytes, err := fs.ReadFile(root, "index.json")
	if err != nil {
		return nil, fmt.Errorf("read schema index: %w", err)
	}
	var index indexDocument
	if err := decodeStrictJSON(indexBytes, &index); err != nil {
		return nil, fmt.Errorf("decode schema index: %w", err)
	}
	if index.ProtocolVersion == "" || index.JSONSchemaDialect != dialect202012 || len(index.Schemas) == 0 {
		return nil, errors.New("schema index has an invalid protocol version, dialect, or empty schema list")
	}

	registry := &Registry{protocolVersion: index.ProtocolVersion, validators: make(map[string]*jsonschema.Schema, len(index.Schemas))}
	rawSchemas := make(map[string][]byte, len(index.Schemas))
	paths := make(map[string]struct{}, len(index.Schemas))
	for _, entry := range index.Schemas {
		clean := path.Clean(entry.Path)
		if entry.Path == "" || clean != entry.Path || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || entry.ID == "" {
			return nil, fmt.Errorf("unsafe or incomplete schema index entry: %#v", entry)
		}
		if _, exists := paths[entry.Path]; exists {
			return nil, fmt.Errorf("duplicate schema path %q", entry.Path)
		}
		paths[entry.Path] = struct{}{}
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
		if _, exists := rawSchemas[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate schema ID %q", entry.ID)
		}
		rawSchemas[entry.ID] = data
	}
	if err := verifySchemaInventory(root, paths); err != nil {
		return nil, err
	}
	for id, data := range rawSchemas {
		var document any
		if err := decodeStrictJSON(data, &document); err != nil {
			return nil, fmt.Errorf("decode schema %q: %w", id, err)
		}
		if err := verifyLocalReferences(id, document, rawSchemas); err != nil {
			return nil, err
		}
	}

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	compiler.UseRegexpEngine(compileECMA)
	ids := make([]string, 0, len(rawSchemas))
	for id := range rawSchemas {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchemas[id]))
		if err != nil {
			return nil, fmt.Errorf("parse schema %q: %w", id, err)
		}
		if err := compiler.AddResource(id, document); err != nil {
			return nil, fmt.Errorf("register schema %q: %w", id, err)
		}
	}
	for _, id := range ids {
		compiled, err := compiler.Compile(id)
		if err != nil {
			return nil, fmt.Errorf("compile schema %q: %w", id, err)
		}
		registry.validators[id] = compiled
	}
	return registry, nil
}

func verifySchemaInventory(root fs.FS, indexed map[string]struct{}) error {
	actual := make(map[string]struct{})
	if err := fs.WalkDir(root, ".", func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			return nil
		}
		actual[filePath] = struct{}{}
		return nil
	}); err != nil {
		return fmt.Errorf("walk schema package: %w", err)
	}
	for filePath := range actual {
		if _, ok := indexed[filePath]; !ok {
			return fmt.Errorf("schema file %q is absent from index.json", filePath)
		}
	}
	for filePath := range indexed {
		if _, ok := actual[filePath]; !ok {
			return fmt.Errorf("schema index path %q is missing", filePath)
		}
	}
	return nil
}

func verifyLocalReferences(schemaID string, value any, schemas map[string][]byte) error {
	switch current := value.(type) {
	case map[string]any:
		if raw, ok := current["$ref"]; ok {
			ref, ok := raw.(string)
			if !ok {
				return fmt.Errorf("schema %q has a non-string $ref", schemaID)
			}
			base := strings.SplitN(ref, "#", 2)[0]
			if base != "" && !strings.HasPrefix(base, "urn:") {
				return fmt.Errorf("schema %q references a non-local resource %q", schemaID, ref)
			}
			if base != "" {
				if _, ok := schemas[base]; !ok {
					return fmt.Errorf("schema %q has an unresolved local reference %q", schemaID, ref)
				}
			}
		}
		for _, child := range current {
			if err := verifyLocalReferences(schemaID, child, schemas); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range current {
			if err := verifyLocalReferences(schemaID, child, schemas); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Registry) ProtocolVersion() string {
	if r == nil {
		return ""
	}
	return r.protocolVersion
}

func (r *Registry) HasSchema(id string) bool {
	if r == nil {
		return false
	}
	_, ok := r.validators[id]
	return ok
}

func (r *Registry) Validate(id string, value any) error {
	if r == nil {
		return errors.New("schema registry is nil")
	}
	validator, ok := r.validators[id]
	if !ok {
		return fmt.Errorf("schema %q is not registered", id)
	}
	if err := validator.Validate(value); err != nil {
		return fmt.Errorf("schema %q: %w", id, err)
	}
	return nil
}

func (r *Registry) ValidateJSON(id string, data []byte) error {
	value, err := parseJSON(data)
	if err != nil {
		return err
	}
	return r.Validate(id, value)
}

func decodeStrictJSON(data []byte, destination any) error {
	value, err := parseJSON(data)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := ensureEOF(decoder); err != nil {
		return err
	}
	return nil
}

func parseJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

func ensureEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing data after JSON value")
		}
		return err
	}
	return nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("%w: %q", ErrDuplicateJSONProperty, key)
			}
			value, err := parseJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		var array []any
		for decoder.More() {
			value, err := parseJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
