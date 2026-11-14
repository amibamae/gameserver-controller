package main

import (
	"reflect"
	"testing"
)

const sampleDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3,0 +4,2 @@ func A() {
+	x := 1
+	_ = x
@@ -10 +12 @@ func B() {
-	old()
+	new()
@@ -20,3 +21,0 @@ func C() {
diff --git a/gone.go b/gone.go
--- a/gone.go
+++ /dev/null
@@ -1,3 +0,0 @@
`

func TestParseUnifiedDiff(t *testing.T) {
	got, err := parseUnifiedDiff([]byte(sampleDiff))
	if err != nil {
		t.Fatal(err)
	}
	want := changedLines{"a.go": {4: true, 5: true, 12: true, 21: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed = %v, want %v", got, want)
	}
	if !got.touched("a.go", 11, 13) || got.touched("a.go", 6, 11) || got.touched("other.go", 1, 100) {
		t.Fatal("touched does not match the changed lines")
	}
}

func TestParseUnifiedDiffRejectsBadHunks(t *testing.T) {
	for _, header := range []string{"@@ -1 1 @@", "@@ -1 +x @@", "@@ -1 +1,y @@", "@@ -1"} {
		if _, err := parseUnifiedDiff([]byte("+++ b/a.go\n" + header + "\n")); err == nil {
			t.Errorf("accepted %q", header)
		}
	}
}
