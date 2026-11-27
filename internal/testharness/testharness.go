// Package testharness indexes the repository's test harness packages. Its
// tests fail when a harness lacks the label, a labeled package is missing
// from the index, production code imports a harness, or a production package
// declares an exported Fake or Scripted name. See
// docs/TEST_HARNESSES.md.
package testharness

// Label is the line every test harness package carries in its package doc.
const Label = "TEST HARNESS: for tests only. Production code must not import this package."

// Packages lists every test harness, as import paths relative to the module.
// Add a new harness here and to docs/TEST_HARNESSES.md.
var Packages = []string{
	"internal/fakeadapter",
	"internal/simulation",
}
