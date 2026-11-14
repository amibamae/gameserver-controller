package capturestage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/captureinvoke"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// These characterization tests pin which staging files exist, with which
// modes, after Open, every Seal path and the Discard that follows, and the
// exact error each path reports.

func TestStorageR3OpenCreatesPrivateStage(t *testing.T) {
	f := newFiles(t)
	s := storageR3Stage(t, f, "abc")
	storageR3Same(t, "open tree", storageR3Tree(t, f.root), storageR3Open)
	storageR3Same(t, "stage directory is safe", safeDirectory(filepath.Base(s.dir)), true)
	storageR3Same(t, "stage parent", filepath.Dir(s.dir), f.root)
	storageR3Must(t, s.Discard())
	storageR3Same(t, "discarded tree", storageR3Tree(t, f.root), "")
}

type storageR3Rejection struct {
	name    string
	ctx     func() context.Context
	receipt func(*capturestream.Receipt)
	output  []byte
}

func (c storageR3Rejection) run(t *testing.T) {
	f := newFiles(t)
	s := storageR3Stage(t, f, "abc")
	receipt := testReceipt(testScope(), "abc")
	if c.receipt != nil {
		c.receipt(&receipt)
	}
	output := testOutput()
	if c.output != nil {
		output = c.output
	}
	storageR3Is(t, "Seal", s.Seal(storageR3Background(c.ctx), receipt, output), ErrInvalid)
	storageR3Same(t, "tree after a rejected seal", storageR3Tree(t, f.root), storageR3Open)
	storageR3Same(t, "state after a rejected seal", storageR3StateOf(s), storageR3State{open: true})
	storageR3Must(t, s.Seal(context.Background(), testReceipt(testScope(), "abc"), testOutput()))
	storageR3Same(t, "tree after a valid seal", storageR3Tree(t, f.root), storageR3Published)
}

func TestStorageR3SealRejectionsLeaveStageUsable(t *testing.T) {
	otherScope := testScope()
	otherScope.CaptureID = "other"
	tests := []storageR3Rejection{
		{name: "nil context", ctx: func() context.Context { return nil }},
		{name: "canceled context", ctx: storageR3Canceled},
		{name: "scope", receipt: func(r *capturestream.Receipt) { r.Scope = otherScope }},
		{name: "size", receipt: func(r *capturestream.Receipt) { r.SizeBytes++ }},
		{name: "digest", receipt: func(r *capturestream.Receipt) { r.SHA256 = strings.Repeat("0", 64) }},
		{name: "digest case", receipt: func(r *capturestream.Receipt) { r.SHA256 = strings.ToUpper(r.SHA256) }},
		{name: "empty output", output: []byte{}},
		{name: "output too large", output: []byte(`"` + strings.Repeat("x", maximumCaptureOutputBytes-1) + `"`)},
		{name: "output not json", output: []byte(`{"outcome":`)},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestStorageR3SealAcceptsLargestOutput(t *testing.T) {
	f := newFiles(t)
	s := storageR3Stage(t, f, "abc")
	output := []byte(`"` + strings.Repeat("x", maximumCaptureOutputBytes-2) + `"`)
	storageR3Must(t, s.Seal(context.Background(), testReceipt(testScope(), "abc"), output))
}

// storageR3SealCase runs Seal once on an "abc" stage, then a second valid
// Seal (always refused once the first has failed or published), then Discard,
// then a late Write (always refused).
type storageR3SealCase struct {
	name     string
	stageCtx func() context.Context
	writeErr error
	prepare  func(t *testing.T, f *Files, s *stage) context.Context
	restore  func(f *Files)
	after    func(t *testing.T)
	want     error
	state    storageR3State
	tree     string
	discard  error
	final    string // tree after Discard
}

func (c storageR3SealCase) run(t *testing.T) {
	f := newFiles(t)
	s := storageR3OpenStage(t, f, storageR3Background(c.stageCtx))
	_, writeErr := io.WriteString(s, "abc")
	storageR3Is(t, "Write", writeErr, c.writeErr)
	ctx := context.Background()
	if c.prepare != nil {
		ctx = c.prepare(t, f, s)
	}
	receipt := testReceipt(testScope(), "abc")
	err := s.Seal(ctx, receipt, testOutput())
	if c.restore != nil {
		c.restore(f)
	}
	storageR3Is(t, "Seal", err, c.want)
	storageR3Same(t, "state after Seal", storageR3StateOf(s), c.state)
	storageR3Same(t, "tree after Seal", storageR3Tree(t, f.root), c.tree)
	if c.after != nil {
		c.after(t)
	}
	storageR3Is(t, "second Seal", s.Seal(context.Background(), receipt, testOutput()), ErrInvalid)
	storageR3Same(t, "tree after second Seal", storageR3Tree(t, f.root), c.tree)
	storageR3Is(t, "Discard", s.Discard(), c.discard)
	storageR3Same(t, "tree after Discard", storageR3Tree(t, f.root), c.final)
	_, lateErr := s.Write([]byte("late"))
	storageR3Is(t, "late Write", lateErr, ErrInvalid)
}

func storageR3PrepareSealed(t *testing.T, _ *Files, s *stage) context.Context {
	storageR3Must(t, s.Seal(context.Background(), testReceipt(testScope(), "abc"), testOutput()))
	return context.Background()
}

func storageR3PrepareDiscarded(t *testing.T, _ *Files, s *stage) context.Context {
	storageR3Must(t, s.Discard())
	return context.Background()
}

func storageR3PrepareStagedReceipt(t *testing.T, _ *Files, s *stage) context.Context {
	storageR3Must(t, os.WriteFile(filepath.Join(s.dir, "receipt.json"), []byte("{}"), 0600))
	return context.Background()
}

func storageR3PrepareCompetitor(t *testing.T, f *Files, _ *stage) context.Context {
	competitor := storageR3Stage(t, f, "abc")
	storageR3Must(t, competitor.Seal(context.Background(), testReceipt(testScope(), "abc"), testOutput()))
	return context.Background()
}

func storageR3PrepareClosedData(t *testing.T, _ *Files, s *stage) context.Context {
	storageR3Must(t, s.file.Close())
	return context.Background()
}

// storageR3Competing is a published capture beside a closed, unlinked one.
const storageR3Competing = "<key>.json -rw------- =receipt\ncapture-* drwx------\ncapture-* drwx------\n" +
	"capture-*/data -r--------\ncapture-*/data -r--------\ncapture-*/receipt.json -rw-------\ncapture-*/receipt.json -rw-------"

func TestStorageR3SealPaths(t *testing.T) {
	reconcile := captureinvoke.ErrStagingNeedsReconciliation
	failedWrite := storageR3CancelAfter(2) // Open checks once, then Write's second check fails.
	sealCtx := storageR3CancelAfter(1)
	var savedRoot string
	tests := []storageR3SealCase{
		{name: "after a failed write", stageCtx: func() context.Context { return failedWrite }, writeErr: ErrInvalid,
			prepare: func(*testing.T, *Files, *stage) context.Context {
				failedWrite.allowed.Store(1 << 30)
				return context.Background()
			},
			want: ErrInvalid, state: storageR3State{open: true, failed: true}, tree: storageR3Open, final: ""},
		{name: "after Discard", prepare: storageR3PrepareDiscarded, want: ErrInvalid,
			state: storageR3State{discarded: true}, tree: "", final: ""},
		{name: "after a successful seal", prepare: storageR3PrepareSealed, want: ErrInvalid,
			state: storageR3State{published: true, sealed: true}, tree: storageR3Published, discard: reconcile, final: storageR3Published},
		{name: "receipt already staged", prepare: storageR3PrepareStagedReceipt, want: ErrInvalid,
			state: storageR3State{}, tree: storageR3Unlinked, final: ""},
		{name: "canceled before publication", prepare: func(*testing.T, *Files, *stage) context.Context { return sealCtx },
			after: func(t *testing.T) { storageR3Same(t, "Seal context checks", sealCtx.calls.Load(), int64(2)) },
			want:  ErrInvalid, state: storageR3State{}, tree: storageR3Unlinked, final: ""},
		{name: "stage canceled before publication", stageCtx: func() context.Context { return storageR3CancelAfter(4) },
			want: ErrInvalid, state: storageR3State{}, tree: storageR3Unlinked, final: ""},
		{name: "publication refused", prepare: func(_ *testing.T, f *Files, _ *stage) context.Context {
			savedRoot, f.root = f.root, filepath.Join(f.root, "missing")
			return context.Background()
		}, restore: func(f *Files) { f.root = savedRoot },
			want: ErrInvalid, state: storageR3State{}, tree: storageR3Unlinked, final: ""},
		{name: "competing record", prepare: storageR3PrepareCompetitor, want: reconcile,
			state: storageR3State{}, tree: storageR3Competing, final: storageR3Published},
		{name: "root sync fails", prepare: func(_ *testing.T, f *Files, _ *stage) context.Context {
			f.syncRoot = func() error { return errors.New("sync failed") }
			return context.Background()
		}, want: reconcile, state: storageR3State{published: true}, tree: storageR3Published, discard: reconcile, final: storageR3Published},
		{name: "data descriptor already closed", prepare: storageR3PrepareClosedData, want: ErrInvalid,
			state: storageR3State{open: true}, tree: storageR3Open, discard: ErrInvalid, final: ""},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestStorageR3SealWritesManifest(t *testing.T) {
	f := newFiles(t)
	s := storageR3Stage(t, f, "abc")
	receipt := testReceipt(testScope(), "abc")
	output := testOutput()
	storageR3Must(t, s.Seal(context.Background(), receipt, output))
	output[0] = '['
	encoded, err := os.ReadFile(filepath.Join(f.root, storageR3Key(t)))
	storageR3Must(t, err)
	receiptJSON, err := json.Marshal(receipt)
	storageR3Must(t, err)
	outputDigest := sha256.Sum256(testOutput())
	want := `{"Version":2,"Directory":"` + filepath.Base(s.dir) + `","Receipt":` + string(receiptJSON) +
		`,"output":` + string(testOutput()) + `,"output_sha256":"` + hex.EncodeToString(outputDigest[:]) + `"}`
	storageR3Same(t, "manifest", string(encoded), want)
	scopeJSON, err := json.Marshal(testScope())
	storageR3Must(t, err)
	scopeDigest := sha256.Sum256(scopeJSON)
	storageR3Same(t, "record name", storageR3Key(t), hex.EncodeToString(scopeDigest[:])+".json")
}
