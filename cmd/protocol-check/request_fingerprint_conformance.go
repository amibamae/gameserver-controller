package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/gowebpki/jcs"
)

func evaluateRequestFingerprintJCS(fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	results := make(map[string]any)
	for _, rawCase := range arrayValue(input, "cases") {
		conformanceCase := asObject(rawCase)
		caseID := stringValue(conformanceCase, "case_id")
		parametersJSON := stringValue(conformanceCase, "parameters_json")

		parsed, parseErr := parseJSON([]byte(parametersJSON), caseID)
		_, objectInput := parsed.(map[string]any)
		accepted := parseErr == nil && objectInput
		canonicalJSON := ""
		fingerprint := ""
		if accepted {
			canonical, err := jcs.Transform([]byte(parametersJSON))
			if err != nil {
				accepted = false
			} else {
				digest := sha256.Sum256(canonical)
				canonicalJSON = string(canonical)
				fingerprint = "sha256:" + hex.EncodeToString(digest[:])
			}
		}
		if caseID == "" {
			return nil, fmt.Errorf("request fingerprint fixture has a case without case_id")
		}
		results[caseID] = map[string]any{
			"accepted":       accepted,
			"canonical_json": canonicalJSON,
			"fingerprint":    fingerprint,
		}
	}
	return map[string]any{"cases": results}, nil
}
