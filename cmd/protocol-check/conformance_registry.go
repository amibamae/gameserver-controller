package main

import "fmt"

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
