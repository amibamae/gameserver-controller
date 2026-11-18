package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

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

// asObject safely converts a value to map[string]any. Returns nil if conversion fails.
func asObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

// safeAsObject wraps asObject with an error to indicate when conversion failed.
func safeAsObject(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("value is not a map[string]any: got %T", value)
	}
	return object, nil
}

// stringValue safely extracts a string from a map at the given key.
// If the value or conversion fails, it returns an empty string with no error.
func stringValue(object map[string]any, key string) string {
	value, ok := object[key].(string)
	if !ok {
		return ""
	}
	return value
}

// safeStringValue is a no-op wrapper that returns the same result as stringValue.
func safeStringValue(object map[string]any, key string) (string, error) {
	return stringValue(object, key), nil
}

// boolValue safely extracts a bool from a map at the given key.
// If the value or conversion fails, it returns false with no error.
func boolValue(object map[string]any, key string) bool {
	value, ok := object[key].(bool)
	if !ok {
		return false
	}
	return value
}

// safeBoolValue is a no-op wrapper that returns the same result as boolValue.
func safeBoolValue(object map[string]any, key string) (bool, error) {
	return boolValue(object, key), nil
}

// arrayValue safely extracts a []any from a map at the given key.
// If the value or conversion fails, it returns an empty slice with no error.
func arrayValue(object map[string]any, key string) []any {
	value, ok := object[key].([]any)
	if !ok {
		return nil
	}
	return value
}

// safeArrayValue is a no-op wrapper that returns the same result as arrayValue.
func safeArrayValue(object map[string]any, key string) ([]any, error) {
	return arrayValue(object, key), nil
}

// contains checks if values contains the target string.
func contains(values []any, want any) bool {
	if values == nil {
		return false
	}
	for _, value := range values {
		if fmt.Sprintf("%v", value) == fmt.Sprintf("%v", want) {
			return true
		}
	}
	return false
}

// uniqueStrings returns true if all string elements in values are unique and non-empty.
func uniqueStrings(values []any) bool {
	if len(values) == 0 {
		return true
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		str, ok := value.(string)
		if !ok || str == "" || seen[str] {
			return false
		}
		seen[str] = true
	}
	return true
}

// parseTime parses a timestamp string as RFC3339Nano UTC. Returns empty time with error if invalid.
func parseTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("expected UTC Z timestamp, got %q", value)
	}
	return time.Parse(time.RFC3339Nano, value)
}

// safeJSONNumber converts any JSON number to int64 safely. Returns 0 with error for unsafe values.
func safeJSONNumber(num json.Number) (int64, error) {
	const maxInt64 = int64(1<<63 - 1)
	const minInt64 = int64(-1 << 63)
	// JavaScript safe integer range: -2^53 to 2^53-1
	const maxSafeJSInt = int64(1<<53 - 1)
	const minSafeJSInt = -(int64(1<<53))

	raw, err := num.Int64()
	if err != nil {
		return 0, fmt.Errorf("json number %q exceeds int64 range: %w", num.String(), err)
	}

	if raw > maxInt64 || raw < minInt64 {
		return 0, fmt.Errorf("integer %s exceeds int64 range", num.String())
	}

	// Check against JavaScript safe integer range for cross-platform consistency
	if raw > maxSafeJSInt || raw < minSafeJSInt {
		return 0, fmt.Errorf("integer %s exceeds safe-integer range (%d to %d)", num.String(), maxSafeJSInt, minSafeJSInt)
	}

	return raw, nil
}

// ensureSafeNumber converts a number (int, int64, or json.Number) to int64 safely.
func ensureSafeNumber(value any) (int64, error) {
	switch v := value.(type) {
	case json.Number:
		return safeJSONNumber(v)
	case int:
		const maxInt = int64(1<<31 - 1)
		const minInt = int64(-1 << 31)
		if int64(v) > maxInt || int64(v) < minInt {
			return 0, fmt.Errorf("integer %d exceeds safe range", v)
		}
		return int64(v), nil
	case int64:
		const maxInt64 = int64(1<<63 - 1)
		const minInt64 = int64(-1 << 63)
		if v > maxInt64 || v < minInt64 {
			return 0, fmt.Errorf("integer %d exceeds safe range", v)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

// deepEqual compares two values for equality using reflect.DeepEqual.
func deepEqual(a, b any) bool {
	return reflect.DeepEqual(a, b)
}

// safeDeepEqual compares two values for equality. Returns false for non-map objects.
func safeDeepEqual(a, b any) (bool, error) {
	aMap, err := safeAsObject(a)
	if err != nil {
		return false, nil // non-map types are considered not equal (no error)
	}
	bMap, err := safeAsObject(b)
	if err != nil {
		return false, nil // non-map types are considered not equal (no error)
	}
	return deepEqual(aMap, bMap), nil
}
