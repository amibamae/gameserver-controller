package main

import (
	"path/filepath"
	"testing"
)

// Exercise both authorized routes with a retained tombstone. The published
// operation-level vector covers the tombstone itself; this test checks the
// management HTTP decision layer's mapping and rejection of acceptance.
func TestManagementSubmitExpiredKey(t *testing.T) {
	path := filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "management-operations-submit-decision.json")
	raw, err := parseJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := asObject(raw)
	input := asObject(fixture["input"])
	cases := make([]any, 0, 2)
	for _, rawCase := range arrayValue(input, "evaluations") {
		c := asObject(rawCase)
		if stringValue(c, "prior_use") == "same_request" {
			c["prior_use"] = "expired_key"
			cases = append(cases, c)
		}
	}
	if len(cases) != 2 {
		t.Fatalf("expected two authorized replay cases, got %d", len(cases))
	}
	input["evaluations"] = cases
	result, err := evaluateManagementSubmitDecision(nil, fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, rawResult := range arrayValue(result, "evaluations") {
		c := asObject(rawResult)
		expected := map[string]any{
			"status":     jsonInt(409),
			"error_code": "core.operation.idempotency_key_expired",
			"accepted":   false,
			"replayed":   false,
		}
		if err := compareExpected(c, expected, "$"); err != nil {
			t.Errorf("%s: %v", stringValue(c, "case_id"), err)
		}
	}
}
