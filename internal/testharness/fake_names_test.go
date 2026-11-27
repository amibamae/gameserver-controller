package testharness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// testDoublePrefixes mark exported names that belong to test doubles.
var testDoublePrefixes = []string{"Fake", "Scripted"}

func TestProductionCodeDeclaresNoTestDoubles(t *testing.T) {
	for _, problem := range fakeProblems(moduleSources(t), Packages) {
		t.Error(problem)
	}
}

// fakeProblems reports exported test-double names declared by non-test files
// outside the harnesses.
func fakeProblems(sources []goSource, harnesses []string) []string {
	var problems []string
	for _, source := range sources {
		if slices.Contains(harnesses, source.dir) {
			continue
		}
		for _, name := range source.fakes {
			problems = append(problems, source.dir+" declares test double "+name+
				"; production packages must not define fakes: move it to a labelled test harness or a _test.go file")
		}
	}
	return problems
}

// fakeDeclarations returns the exported top-level names in file, methods
// included, that start with a test-double prefix.
func fakeDeclarations(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		names = append(names, declNames(decl)...)
	}
	return slices.DeleteFunc(names, func(name string) bool { return !testDoubleName(name) })
}

func declNames(decl ast.Decl) []string {
	switch decl := decl.(type) {
	case *ast.FuncDecl:
		return []string{decl.Name.Name}
	case *ast.GenDecl:
		return specNames(decl.Specs)
	default:
		return nil
	}
}

func specNames(specs []ast.Spec) []string {
	var names []string
	for _, spec := range specs {
		switch spec := spec.(type) {
		case *ast.TypeSpec:
			names = append(names, spec.Name.Name)
		case *ast.ValueSpec:
			for _, name := range spec.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

func testDoubleName(name string) bool {
	hasPrefix := func(prefix string) bool { return strings.HasPrefix(name, prefix) }
	return ast.IsExported(name) && slices.ContainsFunc(testDoublePrefixes, hasPrefix)
}

func TestFakeDeclarationsFindsExportedTestDoubleNames(t *testing.T) {
	const src = `package p
import "fmt"
type FakeVultr struct{}
type fakeLocal struct{}
type Real struct{}
func (Real) FakeReply() {}
func NewFakeVultr() *FakeVultr { return nil }
func ScriptedReply() {}
var (
	FakeDefault, Other = 1, 2
	scriptedHidden     = 3
)
const ScriptedLimit = 4
var _ = fmt.Sprint
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := fakeDeclarations(file)
	want := []string{"FakeVultr", "FakeReply", "ScriptedReply", "FakeDefault", "ScriptedLimit"}
	if !slices.Equal(got, want) {
		t.Fatalf("fakeDeclarations = %q, want %q", got, want)
	}
}

func TestFakeProblemsAllowsHarnessesButNotProductionCode(t *testing.T) {
	sources := []goSource{
		{dir: "internal/fake", fakes: []string{"FakeAdapter"}},
		{dir: "internal/vultrhosting", fakes: []string{"FakeVultr"}},
		{dir: "internal/clean"},
	}
	got := fakeProblems(sources, []string{"internal/fake"})
	want := []string{"internal/vultrhosting declares test double FakeVultr; production packages must not define fakes: move it to a labelled test harness or a _test.go file"}
	if !slices.Equal(got, want) {
		t.Fatalf("problems = %q, want %q", got, want)
	}
}
