package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the error each package check reports, so that splitting
// the checks into steps cannot change which problem a broken package gets.

const draft202012 = "https://json-schema.org/draft/2020-12/schema"

// schemaRoot writes a package root holding only protocol/schemas with the
// given index and files.
func schemaRoot(t *testing.T, index string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	schemas := filepath.Join(root, "protocol", "schemas")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemas, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(schemas, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func indexWith(entries string) string {
	return `{"protocol_version":"9.9.9-test","json_schema_dialect":"` + draft202012 + `","schemas":[` + entries + `]}`
}

func TestLoadPackageReportsEachIndexProblem(t *testing.T) {
	schema := func(id string) string { return `{"$schema":"` + draft202012 + `","$id":"` + id + `","type":"object"}` }
	for _, test := range []struct {
		name, index string
		files       map[string]string
		want        string
	}{
		{"index not an object", `[]`, nil, "protocol/schemas/index.json must be a JSON object"},
		{"no protocol version", `{"json_schema_dialect":"` + draft202012 + `","schemas":[]}`, nil, "schema index must declare protocol_version"},
		{"wrong dialect", `{"protocol_version":"1","json_schema_dialect":"draft-07","schemas":[]}`, nil, "schema index must select JSON Schema Draft 2020-12"},
		{"no schemas", indexWith(``), nil, "schema index must contain a non-empty schemas array"},
		{"entry not an object", indexWith(`1`), nil, "schema index entries must be objects"},
		{"entry without id", indexWith(`{"path":"a.json"}`), nil, "schema index entry is missing path or id"},
		{"unsafe path", indexWith(`{"path":"../a.json","id":"urn:a"}`), nil, "unsafe schema path in index: ../a.json"},
		{"duplicate path", indexWith(`{"path":"a.json","id":"urn:a"},{"path":"a.json","id":"urn:b"}`), map[string]string{"a.json": schema("urn:a")}, "duplicate schema path in index: a.json"},
		{"missing file", indexWith(`{"path":"a.json","id":"urn:a"}`), nil, "read indexed schema a.json"},
		{"schema not an object", indexWith(`{"path":"a.json","id":"urn:a"}`), map[string]string{"a.json": `[]`}, "a.json must be a JSON object"},
		{"id mismatch", indexWith(`{"path":"a.json","id":"urn:a"}`), map[string]string{"a.json": schema("urn:other")}, "a.json: index id does not match schema $id"},
		{"wrong schema dialect", indexWith(`{"path":"a.json","id":"urn:a"}`), map[string]string{"a.json": `{"$schema":"draft-07","$id":"urn:a"}`}, "a.json: schema must declare Draft 2020-12"},
		{"duplicate id", indexWith(`{"path":"a.json","id":"urn:a"},{"path":"b.json","id":"urn:a"}`), map[string]string{"a.json": schema("urn:a"), "b.json": schema("urn:a")}, "duplicate schema $id: urn:a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadPackage(schemaRoot(t, test.index, test.files))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadPackage error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := loadPackage(t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "read schema index:") {
		t.Fatalf("missing index error = %v", err)
	}
}

func TestParseJSONReportsEachSyntaxProblem(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{`{"a":1,"a":2}`, "duplicate JSON object member: a"},
		{`{"a":1`, "invalid JSON"},
		{`[1,2`, "invalid JSON"},
		{`]`, "invalid JSON"},
		{`{"a":1} 2`, "trailing data after JSON value"},
		{`{"a":1} }`, "invalid trailing JSON"},
		{`1e999`, "invalid JSON number"},
	} {
		_, err := parseJSON([]byte(test.input), "test.json")
		if err == nil || !strings.Contains(err.Error(), test.want) || !strings.HasPrefix(err.Error(), "test.json: ") {
			t.Errorf("parseJSON(%s) = %v, want %q", test.input, err, test.want)
		}
	}
	value, err := parseJSON([]byte(`{"a":[],"b":[1,{"c":null}],"d":"x"}`), "ok.json")
	if err != nil {
		t.Fatal(err)
	}
	object := asObject(value)
	if empty, ok := object["a"].([]any); !ok || empty != nil {
		t.Fatalf("an empty array must decode as a nil []any, got %#v", object["a"])
	}
	if nested := object["b"].([]any); len(nested) != 2 || asObject(nested[1])["c"] != nil {
		t.Fatalf("nested values = %#v", nested)
	}
}

// packageCopy loads the real package, then points it at a temporary root
// whose protocol/schemas links to the real schemas, so one file under
// protocol/ can be replaced for a test.
func packageCopy(t *testing.T) (*protocolPackage, string) {
	t.Helper()
	pkg, err := loadPackage(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	real := pkg.root
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "protocol"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "protocol", "schemas"), filepath.Join(root, "protocol", "schemas")); err != nil {
		t.Fatal(err)
	}
	pkg.root = root
	return pkg, real
}

func TestValidateOpenAPIReportsEachReferenceProblem(t *testing.T) {
	pkg, real := packageCopy(t)
	original, err := os.ReadFile(filepath.Join(real, "protocol", "management-api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pkg.root, "protocol", "management-api")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	canonical := `$ref: "../schemas/operations/operation.schema.json"`
	for _, test := range []struct{ name, old, new, want string }{
		{"wrong version", "openapi: 3.2.1", "openapi: 3.1.0", "OpenAPI document must declare 3.2.1"},
		{"component not canonical", canonical, `$ref: "../schemas/lifecycle/server.schema.json"`, "OpenAPI component Operation must reference the canonical schema"},
		{"reference escapes repository", canonical, canonical + "\n      x-escape:\n        $ref: ../../../../outside.json", "OpenAPI reference escapes the repository"},
		{"missing target", canonical, canonical + "\n      x-missing:\n        $ref: ../schemas/missing.schema.json", "OpenAPI reference target does not exist"},
		{"target outside schemas", canonical, canonical + "\n      x-outside:\n        $ref: ./openapi.yaml", "OpenAPI external reference must point to the local schema package"},
		{"unindexed schema", canonical, canonical + "\n      x-unindexed:\n        $ref: ../schemas/index.json", "OpenAPI reference target is not registered in schemas/index.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := string(original)
			if !strings.Contains(text, test.old) {
				t.Fatalf("fixture text %q not found", test.old)
			}
			text = strings.Replace(text, test.old, test.new, 1)
			if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := pkg.validateOpenAPI(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateOpenAPI error = %v, want %q", err, test.want)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pkg.validateOpenAPI(); err != nil {
		t.Fatalf("unchanged document: %v", err)
	}
}

func TestValidateExamplesReportsEachProblem(t *testing.T) {
	pkg, real := packageCopy(t)
	source := filepath.Join(real, "protocol", "examples")
	dir := filepath.Join(pkg.root, "protocol", "examples")
	copyExamples := func(t *testing.T) {
		t.Helper()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(source, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, entry.Name()), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	descriptor := descriptorExample(t, source)
	for _, test := range []struct {
		name   string
		change func(t *testing.T)
		want   string
	}{
		{"unlisted example", func(t *testing.T) { write(t, filepath.Join(dir, "zz-unlisted.json"), `{}`) }, "example is not listed in examples/index.json: zz-unlisted.json"},
		{"listed example missing", func(t *testing.T) { remove(t, filepath.Join(dir, descriptor)) }, "mapped example does not exist: " + descriptor},
		{"capability not an object", func(t *testing.T) { replaceCapabilities(t, filepath.Join(dir, descriptor), `[1]`) }, descriptor + ": capability 0 must be an object"},
		{"schema URI not a string", func(t *testing.T) {
			replaceCapabilities(t, filepath.Join(dir, descriptor), `[{"input_schema_uri":1}]`)
		}, descriptor + ": capability 0 input_schema_uri must be a string"},
		{"schema URI not local", func(t *testing.T) {
			replaceCapabilities(t, filepath.Join(dir, descriptor), `[{"input_schema_uri":"urn:missing","output_schema_uri":"urn:missing"}]`)
		}, descriptor + ": capability 0 input_schema_uri does not resolve in the local schema registry"},
		{"example breaks its schema", func(t *testing.T) { write(t, filepath.Join(dir, descriptor), `{}`) }, descriptor + ": "},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyExamples(t)
			test.change(t)
			if err := pkg.validateExamples(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateExamples error = %v, want %q", err, test.want)
			}
		})
	}
	copyExamples(t)
	if err := pkg.validateExamples(); err != nil {
		t.Fatalf("unchanged examples: %v", err)
	}
}

// descriptorExample returns the first indexed example with a capabilities
// array.
func descriptorExample(t *testing.T, dir string) string {
	t.Helper()
	listed, err := readExampleIndex(filepath.Join(dir, exampleIndexFile))
	if err != nil {
		t.Fatal(err)
	}
	for name := range listed {
		if !strings.HasPrefix(name, "adapter-descriptor.") {
			continue
		}
		doc, err := parseJSONFile(filepath.Join(dir, name))
		if err == nil && asObject(doc)["capabilities"] != nil {
			return name
		}
	}
	t.Fatal("no adapter descriptor example with capabilities")
	return ""
}

func replaceCapabilities(t *testing.T, path, capabilities string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, `"capabilities"`)
	if start < 0 {
		t.Fatal("no capabilities member")
	}
	open := strings.Index(text[start:], "[") + start
	depth, end := 0, -1
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			end = i
			break
		}
	}
	write(t, path, text[:open]+capabilities+text[end+1:])
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
