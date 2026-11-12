package main

import (
	"encoding/hex"
	"fmt"
)

func evaluateRawJSONInteroperability(fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0)
	for _, raw := range arrayValue(asObject(fixture["input"]), "evaluations") {
		evaluation := asObject(raw)
		caseID := stringValue(evaluation, "case_id")
		data, err := hex.DecodeString(stringValue(evaluation, "wire_hex"))
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("%s: invalid raw-wire fixture encoding", caseID)
		}
		value, parseErr := parseJSON(data, caseID)
		result := map[string]any{"case_id": caseID, "accepted": parseErr == nil}
		if text, ok := value.(string); ok && parseErr == nil {
			result["parsed_string"] = text
		}
		results = append(results, result)
	}
	return map[string]any{"evaluations": results}, nil
}

func init() {
	registerEvaluator(withoutPackage(evaluateRawJSONInteroperability), "protocol.json.raw-wire-interoperability")
}
