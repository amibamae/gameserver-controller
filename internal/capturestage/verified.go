package capturestage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

const (
	maximumReceiptBytes = 65536
	// maximumExactSizeBytes is the largest size a JSON number holds exactly.
	maximumExactSizeBytes = 9007199254740991
)

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

// openVerifiedCapture reads and checks the published receipt, then opens and
// rehashes its data. Every refusal returns no descriptor.
func (f *Files) openVerifiedCapture(ctx context.Context, scope capturestream.Scope) (*os.File, capturestream.Receipt, json.RawMessage, error) {
	m, err := f.readManifest(ctx, scope)
	if err != nil {
		return nil, capturestream.Receipt{}, nil, err
	}
	file, err := f.openVerifiedData(ctx, m)
	if err != nil {
		return nil, capturestream.Receipt{}, nil, err
	}
	return file, m.Receipt, append(json.RawMessage(nil), m.Output...), nil
}

// ready reports usable staging and a live context.
func (f *Files) ready(ctx context.Context) bool {
	return f != nil && f.root != "" && f.syncRoot != nil && liveContext(ctx)
}

func (f *Files) readManifest(ctx context.Context, scope capturestream.Scope) (manifest, error) {
	if !f.ready(ctx) {
		return manifest{}, ErrInvalid
	}
	name, err := key(scope)
	if err != nil {
		return manifest{}, err
	}
	encoded, err := readRecord(filepath.Join(f.root, name))
	if err != nil {
		return manifest{}, err
	}
	m, ok := decodeManifest(encoded)
	if !ok || !m.describes(scope) || !m.outputIntact() {
		return manifest{}, ErrInvalid
	}
	return m, nil
}

// readRecord reads a private receipt record of at most maximumReceiptBytes.
func readRecord(path string) ([]byte, error) {
	record, err := privateFile(path)
	if err != nil {
		return nil, err
	}
	encoded, err := io.ReadAll(io.LimitReader(record, maximumReceiptBytes+1))
	// The descriptor is read-only and was read to its bound, so a Close
	// error cannot change what was read.
	_ = record.Close()
	if err != nil || len(encoded) > maximumReceiptBytes {
		return nil, ErrInvalid
	}
	return encoded, nil
}

// decodeManifest accepts exactly one manifest value with no unknown fields.
func decodeManifest(encoded []byte) (manifest, bool) {
	var m manifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m) != nil || decoder.Decode(new(any)) != io.EOF {
		return manifest{}, false
	}
	return m, true
}

// describes reports a known manifest version for exactly this scope, naming
// a safe stage directory and a size JSON holds exactly.
func (m manifest) describes(scope capturestream.Scope) bool {
	return knownVersion(m.Version) && m.Receipt.Scope == scope && safeDirectory(m.Directory) && exactSize(m.Receipt.SizeBytes)
}

func knownVersion(version int) bool {
	return version == 1 || version == 2
}

func exactSize(size int64) bool {
	return size >= 0 && size <= maximumExactSizeBytes
}

// outputIntact reports a version 1 manifest without adapter output, or a
// version 2 manifest whose bounded output matches its recorded digest.
func (m manifest) outputIntact() bool {
	if m.Version == 1 {
		return len(m.Output) == 0 && m.OutputSHA == ""
	}
	if !validOutput(m.Output) {
		return false
	}
	outputDigest := sha256.Sum256(m.Output)
	return m.OutputSHA == hex.EncodeToString(outputDigest[:])
}

// openVerifiedData opens the stage's private data and returns it rewound
// once its size and digest match the receipt. It is closed on every refusal.
func (f *Files) openVerifiedData(ctx context.Context, m manifest) (*os.File, error) {
	directory := filepath.Join(f.root, m.Directory)
	if !privateDirectory(directory) {
		return nil, ErrInvalid
	}
	file, err := privateFile(filepath.Join(directory, "data"))
	if err != nil {
		return nil, err
	}
	if err := verifyData(ctx, file, m.Receipt); err != nil {
		// The data is refused either way; a Close error cannot change that.
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func privateDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode().Perm()&0077 == 0
}

// verifyData rehashes at most one byte more than the receipt's size, checking
// the context on every read, and rewinds the file.
func verifyData(ctx context.Context, file *os.File, receipt capturestream.Receipt) error {
	digest := sha256.New()
	size, err := io.Copy(digest, &contextReader{ctx: ctx, reader: io.LimitReader(file, receipt.SizeBytes+1)})
	if err != nil || ctx.Err() != nil || size != receipt.SizeBytes || hex.EncodeToString(digest.Sum(nil)) != receipt.SHA256 {
		return ErrInvalid
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return ErrInvalid
	}
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
