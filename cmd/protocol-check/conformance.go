package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"slices"
	"strings"
)

// runConformance checks every published fixture and reports all failures
// together, one line per fixture file, in sorted filename order.
func (pkg *protocolPackage) runConformance() error {
	failures, err := pkg.checkAllFixtures()
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
}

// checkAllFixtures is a split-point for testing the fixture loading loop.
func (pkg *protocolPackage) checkAllFixtures() ([]string, error) {
	paths, err := pkg.globConformanceVectorFiles()
	if err != nil {
		return nil, err
	}

	sorter := &filenameSorter{}
	sorter.Sort(paths)

	var failures []string
	for _, path := range paths {
		fail, err := pkg.evaluateFixture(path)
		if err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		} else if !fail {
			failures = append(failures, filepath.Base(path)+": unknown failure")
		}
	}
	return failures, nil
}

// globConformanceVectorFiles returns all .json files in the conformance test-vectors dir.
func (pkg *protocolPackage) globConformanceVectorFiles() ([]string, error) {
	path := filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json")
	files, err := filepath.Glob(path)
	if err != nil {
		return nil, err
	}
	return files, nil
}

// filenameSorter sorts a file list by their basename.
type filenameSorter struct{}

func (f *filenameSorter) Sort(paths []string) {
	slices.SortFunc(paths, func(a, b string) int {
		aBase := filepath.Base(a)
		bBase := filepath.Base(b)
		if aBase < bBase {
			return -1
		}
		if aBase > bBase {
			return 1
		}
		return 0
	})
}

// evaluateFixture runs a single fixture through parse→evaluate→marshal→compare.
func (pkg *protocolPackage) evaluateFixture(path string) (bool, error) {
	fixture, err := pkg.parseJSONFile(path)
	if err != nil {
		return false, err
	}

	actual, err := evaluate(pkg, asObject(fixture))
	if err != nil {
		return false, err
	}

	encoded, err := json.Marshal(actual)
	if err != nil {
		return false, err
	}

	result, err := parseJSON(encoded, filepath.Base(path)+" result")
	if err != nil {
		return false, err
	}

	expectedValue := asObject(asObject(fixture)["expected"])

	err = compareExpected(result, expectedValue)
	if err != nil {
		return false, fmt.Errorf("%s", err.Error())
	}

	return true, nil
}

// parseJSONFile reads and parses a JSON file for the package. Uses parseJSON for error wrapping.
func (pkg *protocolPackage) parseJSONFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}

	fixture, err := parseJSON(data, path)
	if err != nil {
		return nil, err
	}

	result := asObject(fixture)
	if result == nil {
		return nil, fmt.Errorf("parse failed for %q", path)
	}

	return result, nil
}

// runEvaluator looks up and calls the evaluator registered for a fixture.
func (pkg *protocolPackage) runEvaluator(fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	evaluator, ok := evaluators[fixtureID]
	if !ok {
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
	return evaluator(pkg, fixture)
}

// evaluate is a thin wrapper that delegates to runEvaluator. Kept for compatibility
// with conformance files that call it directly without the package method receiver.
func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	return pkg.runEvaluator(fixture)
}

// compareExpected compares an actual result against an expected value at a given path.
func compareExpected(actual, expected any) error {
	actualMap := asObject(actual)
	expectedValue := asObject(expected)

	// Handle missing expected value case first
	if expectedValue == nil {
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("expected %v, got %v", expected, actual)
		}
		return nil
	}

	// Handle object comparison
	actualMap = asObject(actual)
	if actualMap == nil {
		return fmt.Errorf("expected an object")
	}

	keys := make([]string, 0, len(expectedValue))
	for key := range expectedValue {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value, ok := actualMap[key]
		if !ok {
			return fmt.Errorf("%s: missing expected value", key)
		}
		if err := compareExpected(value, expectedValue[key]); err != nil {
			return err
		}
	}

	return nil
}
