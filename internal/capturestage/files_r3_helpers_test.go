package capturestage

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// Shared helpers for the staging characterization tests: a listing of every
// staged file with its mode, a context that cancels on a chosen Err call, and
// small assertions.

const (
	storageR3Open      = "capture-* drwx------\ncapture-*/data -rw-------"
	storageR3Unlinked  = "capture-* drwx------\ncapture-*/data -r--------\ncapture-*/receipt.json -rw-------"
	storageR3Published = "<key>.json -rw------- =receipt\ncapture-* drwx------\ncapture-*/data -r--------\ncapture-*/receipt.json -rw-------"
)

// storageR3Tree lists every entry under root with its mode. The random
// capture directory name is shown as "capture-*" and the receipt record as
// "<key>.json"; "=receipt" marks a record hard-linked to a stage receipt.
func storageR3Tree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	walkErr := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		line, lineErr := storageR3TreeLine(root, path)
		lines = append(lines, line)
		return lineErr
	})
	storageR3Must(t, walkErr)
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func storageR3TreeLine(root, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	name := storageR3TreeName(rel)
	line := name + " " + info.Mode().String()
	if name == "<key>.json" && storageR3LinkedReceipt(root, info) {
		line += " =receipt"
	}
	return line, nil
}

func storageR3TreeName(rel string) string {
	parts := strings.Split(rel, string(filepath.Separator))
	if strings.HasPrefix(parts[0], "capture-") {
		parts[0] = "capture-*"
	}
	if len(parts) == 1 && storageR3RecordName(parts[0]) {
		parts[0] = "<key>.json"
	}
	return strings.Join(parts, "/")
}

// storageR3RecordName reports a hex SHA-256 digest followed by ".json".
func storageR3RecordName(name string) bool {
	return len(name) == 69 && strings.HasSuffix(name, ".json")
}

func storageR3LinkedReceipt(root string, record os.FileInfo) bool {
	// Glob fails only for a malformed pattern, and this one is constant.
	receipts, _ := filepath.Glob(filepath.Join(root, "capture-*", "receipt.json"))
	for _, receipt := range receipts {
		other, err := os.Stat(receipt)
		if err == nil && os.SameFile(record, other) {
			return true
		}
	}
	return false
}

// storageR3Context reports cancellation from call number allowed+1 onwards.
type storageR3Context struct {
	context.Context
	allowed atomic.Int64
	calls   atomic.Int64
}

func storageR3CancelAfter(allowed int64) *storageR3Context {
	ctx := &storageR3Context{Context: context.Background()}
	ctx.allowed.Store(allowed)
	return ctx
}

func (c *storageR3Context) Err() error {
	if c.calls.Add(1) > c.allowed.Load() {
		return context.Canceled
	}
	return nil
}

func storageR3Canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func storageR3Background(build func() context.Context) context.Context {
	if build == nil {
		return context.Background()
	}
	return build()
}

func storageR3OpenStage(t *testing.T, f *Files, ctx context.Context) *stage {
	t.Helper()
	opened, err := f.Open(ctx, testScope())
	storageR3Must(t, err)
	return opened.(*stage)
}

func storageR3Stage(t *testing.T, f *Files, data string) *stage {
	t.Helper()
	s := storageR3OpenStage(t, f, context.Background())
	_, err := io.WriteString(s, data)
	storageR3Must(t, err)
	return s
}

// storageR3Sealed returns staging holding one sealed "abc" capture, the
// record path, and the stage directory.
func storageR3Sealed(t *testing.T) (*Files, string, string) {
	t.Helper()
	f := newFiles(t)
	s := storageR3Stage(t, f, "abc")
	storageR3Must(t, s.Seal(context.Background(), testReceipt(testScope(), "abc"), testOutput()))
	return f, filepath.Join(f.root, storageR3Key(t)), s.dir
}

func storageR3Key(t *testing.T) string {
	t.Helper()
	name, err := key(testScope())
	storageR3Must(t, err)
	return name
}

type storageR3State struct{ open, failed, published, sealed, discarded bool }

func storageR3StateOf(s *stage) storageR3State {
	return storageR3State{open: s.file != nil, failed: s.failed, published: s.published, sealed: s.sealed, discarded: s.discarded}
}

func storageR3Must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// storageR3Is checks that err is exactly want, not merely equal text.
func storageR3Is(t *testing.T, what string, err, want error) {
	t.Helper()
	if err != want {
		t.Fatalf("%s returned %v, want %v", what, err, want)
	}
}

func storageR3Same(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got %#v\nwant %#v", what, got, want)
	}
}

func storageR3FDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("open descriptors cannot be counted on this platform")
	}
	return len(entries)
}
