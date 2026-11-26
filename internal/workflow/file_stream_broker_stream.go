package workflow

// Bounded staged streams: the sink an adapter writes captured or read-back
// bytes into, and the source it reads verified staged bytes from.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"sync"
	"time"
)

// stagedStream is the state a bounded sink and a bounded source share: the
// staged file, its record, and what has crossed the stream so far. The
// methods below expect the caller to hold mu.
type stagedStream struct {
	mu       sync.Mutex
	file     *os.File
	metaPath string
	record   stagedStreamRecord
	hasher   hash.Hash
	actual   int64
	failed   bool
	closed   bool
}

// streamTargetMissing reports whether a stream has no file or record path.
func streamTargetMissing(file *os.File, metaPath string) bool {
	return file == nil || metaPath == ""
}

// recordLive reports whether a stream's record has not yet expired.
func recordLive(record stagedStreamRecord) bool {
	return time.Now().UTC().Before(record.ExpiresAt)
}

// startCall rejects a Read or Write on a closed stream and fails an expired one.
func (s *stagedStream) startCall() error {
	if s.closed {
		return os.ErrClosed
	}
	if !recordLive(s.record) {
		s.failed = true
		return ErrStreamExpired
	}
	return nil
}

// measure hashes and counts bytes that crossed the stream.
func (s *stagedStream) measure(value []byte) {
	// hash.Hash.Write never returns an error.
	_, _ = s.hasher.Write(value)
	s.actual += int64(len(value))
}

// markClosed closes the stream and reports false if it was already closed.
func (s *stagedStream) markClosed() bool {
	if s.closed {
		return false
	}
	s.closed = true
	return true
}

// recordMeasurement stores the measured size and digest in the record.
func (s *stagedStream) recordMeasurement() {
	s.record.ActualSize = s.actual
	s.record.ActualSHA256 = hex.EncodeToString(s.hasher.Sum(nil))
}

// storeRecord writes the measured record with its completion flag.
func (s *stagedStream) storeRecord(complete bool) error {
	s.record.Complete = complete
	return writeStagedStreamRecord(s.metaPath, s.record)
}

type stagedStreamWriter struct {
	stagedStream
}

func newStagedStreamWriter(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamWriter, error) {
	if streamTargetMissing(file, metaPath) || !record.StoresBytes || record.Direction != "adapter_to_controller" {
		return nil, errors.New("valid bounded stream sink is required")
	}
	return &stagedStreamWriter{stagedStream: stagedStream{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}}, nil
}

func (writer *stagedStreamWriter) Write(value []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if err := writer.startCall(); err != nil {
		return 0, err
	}
	if int64(len(value)) > writer.record.MaximumBytes-writer.actual {
		writer.failed = true
		return 0, ErrStreamTooLarge
	}
	written, err := writer.file.Write(value)
	if written > 0 {
		writer.measure(value[:written])
	}
	if err != nil {
		writer.failed = true
	}
	return written, err
}

func (writer *stagedStreamWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if !writer.markClosed() {
		return nil
	}
	writer.syncAndClose()
	writer.recordMeasurement()
	if !recordLive(writer.record) || !recordMeetsOwnExpectation(writer.record) {
		writer.failed = true
	}
	if err := writer.storeRecord(!writer.failed); err != nil {
		return err
	}
	if writer.failed {
		return errors.New("bounded stream sink was incomplete, expired, oversized, or mismatched")
	}
	return nil
}

// syncAndClose flushes and closes the staged file. Either failure fails the
// sink, which Close then reports with its own error.
func (writer *stagedStreamWriter) syncAndClose() {
	if err := writer.file.Sync(); err != nil {
		writer.failed = true
	}
	if err := writer.file.Close(); err != nil {
		writer.failed = true
	}
}

type stagedStreamReader struct {
	stagedStream
	eof bool
}

func newStagedStreamReader(file *os.File, metaPath string, record stagedStreamRecord) (*stagedStreamReader, error) {
	if streamTargetMissing(file, metaPath) || !sourceStreamRecord(record) {
		return nil, errors.New("valid bounded stream source is required")
	}
	return &stagedStreamReader{stagedStream: stagedStream{file: file, metaPath: metaPath, record: record, hasher: sha256.New()}}, nil
}

// sourceStreamRecord reports whether a record describes a source stream of
// already staged bytes with a known size.
func sourceStreamRecord(record stagedStreamRecord) bool {
	return !record.StoresBytes && record.Direction == "controller_to_adapter" && record.ExpectedSize >= 0
}

func (reader *stagedStreamReader) Read(value []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if err := reader.startCall(); err != nil {
		return 0, err
	}
	read, err := reader.file.Read(value)
	if read > 0 {
		if int64(read) > reader.record.MaximumBytes-reader.actual {
			reader.failed = true
			return 0, ErrStreamTooLarge
		}
		reader.measure(value[:read])
	}
	if errors.Is(err, io.EOF) {
		reader.eof = true
	} else if err != nil {
		reader.failed = true
	}
	return read, err
}

func (reader *stagedStreamReader) Close() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if !reader.markClosed() {
		return nil
	}
	closeErr := reader.file.Close()
	reader.recordMeasurement()
	complete := reader.drained(closeErr) && recordLive(reader.record) &&
		reader.actual == reader.record.ExpectedSize && recordChecksumMet(reader.record)
	if err := reader.storeRecord(complete); err != nil {
		return err
	}
	if complete {
		return nil
	}
	if closeErr != nil {
		return closeErr
	}
	return errors.New("bounded stream source was incomplete, expired, oversized, or mismatched")
}

// drained reports whether the source was read to its end without a failure
// and its file closed cleanly.
func (reader *stagedStreamReader) drained(closeErr error) bool {
	return reader.eof && !reader.failed && closeErr == nil
}
