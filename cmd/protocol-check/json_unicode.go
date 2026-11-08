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
		if !inString {
			if data[i] == '"' {
				inString = true
			}
			continue
		}
		switch data[i] {
		case '"':
			inString = false
		case 0x5c:
			if i+1 >= len(data) || data[i+1] != 'u' {
				i++
				continue
			}
			unit, err := jsonUnicodeCodeUnit(data, i+2)
			if err != nil {
				return err
			}
			if unit >= 0xD800 && unit <= 0xDBFF {
				if i+12 > len(data) || data[i+6] != 0x5c || data[i+7] != 'u' {
					return fmt.Errorf("unpaired high surrogate")
				}
				low, err := jsonUnicodeCodeUnit(data, i+8)
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return fmt.Errorf("unpaired high surrogate")
				}
				i += 11
			} else if unit >= 0xDC00 && unit <= 0xDFFF {
				return fmt.Errorf("unpaired low surrogate")
			} else {
				i += 5
			}
		}
	}
	return nil
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
