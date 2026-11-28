package testharness

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// skippedDirs hold no first-party Go packages.
var skippedDirs = map[string]bool{".git": true, "graphify-out": true, "node_modules": true, "testdata": true, "vendor": true}

// goSource is one non-test Go file: its package directory relative to the
// module root, its package doc, its imports, and the exported top-level
// names it declares with a test-double prefix (see fakeDeclarations).
type goSource struct {
	dir     string
	doc     string
	imports []string
	fakes   []string
}

func TestEveryListedHarnessIsLabeledAndEveryLabeledPackageIsListed(t *testing.T) {
	sources := moduleSources(t)
	for _, problem := range labelProblems(sources, Packages) {
		t.Error(problem)
	}
}

func TestProductionCodeDoesNotImportTestHarnesses(t *testing.T) {
	module, sources := modulePath(t), moduleSources(t)
	for _, problem := range importProblems(sources, module, Packages) {
		t.Error(problem)
	}
}

func TestDocsIndexEveryHarness(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "docs", "TEST_HARNESSES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range Packages {
		if !strings.Contains(string(data), "`"+pkg+"`") {
			t.Errorf("docs/TEST_HARNESSES.md does not list `%s`", pkg)
		}
	}
}

// labelProblems reports listed harnesses without the label in their package
// doc, and labeled packages missing from the list.
func labelProblems(sources []goSource, listed []string) []string {
	labeled := map[string]bool{}
	for _, source := range sources {
		if strings.Contains(source.doc, Label) {
			labeled[source.dir] = true
		}
	}
	var problems []string
	for _, pkg := range listed {
		if !labeled[pkg] {
			problems = append(problems, pkg+": package doc lacks the line \""+Label+"\"")
		}
	}
	for dir := range labeled {
		if !slices.Contains(listed, dir) {
			problems = append(problems, dir+": labeled as a test harness but missing from testharness.Packages")
		}
	}
	slices.Sort(problems)
	return problems
}

// importProblems reports non-test files outside the harnesses that import a
// harness. Harnesses may import each other.
func importProblems(sources []goSource, module string, harnesses []string) []string {
	var problems []string
	for _, source := range sources {
		if slices.Contains(harnesses, source.dir) {
			continue
		}
		for _, imported := range source.imports {
			if harness, ok := strings.CutPrefix(imported, module+"/"); ok && slices.Contains(harnesses, harness) {
				problems = append(problems, source.dir+" imports test harness "+harness+"; only _test.go files and other harnesses may")
			}
		}
	}
	return problems
}

func moduleSources(t *testing.T) []goSource {
	t.Helper()
	root := moduleRoot(t)
	var sources []goSource
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return skipDir(entry.Name(), path != root)
		}
		if !productionGoFile(entry.Name()) {
			return nil
		}
		source, err := parseSource(root, path)
		sources = append(sources, source)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return sources
}

func skipDir(name string, nested bool) error {
	if nested && (skippedDirs[name] || strings.HasPrefix(name, ".")) {
		return filepath.SkipDir
	}
	return nil
}

func productionGoFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func parseSource(root, path string) (goSource, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return goSource{}, err
	}
	dir, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return goSource{}, err
	}
	source := goSource{dir: filepath.ToSlash(dir), fakes: fakeDeclarations(file)}
	if file.Doc != nil {
		source.doc = file.Doc.Text()
	}
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return goSource{}, err
		}
		source.imports = append(source.imports, imported)
	}
	return source, nil
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal(errors.New("go.mod not found above the test directory"))
		}
		dir = parent
	}
}

func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if module, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return module
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}
