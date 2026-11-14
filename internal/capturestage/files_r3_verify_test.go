package capturestage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// These characterization tests pin that every rejected reopen reports
// exactly ErrInvalid, returns nothing, leaves no descriptor open, and leaves
// staging untouched; and what a verified reopen returns.

func storageR3Rewrite(t *testing.T, record string, edit func(map[string]any)) {
	t.Helper()
	encoded, err := os.ReadFile(record)
	storageR3Must(t, err)
	var value map[string]any
	storageR3Must(t, json.Unmarshal(encoded, &value))
	edit(value)
	encoded, err = json.Marshal(value)
	storageR3Must(t, err)
	storageR3Replace(t, record, encoded)
}

// storageR3Replace swaps the record for new bytes. The record is hard-linked
// to the stage receipt, so only the link is replaced.
func storageR3Replace(t *testing.T, record string, encoded []byte) {
	t.Helper()
	storageR3Must(t, os.Remove(record))
	storageR3Must(t, os.WriteFile(record, encoded, 0600))
}

func storageR3RecordEdit(edit func(map[string]any)) func(*testing.T, string, string) {
	return func(t *testing.T, record, _ string) { storageR3Rewrite(t, record, edit) }
}

func storageR3ReceiptEdit(field string, value any) func(*testing.T, string, string) {
	return storageR3RecordEdit(func(v map[string]any) { v["Receipt"].(map[string]any)[field] = value })
}

func storageR3DataEdit(content string) func(*testing.T, string, string) {
	return func(t *testing.T, _, dir string) {
		data := filepath.Join(dir, "data")
		storageR3Must(t, os.Chmod(data, 0600))
		storageR3Must(t, os.WriteFile(data, []byte(content), 0600))
	}
}

type storageR3VerifyCase struct {
	name  string
	files func(*Files) *Files
	ctx   func() context.Context
	scope func(*capturestream.Scope)
	setup func(t *testing.T, record, dir string)
}

func (c storageR3VerifyCase) run(t *testing.T) {
	f, record, dir := storageR3Sealed(t)
	if c.setup != nil {
		c.setup(t, record, dir)
	}
	before := storageR3Tree(t, f.root)
	target := f
	if c.files != nil {
		target = c.files(f)
	}
	scope := testScope()
	if c.scope != nil {
		c.scope(&scope)
	}
	descriptors := storageR3FDs(t)
	file, receipt, output, err := target.openVerifiedCapture(storageR3Background(c.ctx), scope)
	storageR3Is(t, "openVerifiedCapture", err, ErrInvalid)
	storageR3Same(t, "file", file, (*os.File)(nil))
	storageR3Same(t, "receipt", receipt, capturestream.Receipt{})
	storageR3Same(t, "output", output, json.RawMessage(nil))
	storageR3Same(t, "descriptors left open", storageR3FDs(t)-descriptors, 0)
	storageR3Same(t, "staging after a rejected reopen", storageR3Tree(t, f.root), before)
	legacy, legacyReceipt, legacyErr := target.OpenVerified(storageR3Background(c.ctx), scope)
	storageR3Is(t, "OpenVerified", legacyErr, ErrInvalid)
	storageR3Same(t, "OpenVerified result", []any{legacy, legacyReceipt}, []any{(*os.File)(nil), capturestream.Receipt{}})
	reader, publicReceipt, publicOutput, publicErr := target.OpenVerifiedCapture(storageR3Background(c.ctx), scope)
	storageR3Is(t, "OpenVerifiedCapture", publicErr, ErrInvalid)
	storageR3Same(t, "OpenVerifiedCapture result", []any{reader, publicReceipt, publicOutput}, []any{io.ReadCloser((*os.File)(nil)), capturestream.Receipt{}, json.RawMessage(nil)})
}

func storageR3RecordCases() []storageR3VerifyCase {
	digest := sha256.Sum256([]byte("{}"))
	return []storageR3VerifyCase{
		{name: "record public", setup: func(t *testing.T, record, _ string) { storageR3Must(t, os.Chmod(record, 0644)) }},
		{name: "record symlink", setup: func(t *testing.T, record, dir string) {
			storageR3Must(t, os.Remove(record))
			storageR3Must(t, os.Symlink(filepath.Join(dir, "receipt.json"), record))
		}},
		{name: "record directory", setup: func(t *testing.T, record, _ string) {
			storageR3Must(t, os.Remove(record))
			storageR3Must(t, os.Mkdir(record, 0700))
		}},
		{name: "record too large", setup: func(t *testing.T, record, _ string) { storageR3Replace(t, record, []byte(strings.Repeat(" ", 65537))) }},
		{name: "record not json", setup: func(t *testing.T, record, _ string) { storageR3Replace(t, record, []byte(`{"Version":`)) }},
		{name: "record unknown field", setup: storageR3RecordEdit(func(v map[string]any) { v["Extra"] = true })},
		{name: "record trailing value", setup: func(t *testing.T, record, _ string) {
			encoded, err := os.ReadFile(record)
			storageR3Must(t, err)
			storageR3Replace(t, record, append(encoded, []byte(" {}")...))
		}},
		{name: "record version 0", setup: storageR3RecordEdit(func(v map[string]any) { v["Version"] = 0 })},
		{name: "record version 3", setup: storageR3RecordEdit(func(v map[string]any) { v["Version"] = 3 })},
		{name: "record scope", setup: storageR3ReceiptEdit("Scope", map[string]any{"StepID": "other"})},
		{name: "directory prefix only", setup: storageR3RecordEdit(func(v map[string]any) { v["Directory"] = "capture-" })},
		{name: "directory other prefix", setup: storageR3RecordEdit(func(v map[string]any) { v["Directory"] = "staging-abc" })},
		{name: "directory separator", setup: storageR3RecordEdit(func(v map[string]any) { v["Directory"] = "capture-a/b" })},
		{name: "directory too long", setup: storageR3RecordEdit(func(v map[string]any) { v["Directory"] = "capture-" + strings.Repeat("a", 121) })},
		{name: "negative size", setup: storageR3ReceiptEdit("SizeBytes", -1)},
		{name: "size beyond exact integers", setup: storageR3ReceiptEdit("SizeBytes", 9007199254740992)},
		{name: "version 1 with output", setup: storageR3RecordEdit(func(v map[string]any) { v["Version"] = 1; delete(v, "output_sha256") })},
		{name: "version 1 with output digest", setup: storageR3RecordEdit(func(v map[string]any) { v["Version"] = 1; delete(v, "output") })},
		{name: "version 2 without output", setup: storageR3RecordEdit(func(v map[string]any) { delete(v, "output"); delete(v, "output_sha256") })},
		{name: "version 2 output digest", setup: storageR3RecordEdit(func(v map[string]any) { v["output_sha256"] = hex.EncodeToString(digest[:]) })},
		{name: "version 2 output altered", setup: storageR3RecordEdit(func(v map[string]any) { v["output"] = map[string]any{} })},
	}
}

func storageR3DataCases() []storageR3VerifyCase {
	return []storageR3VerifyCase{
		{name: "directory missing", setup: func(t *testing.T, _, dir string) { storageR3Must(t, os.RemoveAll(dir)) }},
		{name: "directory public", setup: func(t *testing.T, _, dir string) { storageR3Must(t, os.Chmod(dir, 0750)) }},
		{name: "directory symlink", setup: func(t *testing.T, _, dir string) {
			storageR3Must(t, os.Rename(dir, dir+"moved"))
			storageR3Must(t, os.Symlink(dir+"moved", dir))
		}},
		{name: "directory is a file", setup: func(t *testing.T, _, dir string) {
			storageR3Must(t, os.RemoveAll(dir))
			storageR3Must(t, os.WriteFile(dir, []byte("abc"), 0600))
		}},
		{name: "data missing", setup: func(t *testing.T, _, dir string) { storageR3Must(t, os.Remove(filepath.Join(dir, "data"))) }},
		{name: "data public", setup: func(t *testing.T, _, dir string) { storageR3Must(t, os.Chmod(filepath.Join(dir, "data"), 0444)) }},
		{name: "data symlink", setup: func(t *testing.T, _, dir string) {
			outside := filepath.Join(t.TempDir(), "outside")
			storageR3Must(t, os.WriteFile(outside, []byte("abc"), 0600))
			storageR3Must(t, os.Remove(filepath.Join(dir, "data")))
			storageR3Must(t, os.Symlink(outside, filepath.Join(dir, "data")))
		}},
		{name: "data short", setup: storageR3DataEdit("ab")},
		{name: "data long", setup: storageR3DataEdit("abcd")},
		{name: "data altered", setup: storageR3DataEdit("abd")},
		{name: "canceled while hashing", ctx: func() context.Context { return storageR3CancelAfter(1) }},
		// The entry check, two reads of the three-byte data, then the
		// post-copy check.
		{name: "canceled after hashing", ctx: func() context.Context { return storageR3CancelAfter(3) }},
	}
}

func TestStorageR3OpenVerifiedRejections(t *testing.T) {
	tests := []storageR3VerifyCase{
		{name: "nil files", files: func(*Files) *Files { return nil }},
		{name: "empty root", files: func(f *Files) *Files { return &Files{syncRoot: f.syncRoot} }},
		{name: "no sync", files: func(f *Files) *Files { return &Files{root: f.root} }},
		{name: "nil context", ctx: func() context.Context { return nil }},
		{name: "canceled context", ctx: storageR3Canceled},
		{name: "empty scope field", scope: func(s *capturestream.Scope) { s.DataSetID = "" }},
		{name: "long scope field", scope: func(s *capturestream.Scope) { s.CaptureID = strings.Repeat("c", 501) }},
		{name: "other scope", scope: func(s *capturestream.Scope) { s.CaptureID = "other" }},
	}
	tests = append(append(tests, storageR3RecordCases()...), storageR3DataCases()...)
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type storageR3VerifySuccess struct {
	name   string
	legacy bool
	output json.RawMessage
}

func (c storageR3VerifySuccess) run(t *testing.T) {
	f, record, _ := storageR3Sealed(t)
	if c.legacy {
		storageR3Rewrite(t, record, func(v map[string]any) {
			v["Version"] = 1
			delete(v, "output")
			delete(v, "output_sha256")
		})
	}
	before := storageR3Tree(t, f.root)
	descriptors := storageR3FDs(t)
	file, receipt, output, err := f.openVerifiedCapture(context.Background(), testScope())
	storageR3Must(t, err)
	t.Cleanup(func() { storageR3Must(t, file.Close()) })
	storageR3Same(t, "descriptors held", storageR3FDs(t)-descriptors, 1)
	info, err := file.Stat()
	storageR3Must(t, err)
	offset, err := file.Seek(0, io.SeekCurrent)
	storageR3Must(t, err)
	data, err := io.ReadAll(file)
	storageR3Must(t, err)
	storageR3Same(t, "verified result", []any{offset, string(data), info.Mode(), receipt, output},
		[]any{int64(0), "abc", os.FileMode(0400), testReceipt(testScope(), "abc"), c.output})
	storageR3Same(t, "staging after a verified reopen", storageR3Tree(t, f.root), before)
}

func TestStorageR3OpenVerifiedSuccess(t *testing.T) {
	tests := []storageR3VerifySuccess{
		{name: "legacy", legacy: true, output: json.RawMessage(nil)},
		{name: "current", output: json.RawMessage(testOutput())},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestStorageR3SafeDirectory(t *testing.T) {
	for name, want := range map[string]bool{
		"capture-a": true, "capture-AZaz09": true, "capture-" + strings.Repeat("a", 120): true,
		"capture-": false, "capture-" + strings.Repeat("a", 121): false, "capture-a_b": false,
		"capture-a.b": false, "capture-é": false, "Capture-a": false, "../capture-a": false,
	} {
		storageR3Same(t, "safeDirectory("+name+")", safeDirectory(name), want)
	}
}
