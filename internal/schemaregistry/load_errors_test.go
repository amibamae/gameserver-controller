package schemaregistry

import (
	"testing"
	"testing/fstest"
)

func TestLoadFromFSPreservesValidationOrder(t *testing.T) {
	const indexPrefix = `{"protocol_version":"test","json_schema_dialect":"https://json-schema.org/draft/2020-12/schema","schemas":[`
	const entry = `{"path":"a.schema.json","id":"urn:test:a"}`
	const schema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:test:a","$ref":"urn:test:missing"}`
	for _, test := range []struct {
		name, entries, extraFile, want string
	}{
		{"duplicate path before conflicting ID", entry + `,{"path":"a.schema.json","id":"urn:test:other"}`, "",
			`duplicate schema path "a.schema.json"`},
		{"schema metadata before duplicate ID", entry + `,{"path":"b.schema.json","id":"urn:test:a"}`, `{}`,
			`schema "b.schema.json" has an ID or dialect that does not match the index`},
		{"inventory before unresolved reference", entry, `{}`,
			`schema file "b.schema.json" is absent from index.json`},
		{"unresolved reference", entry, "",
			`schema "urn:test:a" has an unresolved local reference "urn:test:missing"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fstest.MapFS{
				"index.json":    {Data: []byte(indexPrefix + test.entries + `]}`)},
				"a.schema.json": {Data: []byte(schema)},
			}
			if test.extraFile != "" {
				root["b.schema.json"] = &fstest.MapFile{Data: []byte(test.extraFile)}
			}
			if _, err := LoadFromFS(root); err == nil || err.Error() != test.want {
				t.Fatalf("LoadFromFS error = %v, want %q", err, test.want)
			}
		})
	}
}
