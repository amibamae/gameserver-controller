package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	regexp2 "github.com/dlclark/regexp2"
)

// ecmaRegexp is a JSON Schema regexp interface that uses the ECMAScript engine.
type ecmaRegexp struct {
	pattern string
	regex   *regexp2.Regexp
}

// MatchString compiles and matches the pattern if not already compiled.
func (re *ecmaRegexp) MatchString(value string) bool {
	if re.regex == nil {
		var err error
		re.regex, err = regexp2.Compile(re.pattern, regexp2.ECMAScript)
		if err != nil {
			return false
		}
	}
	matched, err := re.regex.MatchString(value)
	return err == nil && matched
}

// String returns the string pattern for this regexp.
func (re *ecmaRegexp) String() string {
	return re.pattern
}

// compileECMA compiles an ECMAScript regex and returns an ecmaRegexp interface.
func compileECMA(expression string) (jsonschema.Regexp, error) {
	_, err := regexp2.Compile(expression, regexp2.ECMAScript)
	if err != nil {
		return nil, fmt.Errorf("compileECMA(%q): %w", expression, err)
	}
	return &ecmaRegexp{pattern: expression}, nil
}

type schemaEntry struct {
	Path string
	ID   string
}

// evaluator checks one conformance fixture and returns the result that is
// compared with the fixture's expected section.
type evaluator func(*protocolPackage, map[string]any) (map[string]any, error)

// evaluators maps each published fixture_id to its evaluator. Evaluators
// register themselves from an init function in the file that defines them, so
// adding a fixture does not require editing a shared dispatch table.
var evaluators = map[string]evaluator{}

// registerEvaluator routes each fixture ID to fn. It panics on an empty or
// repeated ID, so a registration mistake stops the program at startup.
func registerEvaluator(fn evaluator, fixtureIDs ...string) {
	if fn == nil || len(fixtureIDs) == 0 {
		panic("registerEvaluator needs an evaluator and at least one fixture ID")
	}
	for _, fixtureID := range fixtureIDs {
		if fixtureID == "" {
			panic("registerEvaluator: empty fixture ID")
		}
		if _, exists := evaluators[fixtureID]; exists {
			panic(fmt.Sprintf("registerEvaluator: fixture %q is already registered", fixtureID))
		}
		evaluators[fixtureID] = fn
	}
}

// withoutPackage adapts an evaluator that needs only the fixture.
func withoutPackage(fn func(map[string]any) (map[string]any, error)) evaluator {
	return func(_ *protocolPackage, fixture map[string]any) (map[string]any, error) {
		return fn(fixture)
	}
}

type protocolPackage struct {
	root       string
	version    string
	entries    []schemaEntry
	schemaDocs map[string]any
	rawSchemas map[string][]byte
	validators map[string]*jsonschema.Schema
	exampleCount int
	fixtureCount int
}

// asObject safely converts a value to map[string]any. Returns nil if conversion fails.
func asObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

// parseJSON parses JSON bytes with strict validation.
func parseJSON(data []byte, source string) (any, error) {
	if err := validateJSONUnicode(data); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON Unicode: %w", source, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", source, err)
	}
	if _, err := decoder.Token(); err != json.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s: trailing data after JSON value", source)
		}
		return nil, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, _ := decoder.Token()
	if token == nil {
		return nil, nil
	}
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func validateJSONUnicode(data []byte) error {
	for _, r := range string(data) {
		if r > 127 && (r < 0xD800 || r > 0xDFFF) {
			continue
		}
	}
	return nil
}

// loadPackage loads the protocol package from root directory.
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
		fixtureCount: 0,
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
	data, err := os.ReadFile(filepath.Join(pkg.root, "protocol/schemas", clean))
	if err != nil {
		return fmt.Errorf("read indexed schema %s: %w", relative, err)
	}
	doc, err := parseJSON(data, filepath.Join("protocol/schemas", relative))
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
	root := filepath.Join(pkg.root, "protocol/schemas")
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
	}
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

// validatePackage checks the protocol package structure.
func (pkg *protocolPackage) validatePackage() error {
	if len(pkg.entries) == 0 {
		return fmt.Errorf("protocol package has no schemas")
	}
	for _, e := range pkg.entries {
		if e.Path == "" || e.ID == "" {
			return fmt.Errorf("schema entry %s is invalid", e.Path)
		}
	}
	return nil
}

// runConformance checks every published fixture and reports all failures together.
func (pkg *protocolPackage) runConformance() error {
	pkg.fixtureCount = 0
	pkg.exampleCount = 0
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol/conformance/test-vectors/*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var failures []string
	for _, path := range paths {
		if err := pkg.checkFixture(path); err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
	}
	return nil
}

// checkFixture evaluates one conformance fixture and reports failure if validation_failed.
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
	pkg.fixtureCount++
	return nil
}

// packageManifest produces the protocol package manifest as JSON.
func (pkg *protocolPackage) packageManifest() (map[string]any, error) {
	manifest := make(map[string]any)
	manifest["protocol_version"] = pkg.version
	manifest["schemas"] = make([]map[string]any, len(pkg.entries))
	for i, e := range pkg.entries {
		manifest["schemas"].([]map[string]any)[i] = map[string]string{
			"id":   e.ID,
			"path": e.Path,
		}
	}
	return manifest, nil
}

func (pkg *protocolPackage) fixtureCount() int {
	return pkg.fixtureCount
}

// stringValue safely extracts a string from a map at the given key.
// Signature matches how conformance files use it: stringValue(map[string]any, string) -> string
func stringValue(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, ok := object[key].(string)
	if !ok {
		return ""
	}
	return value
}

// arrayValue safely extracts a []any from a map at the given key.
func arrayValue(object map[string]any, key string) []any {
	if object == nil {
		return nil
	}
	value, ok := object[key].([]any)
	if !ok {
		return nil
	}
	return value
}

// boolValue safely extracts a bool from a map at the given key.
func boolValue(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	value, ok := object[key].(bool)
	if !ok {
		return false
	}
	return value
}

// intValue safely extracts an int from a map at the given key.
func intValue(object map[string]any, key string) int {
	if object == nil {
		return 0
	}
	value, ok := object[key].(int)
	if !ok {
		return 0
	}
	return value
}

// integerValue safely extracts an int64 from a map at the given key.
func integerValue(object map[string]any, key string) (int64, error) {
	if object == nil {
		return 0, fmt.Errorf("object is nil")
	}
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

// mergeJSONObjects merges two JSON objects recursively.
func mergeJSONObjects(base, override map[string]any) map[string]any {
	if base == nil {
		base = make(map[string]any)
	}
	result := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		if baseObject, ok := result[key].(map[string]any); ok {
			if overrideObject, nested := value.(map[string]any); nested {
				result[key] = mergeJSONObjects(baseObject, overrideObject)
				continue
			}
		}
		result[key] = value
	}
	return result
}

// deleteStringKeys deletes keys from a map that match the given keys.
func deleteStringKeys(object map[string]any, keys []any) {
	if object == nil || len(keys) == 0 {
		return
	}
	for _, key := range keys {
		if stringKey, ok := key.(string); ok {
			delete(object, stringKey)
		}
	}
}

// containsString checks if any element in values equals target.
func containsString(values []any, target string) bool {
	for _, value := range values {
		if item, ok := value.(string); ok && item == target {
			return true
		}
	}
	return false
}

// contains checks if any element in values equals target (generic).
func contains(values []any, target any) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// uniqueStrings checks if all string elements in values are unique.
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

// parseTime parses a UTC RFC3339Nano timestamp.
func parseTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("expected UTC Z timestamp, got %q", value)
	}
	return time.Parse(time.RFC3339Nano, value)
}

// setString sets a string key in an object.
func setString(object map[string]any, key string, value string) {
	if object == nil {
		object = make(map[string]any)
	}
	object[key] = value
}

// appendStringAppends a string to a slice at the given key.
func appendString(object map[string]any, key string, value string) {
	if object == nil {
		object = make(map[string]any)
	}
	if values, ok := object[key].([]any); ok {
		values = append(values, value)
	} else {
		values = []any{value}
		object[key] = values
	}
}

// mapStringAny creates a new map[string]any from key-value pairs.
func mapStringAny(m ...string) map[string]any {
	result := make(map[string]any, len(m)/2)
	for i := 0; i < len(m); i += 2 {
		if i+1 < len(m) {
			result[m[i]] = m[i+1]
		}
	}
	return result
}

// timeTimeValue safely extracts a time.Time from a map at the given key.
func timeTimeValue(object map[string]any, key string) (time.Time, error) {
	if object == nil {
		return time.Time{}, fmt.Errorf("object is nil")
	}
	switch v := object[key].(type) {
	case time.Time:
		return v, nil
	case string:
		return parseTime(v)
	default:
		return time.Time{}, fmt.Errorf("unexpected type %T for %q", v, key)
	}
}
