// Package capturestage stores private capture bytes and receipts on a local
// filesystem. Its root must be exclusively controlled by the controller user.
package capturestage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"os"
	"path/filepath"
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
	if !f.ready(ctx) {
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
