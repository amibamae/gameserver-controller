package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
)

func parseJSONFile(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseJSON(data, path)
}

func parseJSON(data []byte, source string) (any, error) {
	if err := validateJSONUnicode(data); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON Unicode: %w", source, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", source, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s: trailing data after JSON value", source)
		}
		return nil, fmt.Errorf("%s: invalid trailing JSON: %w", source, err)
	}
	if err := checkSafeIntegers(value, source, "$"); err != nil {
		return nil, err
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delim, ok := token.(json.Delim); {
	case ok && delim == '{':
		return parseJSONObject(decoder)
	case ok && delim == '[':
		return parseJSONArray(decoder)
	case ok:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delim)
	default:
		if number, ok := token.(json.Number); ok {
			if _, err := number.Float64(); err != nil {
				return nil, fmt.Errorf("invalid JSON number %q", number)
			}
		}
		return token, nil
	}
}

// parseJSONObject reads an object's members after its opening brace and
// rejects a repeated member name.
func parseJSONObject(decoder *json.Decoder) (any, error) {
	object := make(map[string]any)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("object member name is not a string")
		}
		if _, exists := object[key]; exists {
			return nil, fmt.Errorf("duplicate JSON object member: %s", key)
		}
		value, err := parseJSONValue(decoder)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, fmt.Errorf("unterminated JSON object")
	}
	return object, nil
}

// parseJSONArray reads an array's values after its opening bracket. An empty
// array decodes as a nil []any.
func parseJSONArray(decoder *json.Decoder) (any, error) {
	var values []any
	for decoder.More() {
		value, err := parseJSONValue(decoder)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil, fmt.Errorf("unterminated JSON array")
	}
	return values, nil
}

func checkSafeIntegers(value any, source, location string) error {
	switch typed := value.(type) {
	case json.Number:
		raw := typed.String()
		rational, ok := new(big.Rat).SetString(raw)
		if !ok {
			return fmt.Errorf("%s%s: invalid JSON number %q", source, location, raw)
		}
		if rational.Denom().Cmp(big.NewInt(1)) != 0 {
			return nil
		}
		limit, _ := new(big.Int).SetString("9007199254740991", 10)
		if new(big.Int).Abs(rational.Num()).Cmp(limit) > 0 {
			return fmt.Errorf("%s%s: integer exceeds the interoperable JSON safe-integer range", source, location)
		}
	case map[string]any:
		for key, child := range typed {
			if err := checkSafeIntegers(child, source, location+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := checkSafeIntegers(child, source, fmt.Sprintf("%s[%d]", location, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectRefs(value any, refs *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			*refs = append(*refs, ref)
		}
		for _, child := range typed {
			collectRefs(child, refs)
		}
	case []any:
		for _, child := range typed {
			collectRefs(child, refs)
		}
	}
}
