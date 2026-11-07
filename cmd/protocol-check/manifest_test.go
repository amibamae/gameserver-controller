package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDigestPackageFilesGolden(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{"a.txt": "hello\n", "z.txt": ""} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// SHA-256 over a.txt NUL sha256("hello\\n") LF z.txt NUL sha256("") LF.
	const want = "sha256:f82e658a03d1872f1df56dd31b5714ac8e02992deaa5e286db880ed7d7bef2f4"
	got, err := digestPackageFiles(root, []string{"z.txt", "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := digestPackageFiles(root, []string{"a.txt", "z.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if changed == got {
		t.Fatal("line ending change did not change the digest")
	}
	if _, err := digestPackageFiles(root, []string{"missing.txt"}); err == nil {
		t.Fatal("missing package file did not fail")
	}
}

func TestPackageManifestFileSelection(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"protocol/schemas/index.json":                 "{}",
		"protocol/schemas/test.json":                  "{}",
		"protocol/conformance/test-vectors/test.json": "{}",
		"protocol/conformance/package-manifest.json":  "{}",
		"protocol/conformance/requirements.md":        "draft",
	} {
		path = filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg := &protocolPackage{root: root, version: "test-draft", entries: []schemaEntry{{Path: "test.json"}}}
	before, err := pkg.packageManifest()
	if err != nil {
		t.Fatal(err)
	}
	if before.SchemaCount != 1 || before.FixtureCount != 1 || before.ProtocolVersion != "test-draft" {
		t.Fatalf("unexpected manifest metadata: %+v", before)
	}
	if err := os.WriteFile(filepath.Join(root, "protocol/conformance/package-manifest.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := pkg.packageManifest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("manifest included itself")
	}
	if err := os.WriteFile(filepath.Join(root, "protocol/conformance/test-vectors/test.json"), []byte("{ }"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err = pkg.packageManifest()
	if err != nil {
		t.Fatal(err)
	}
	if before.FixtureSuiteDigest == after.FixtureSuiteDigest || before.SchemaRegistryDigest != after.SchemaRegistryDigest {
		t.Fatal("fixture edit must change only the fixture digest")
	}
}
