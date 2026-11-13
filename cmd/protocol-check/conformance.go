package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// runConformance checks every published fixture and reports all failures
// together, one line per fixture file, in file-name order.
func (pkg *protocolPackage) runConformance() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
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

// checkFixture evaluates one fixture file and compares the result, after a
// JSON round trip, with the fixture's expected section.
func (pkg *protocolPackage) checkFixture(path string) error {
	fixture, err := parseJSONFile(path)
	if err != nil {
		return err
	}
	actual, err := evaluate(pkg, asObject(fixture))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	normalized, err := parseJSON(encoded, filepath.Base(path)+" result")
	if err != nil {
		return err
	}
	return compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$")
}

func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	evaluator, ok := evaluators[fixtureID]
	if !ok {
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
	return evaluator(pkg, fixture)
}

func readinessAssertionBindsAtCompletion(assertion map[string]any, completedAt, serverID, environmentID string, instanceRef any) (bool, error) {
	if len(assertion) == 0 {
		return true, nil
	}
	completed, err := parseTime(completedAt)
	if err != nil {
		return false, err
	}
	observed, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return false, err
	}
	expires, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return false, err
	}
	assertedInstance := asObject(assertion["instance_ref"])
	bound := stringValue(assertion, "server_id") == serverID &&
		stringValue(assertion, "environment_id") == environmentID &&
		stringValue(assertedInstance, "environment_id") == environmentID &&
		reflect.DeepEqual(instanceRef, assertion["instance_ref"]) &&
		!completed.Before(observed) && completed.Before(expires)
	return bound, nil
}

func sameObjectRefSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet, rightSet := make(map[string]bool, len(left)), make(map[string]bool, len(right))
	for _, value := range left {
		encoded, err := json.Marshal(value)
		if err != nil || leftSet[string(encoded)] {
			return false
		}
		leftSet[string(encoded)] = true
	}
	for _, value := range right {
		encoded, err := json.Marshal(value)
		if err != nil || rightSet[string(encoded)] {
			return false
		}
		rightSet[string(encoded)] = true
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for key := range leftSet {
		if !rightSet[key] {
			return false
		}
	}
	return true
}

func sameStringSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]bool, len(left))
	for _, value := range left {
		str, ok := value.(string)
		if !ok {
			return false
		}
		set[str] = true
	}
	for _, value := range right {
		str, ok := value.(string)
		if !ok || !set[str] {
			return false
		}
	}
	return true
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func compareExpected(actual, expected any, path string) error {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap := asObject(actual)
		if actualMap == nil {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("%s.%s: missing expected value", path, key)
			}
			if err := compareExpected(value, expectedValue[key], path+"."+key); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s: expected %v, got %v", path, expected, actual)
		}
	}
	return nil
}
