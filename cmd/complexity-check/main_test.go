package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree creates a directory with one Go file holding source, plus a
// test file and a testdata file that must be skipped by default.
func writeTree(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.go":               source,
		"a_test.go":          "package a\nfunc T(a, b, c, d, e bool) bool { return a && b && c && d && e }\n",
		"testdata/skip.go":   "package a\nfunc S(a, b, c, d, e bool) bool { return a && b && c && d && e }\n",
		".hidden/skip.go":    "package a\nfunc H(a, b, c, d, e bool) bool { return a && b && c && d && e }\n",
		"vendor/v/vendor.go": "package v\nfunc V(a, b, c, d, e bool) bool { return a && b && c && d && e }\n",
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runCheck(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunReportsOnlyProductionViolations(t *testing.T) {
	dir := writeTree(t, "package a\nfunc Wide(a, b, c, d, e bool) bool { return a && b && c && d && e }\nfunc Ok() {}\n")
	code, stdout, stderr := runCheck(t, dir)
	if code != 1 || strings.Count(stdout, "\n") != 1 || !strings.Contains(stdout, "a.go:2: Wide: 4 boolean operators") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "1 function(s) over the limits") {
		t.Fatalf("stderr %q", stderr)
	}
	if code, stdout, _ := runCheck(t, "-tests", dir); code != 1 || !strings.Contains(stdout, "a_test.go") {
		t.Fatalf("-tests: code %d, stdout %q", code, stdout)
	}
}

func TestRunPassesCleanAndAllowedCode(t *testing.T) {
	allowed := "package a\n// complexity-check:allow generated table\nfunc Wide(a, b, c, d, e bool) bool { return a && b && c && d && e }\n"
	code, stdout, stderr := runCheck(t, writeTree(t, allowed))
	if code != 0 || !strings.Contains(stdout, "allowed by complexity-check:allow") || stderr != "" {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, _, _ := runCheck(t, writeTree(t, "package a\nfunc Ok() {}\n")); code != 0 {
		t.Fatalf("clean code exited %d", code)
	}
}

func TestRunUsageErrors(t *testing.T) {
	if code, _, stderr := runCheck(t, "-max-complexity", "x"); code != 2 || stderr == "" {
		t.Fatalf("bad flag: code %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCheck(t, filepath.Join(t.TempDir(), "missing")); code != 2 || !strings.Contains(stderr, "complexity-check:") {
		t.Fatalf("missing path: code %d, stderr %q", code, stderr)
	}
	if code, _, _ := runCheck(t, "-h"); code != 0 {
		t.Fatalf("-h exited %d", code)
	}
	bad := writeTree(t, "package a\nfunc {\n")
	if code, _, stderr := runCheck(t, bad); code != 2 || !strings.Contains(stderr, "parse") {
		t.Fatalf("parse error: code %d, stderr %q", code, stderr)
	}
}

// TestRepositoryCodeOfThisCommand keeps the checker within its own limits.
func TestRepositoryCodeOfThisCommand(t *testing.T) {
	if code, stdout, stderr := runCheck(t, "-tests", "."); code != 0 {
		t.Fatalf("complexity-check is over its own limits:\n%s%s", stdout, stderr)
	}
}

func TestRunRequiresAnExceptionReason(t *testing.T) {
	for _, comment := range []string{"complexity-check:allow", "mentions complexity-check:allow a reason"} {
		source := "package a\n// " + comment + "\nfunc Wide(a, b, c, d, e bool) bool { return a && b && c && d && e }\n"
		code, stdout, stderr := runCheck(t, writeTree(t, source))
		if code != 1 || strings.Contains(stdout, "allowed by") || !strings.Contains(stderr, "over the limits") {
			t.Fatalf("comment %q: code %d, stdout %q, stderr %q", comment, code, stdout, stderr)
		}
	}
}
