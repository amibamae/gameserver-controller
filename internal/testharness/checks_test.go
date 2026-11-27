package testharness

import (
	"slices"
	"testing"
)

const testModule = "example.com/m"

func TestLabelProblemsReportsUnlabeledAndUnlistedPackages(t *testing.T) {
	sources := []goSource{
		{dir: "internal/listed", doc: "Package listed.\n\n" + Label + "\n"},
		{dir: "internal/unlabeled", doc: "Package unlabeled does things.\n"},
		{dir: "internal/stray", doc: Label + "\n"},
	}
	got := labelProblems(sources, []string{"internal/listed", "internal/unlabeled"})
	want := []string{
		"internal/stray: labeled as a test harness but missing from testharness.Packages",
		"internal/unlabeled: package doc lacks the line \"" + Label + "\"",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

func TestImportProblemsAllowsHarnessesButNotProductionCode(t *testing.T) {
	sources := []goSource{
		{dir: "internal/fake", imports: []string{testModule + "/internal/sim"}},
		{dir: "cmd/server", imports: []string{"fmt", testModule + "/internal/fake", testModule + "/internal/fakeish"}},
	}
	got := importProblems(sources, testModule, []string{"internal/fake", "internal/sim"})
	want := []string{"cmd/server imports test harness internal/fake; only _test.go files and other harnesses may"}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}

func TestDirectoryAndFileFilters(t *testing.T) {
	for name, want := range map[string]bool{"vendor": true, ".github": true, "graphify-out": true, "internal": false} {
		if skipped := skipDir(name, true) != nil; skipped != want {
			t.Errorf("skipDir(%q) = %v, want %v", name, skipped, want)
		}
	}
	if skipDir(".", false) != nil {
		t.Error("the module root itself must be walked")
	}
	for name, want := range map[string]bool{"a.go": true, "a_test.go": false, "a.md": false} {
		if got := productionGoFile(name); got != want {
			t.Errorf("productionGoFile(%q) = %v, want %v", name, got, want)
		}
	}
}
