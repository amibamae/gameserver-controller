package main

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// Validate before encoding/json can replace invalid Unicode with U+FFFD.
func validateJSONUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	inString := false
	for i := 0; i < len(data); i++ {
		switch {
		case data[i] == '"':
			inString = !inString
		case inString && data[i] == '\\':
			trailing, err := jsonEscapeTrailingBytes(data[i:])
			if err != nil {
				return err
			}
			i += trailing
		}
	}
	return nil
}

// jsonEscapeTrailingBytes takes data starting at a backslash and counts the
// bytes after it consumed by the escape, including a required surrogate pair.
func jsonEscapeTrailingBytes(escape []byte) (int, error) {
	if len(escape) < 2 || escape[1] != 'u' {
		return 1, nil
	}
	unit, err := jsonUnicodeCodeUnit(escape, 2)
	if err != nil {
		return 0, err
	}
	if unit < 0xD800 || unit > 0xDFFF {
		return 5, nil
	}
	if unit >= 0xDC00 {
		return 0, fmt.Errorf("unpaired low surrogate")
	}
	if !hasLowSurrogate(escape) {
		return 0, fmt.Errorf("unpaired high surrogate")
	}
	return 11, nil
}

func hasLowSurrogate(pair []byte) bool {
	if len(pair) < 12 || pair[6] != '\\' || pair[7] != 'u' {
		return false
	}
	low, err := jsonUnicodeCodeUnit(pair, 8)
	return err == nil && low >= 0xDC00 && low <= 0xDFFF
}

func jsonUnicodeCodeUnit(data []byte, start int) (uint64, error) {
	if start+4 > len(data) {
		return 0, fmt.Errorf("incomplete Unicode escape")
	}
	unit, err := strconv.ParseUint(string(data[start:start+4]), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("invalid Unicode escape")
	}
	return unit, nil
}
