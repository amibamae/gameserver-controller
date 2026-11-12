package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// writeExamplePackage creates a package root whose protocol/examples holds
// index (as examples/index.json) and the named example files.
func writeExamplePackage(t *testing.T, index string, files ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "protocol", "examples")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exampleIndexFile), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadExampleIndexRejectsMalformedEntries(t *testing.T) {
	tests := []struct {
		name  string
		index string
		want  string
	}{
		{"not an object", `[]`, "non-empty examples array"},
		{"no examples", `{"examples":[]}`, "non-empty examples array"},
		{"missing path", `{"examples":[{"schema_id":"urn:x"}]}`, "empty or duplicated"},
		{"missing schema", `{"examples":[{"path":"a.json"}]}`, "empty or duplicated"},
		{"repeated path", `{"examples":[{"path":"a.json","schema_id":"urn:x"},{"path":"a.json","schema_id":"urn:y"}]}`, `"a.json" is empty or duplicated`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeExamplePackage(t, test.index)
			_, err := readExampleIndex(filepath.Join(root, "protocol", "examples", exampleIndexFile))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readExampleIndex error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExamplesRequiresIndexToMatchFiles(t *testing.T) {
	const oneEntry = `{"examples":[{"path":"a.json","schema_id":"urn:test:a"}]}`
	tests := []struct {
		name  string
		index string
		files []string
		want  string
	}{
		{"example file missing from the index", oneEntry, []string{"a.json", "b.json"}, "example is not listed in examples/index.json: b.json"},
		{"indexed example file missing", oneEntry, nil, "mapped example does not exist: a.json"},
		{"index names an unknown schema", oneEntry, []string{"a.json"}, "mapped schema is missing from index.json: urn:test:a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg := &protocolPackage{root: writeExamplePackage(t, test.index, test.files...), validators: map[string]*jsonschema.Schema{}}
			if err := pkg.validateExamples(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateExamples error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExamplesCountsPublishedExamples(t *testing.T) {
	pkg, err := loadPackage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := pkg.validateExamples(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "examples", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := len(files) - 1; pkg.exampleCount != want || want == 0 {
		t.Fatalf("exampleCount = %d, want %d (every example file except %s)", pkg.exampleCount, want, exampleIndexFile)
	}
}
