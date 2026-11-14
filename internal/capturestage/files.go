// Package capturestage stores private capture bytes and receipts on a local
// filesystem. Its root must be exclusively controlled by the controller user.
package capturestage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/amibamae/gameserver-controller/internal/captureinvoke"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

var ErrInvalid = errors.New("invalid or incomplete capture staging")

type Files struct {
	root     string
	syncRoot func() error
}
type manifest struct {
	Version   int
	Directory string
	Receipt   capturestream.Receipt
	Output    json.RawMessage `json:"output,omitempty"`
	OutputSHA string          `json:"output_sha256,omitempty"`
}

const maximumCaptureOutputBytes = 64 * 1024

func New(root string) (*Files, error) {
	if root == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrInvalid
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, ErrInvalid
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, ErrInvalid
	}
	return &Files{root: canonical, syncRoot: func() error { return syncDir(canonical) }}, nil
}
func key(scope capturestream.Scope) (string, error) {
	for _, v := range []string{scope.OperationID, scope.StepID, scope.AdapterID, scope.ServerID, scope.EnvironmentID, scope.InstanceAdapterID, scope.InstanceID, scope.DataGenerationID, scope.DataSetID, scope.CaptureID} {
		if v == "" || len(v) > 500 {
			return "", ErrInvalid
		}
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]) + ".json", nil
}
func (f *Files) Open(ctx context.Context, scope capturestream.Scope) (captureinvoke.Stage, error) {
	if f == nil || f.root == "" || f.syncRoot == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrInvalid
	}
	name, err := key(scope)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(filepath.Join(f.root, name)); err == nil {
		return nil, captureinvoke.ErrStagingNeedsReconciliation
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrInvalid
	}
	dir, err := os.MkdirTemp(f.root, "capture-")
	if err != nil {
		return nil, ErrInvalid
	}
	file, err := os.OpenFile(filepath.Join(dir, "data"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, ErrInvalid
	}
	return &stage{ctx: ctx, owner: f, scope: scope, name: name, dir: dir, file: file, digest: sha256.New()}, nil
}

type stage struct {
	mu                                   sync.Mutex
	ctx                                  context.Context
	owner                                *Files
	scope                                capturestream.Scope
	name, dir                            string
	file                                 *os.File
	digest                               hash.Hash
	size                                 int64
	failed, published, sealed, discarded bool
}

func (s *stage) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writable() || s.ctx.Err() != nil {
		return 0, ErrInvalid
	}
	n, err := s.file.Write(p)
	if n > 0 {
		_, _ = s.digest.Write(p[:n])
		s.size += int64(n)
	}
	if err != nil || n != len(p) || s.ctx.Err() != nil {
		s.failed = true
		return n, ErrInvalid
	}
	return n, nil
}
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Seal checks the receipt and adapter output against what was written, makes
// the data read-only and durable, writes a durable receipt beside it, and
// publishes that receipt into the root by hard link. Once the link exists the
// stage is published and cannot be discarded, even if syncing the root fails.
func (s *stage) Seal(ctx context.Context, receipt capturestream.Receipt, output []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sealable(ctx, receipt, output) {
		return ErrInvalid
	}
	if err := s.closeData(); err != nil {
		return err
	}
	if err := s.writeReceipt(receipt, output); err != nil {
		return err
	}
	if ctx.Err() != nil || s.ctx.Err() != nil {
		return ErrInvalid
	}
	return s.publish()
}

// sealable checks, in order, both contexts, the stage state, the receipt, and
// the adapter output. A refusal changes nothing, so the stage stays usable.
func (s *stage) sealable(ctx context.Context, receipt capturestream.Receipt, output []byte) bool {
	return s.contextsLive(ctx) && s.writable() && s.matchesReceipt(receipt) && validOutput(output)
}

// contextsLive reports that neither the Seal call nor the stage was canceled.
func (s *stage) contextsLive(ctx context.Context) bool {
	return liveContext(ctx) && s.ctx.Err() == nil
}

// writable reports a stage that has not failed, been published or discarded,
// and still holds its data file open.
func (s *stage) writable() bool {
	return !s.failed && !s.published && !s.discarded && s.file != nil
}

func (s *stage) matchesReceipt(receipt capturestream.Receipt) bool {
	return receipt.Scope == s.scope && receipt.SizeBytes == s.size && receipt.SHA256 == hex.EncodeToString(s.digest.Sum(nil))
}

func liveContext(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil
}

func validOutput(output []byte) bool {
	return len(output) != 0 && len(output) <= maximumCaptureOutputBytes && json.Valid(output)
}

// closeData makes the data read-only and durable, then closes it. Only a
// failed Close marks the stage failed; the data file is nil afterwards.
func (s *stage) closeData() error {
	if s.file.Chmod(0400) != nil || s.file.Sync() != nil {
		return ErrInvalid
	}
	if err := s.file.Close(); err != nil {
		s.failed = true
		return ErrInvalid
	}
	s.file = nil
	return nil
}

// writeReceipt writes the version 2 manifest to the stage's receipt.json and
// syncs the stage directory.
func (s *stage) writeReceipt(receipt capturestream.Receipt, output []byte) error {
	outputCopy := append(json.RawMessage(nil), output...)
	outputDigest := sha256.Sum256(outputCopy)
	encoded, err := json.Marshal(manifest{Version: 2, Directory: filepath.Base(s.dir), Receipt: receipt, Output: outputCopy, OutputSHA: hex.EncodeToString(outputDigest[:])})
	if err != nil {
		return ErrInvalid
	}
	if err := writeNewDurable(filepath.Join(s.dir, "receipt.json"), encoded); err != nil || syncDir(s.dir) != nil {
		return ErrInvalid
	}
	return nil
}

// writeNewDurable creates path, refusing to replace a file, and writes and
// syncs encoded. Close runs even when the write or sync failed.
func writeNewDurable(path string, encoded []byte) error {
	record, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrInvalid
	}
	_, writeErr := record.Write(encoded)
	syncErr := record.Sync()
	closeErr := record.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrInvalid
	}
	return nil
}

// publish hard-links the receipt into the root. The link is atomic and
// refuses to replace an existing record.
func (s *stage) publish() error {
	if err := os.Link(filepath.Join(s.dir, "receipt.json"), filepath.Join(s.owner.root, s.name)); err != nil {
		return publicationError(err)
	}
	s.published = true
	if s.owner.syncRoot() != nil {
		return captureinvoke.ErrStagingNeedsReconciliation
	}
	s.sealed = true
	return nil
}

// publicationError reports a record another stage already published as
// needing reconciliation, and any other link failure as invalid.
func publicationError(err error) error {
	if errors.Is(err, os.ErrExist) {
		return captureinvoke.ErrStagingNeedsReconciliation
	}
	return ErrInvalid
}
func (s *stage) Discard() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published {
		return captureinvoke.ErrStagingNeedsReconciliation
	}
	if s.discarded {
		return nil
	}
	var closeErr error
	if s.file != nil {
		closeErr = s.file.Close()
		s.file = nil
	}
	if os.RemoveAll(s.dir) != nil || s.owner.syncRoot() != nil || closeErr != nil {
		return ErrInvalid
	}
	s.discarded = true
	return nil
}
func privateFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		file.Close()
		return nil, ErrInvalid
	}
	return file, nil
}
func safeDirectory(name string) bool {
	if !strings.HasPrefix(name, "capture-") || len(name) > 128 || len(name) <= 8 {
		return false
	}
	for _, c := range name[8:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// OpenVerified reopens a sealed capture by exact scope and rehashes its bytes.
// The returned descriptor is rewound. Callers must close it and independently
// verify any subsequent storage transfer. This is not a verified backup.
func (f *Files) OpenVerified(ctx context.Context, scope capturestream.Scope) (*os.File, capturestream.Receipt, error) {
	file, receipt, _, err := f.openVerifiedCapture(ctx, scope)
	return file, receipt, err
}

// OpenVerifiedCapture reopens the exact sealed stream and its original
// validated adapter response. Version 1 receipts remain readable through
// OpenVerified, but cannot recover a completed workflow step automatically.
func (f *Files) OpenVerifiedCapture(ctx context.Context, scope capturestream.Scope) (io.ReadCloser, capturestream.Receipt, json.RawMessage, error) {
	return f.openVerifiedCapture(ctx, scope)
}

func (f *Files) openVerifiedCapture(ctx context.Context, scope capturestream.Scope) (*os.File, capturestream.Receipt, json.RawMessage, error) {
	if f == nil || f.root == "" || f.syncRoot == nil || ctx == nil || ctx.Err() != nil {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	name, err := key(scope)
	if err != nil {
		return nil, capturestream.Receipt{}, nil, err
	}
	record, err := privateFile(filepath.Join(f.root, name))
	if err != nil {
		return nil, capturestream.Receipt{}, nil, err
	}
	encoded, err := io.ReadAll(io.LimitReader(record, 65537))
	record.Close()
	if err != nil || len(encoded) > 65536 {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	var m manifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF || (m.Version != 1 && m.Version != 2) || m.Receipt.Scope != scope || !safeDirectory(m.Directory) || m.Receipt.SizeBytes < 0 || m.Receipt.SizeBytes > 9007199254740991 {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	if m.Version == 1 {
		if len(m.Output) != 0 || m.OutputSHA != "" {
			return nil, capturestream.Receipt{}, nil, ErrInvalid
		}
	} else {
		if len(m.Output) == 0 || len(m.Output) > maximumCaptureOutputBytes || !json.Valid(m.Output) {
			return nil, capturestream.Receipt{}, nil, ErrInvalid
		}
		outputDigest := sha256.Sum256(m.Output)
		if m.OutputSHA != hex.EncodeToString(outputDigest[:]) {
			return nil, capturestream.Receipt{}, nil, ErrInvalid
		}
	}
	directory := filepath.Join(f.root, m.Directory)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	file, err := privateFile(filepath.Join(directory, "data"))
	if err != nil {
		return nil, capturestream.Receipt{}, nil, err
	}
	valid := false
	defer func() {
		if !valid {
			file.Close()
		}
	}()
	digest := sha256.New()
	reader := &contextReader{ctx: ctx, reader: io.LimitReader(file, m.Receipt.SizeBytes+1)}
	size, err := io.Copy(digest, reader)
	if err != nil || ctx.Err() != nil || size != m.Receipt.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != m.Receipt.SHA256 {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, capturestream.Receipt{}, nil, ErrInvalid
	}
	valid = true
	return file, m.Receipt, append(json.RawMessage(nil), m.Output...), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
