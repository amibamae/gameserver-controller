package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	regexp2 "github.com/dlclark/regexp2"
)

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

type protocolPackage struct {
	root       string
	version    string
	entries    []schemaEntry
	schemaDocs map[string]any
	rawSchemas map[string][]byte
	validators map[string]*jsonschema.Schema
	exampleCount int
}

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
		exampleCount: 0,
	}
	indexedPaths := make(map[string]bool)
	for _, rawEntry := range rawEntries {
		if err := pkg.loadIndexedSchema(rawEntry, indexedPaths); err != nil {
			return nil, err
		}
	}
	if err := pkg.compileSchemas(); err != nil {
		return nil, err
	}
	return pkg, nil
}

func readSchemaIndex(root string) (string, []any, error) {
	indexBytes, err := os.ReadFile(filepath.Join(root, "protocol/schemas/index.json"))
	if err != nil {
		return "", nil, fmt.Errorf("read schema index: %w", err)
	}
	indexValue, err := parseJSON(indexBytes, "protocol/schemas/index.json")
	if err != nil {
		return "", nil, err
	}
	index := asObject(indexValue)
	if index == nil {
		return "", nil, fmt.Errorf("protocol/schemas/index.json must be a JSON object")
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
	data, err := os.ReadFile(filepath.Join(pkg.root, "protocol/schemas", filepath.Clean(relative)))
	if err != nil {
		return fmt.Errorf("read indexed schema %s: %w", relative, err)
	}
	if _, ok := entryMap["$id"].(string); !ok {
		entryMap["$id"] = id
	}
	pkg.schemaDocs[id] = asObject(entryMap)
	pkg.rawSchemas[id] = data
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

func collectRefs(doc any, refs *[]string) {
	switch d := doc.(type) {
	case map[string]any:
		if raw, ok := d["allOf"].([]any); ok {
			for _, entry := range raw {
				collectRefs(entry, refs)
			}
		}
		if raw, ok := d["oneOf"].([]any); ok {
			for _, entry := range raw {
				collectRefs(entry, refs)
			}
		}
		if raw, ok := d["not"].(map[string]any); ok {
			collectRefs(raw, refs)
		}
	case []any:
		for _, entry := range d {
			collectRefs(entry, refs)
		}
	default:
		return
	}
}

func (pkg *protocolPackage) runConformance() error {
	pkg.exampleCount = 0
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol/conformance/test-vectors/*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := pkg.checkFixture(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	pkg.exampleCount = len(paths)
	return nil
}

func (pkg *protocolPackage) checkFixture(path string) error {
	value, err := parseJSON(os.ReadFile(path), path)
	if err != nil {
		return err
	}
	fixture := asObject(value)
	fixtureID := stringValue(fixture, "fixture_id")
	result, err := evaluators[fixtureID](pkg, fixture)
	if err != nil {
		return fmt.Errorf("evaluate %s: %w", fixtureID, err)
	}
	if !result["validation_passed"].(bool) {
		missing := result["missing"]
		missingList := make([]string, 0)
		if m, ok := missing.([]any); ok {
			for _, v := range m {
				missingList = append(missingList, fmt.Sprint(v))
			}
		}
		return fmt.Errorf("validation_failed: %s - %v", fixtureID, missingList)
	}
	if result["fixture_passed"].(bool) {
		pkg.exampleCount++
	}
	return nil
}

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func parseJSON(data []byte) (any, error) {
	var result any
	err := json.Unmarshal(data, &result)
	return result, err
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func boolValue(object map[string]any, key string) bool {
	value, _ := object[key].(bool)
	return value
}

func arrayValue(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	return value
}

func contains(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func uniqueStrings(values []any) bool {
	seen := make(map[string]bool)
	for _, value := range values {
		str, ok := value.(string)
		if !ok || seen[str] {
			return false
		}
		seen[str] = true
	}
	return true
}

func parseTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("expected UTC Z timestamp, got %q", value)
	}
	return time.Parse(time.RFC3339Nano, value)
}

func integer64Value(object map[string]any, key string) (int64, error) {
	switch v := object[key].(type) {
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n, nil
	default:
		return 0, fmt.Errorf("unexpected type %T for %q", v, key)
	}
}

func integerValueDirect(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n, nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

func stringValueDirect(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func arrayValueDirect(value any) []any {
	if arr, ok := value.([]any); ok {
		return arr
	}
	return nil
}

func boolValueDirect(value any) bool {
	if b, ok := value.(bool); ok {
		return b
	}
	return false
}

func intValueDirect(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		var n int
		fmt.Sscanf(fmt.Sprintf("%v", v), "%d", &n)
		return n
	}
}

func writeJSONFile(path string, value any) error {
	encoder := json.NewEncoder(os.Create(filepath.Clean(path)))
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func readAndParseJSON(path string) (any, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("readJSONFile(%q): %w", path, err)
	}
	return parseJSON(data)
}

func sameObjectRefSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	leftMap := make(map[string]bool, len(left))
	for _, p := range left {
		if p == nil {
			continue
		}
		reflectVal := reflect.ValueOf(p).String()
		leftMap[reflectVal] = true
	}
	for _, p := range right {
		if p == nil {
			continue
		}
		reflectVal := reflect.ValueOf(p).String()
		if !leftMap[reflectVal] {
			return false
		}
		delete(leftMap, reflectVal)
	}
	return true
}

func sha256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

var evaluators = map[string]any{}

func init() {
	// Evaluators are registered automatically when their package is loaded.
}