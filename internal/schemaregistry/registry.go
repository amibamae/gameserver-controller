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

// ParseStrictJSON parses one JSON value, rejecting duplicate object members
// and trailing data. Numbers are returned as json.Number.
func ParseStrictJSON(data []byte) (any, error) { return parseJSON(data) }

// DecodeStrictJSON decodes one JSON value into destination, rejecting
// duplicate object members, unknown fields, and trailing data.
func DecodeStrictJSON(data []byte, destination any) error { return decodeStrictJSON(data, destination) }

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
		// A non-nil empty slice keeps [] from re-encoding as null in DecodeStrictJSON.
		array := []any{}
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
