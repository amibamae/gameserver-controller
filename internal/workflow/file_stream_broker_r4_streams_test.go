package workflow

// Round-4 characterization of the staged stream writer (sink) and reader
// (source): construction guards, per-call limits, expiry, the record each
// Close leaves behind, and the exact error each path returns.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	brokerR4ErrSinkGuard   = "valid bounded stream sink is required"
	brokerR4ErrSourceGuard = "valid bounded stream source is required"
	brokerR4ErrSinkBad     = "bounded stream sink was incomplete, expired, oversized, or mismatched"
	brokerR4ErrSourceBad   = "bounded stream source was incomplete, expired, oversized, or mismatched"
)

// brokerR4StreamRecord is a record for a stream of up to maximum bytes that
// expects exactly data (or any size when expectedSize is -1).
func brokerR4StreamRecord(direction string, data []byte, maximum, expectedSize int64) stagedStreamRecord {
	return stagedStreamRecord{
		OperationID: "op", StepID: "step", Attempt: 1, DataSetID: "world",
		Direction: direction, MaximumBytes: maximum, ExpectedSize: expectedSize,
		ExpectedSHA256: brokerR3Digest(data), ExpiresAt: time.Now().Add(time.Minute),
		StoresBytes: direction == brokerR3Sink, InputSHA256: brokerR3Digest([]byte("input")),
	}
}

// brokerR4Stream is a writer or reader under test plus its staged paths.
type brokerR4Stream struct {
	file     *os.File
	dataPath string
	metaPath string
}

func brokerR4StreamPaths(t *testing.T) brokerR4Stream {
	t.Helper()
	dir := t.TempDir()
	return brokerR4Stream{dataPath: filepath.Join(dir, "data.bin"), metaPath: filepath.Join(dir, "data.bin.json")}
}

// brokerR4TempFile is an open file closed when the test ends.
func brokerR4TempFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stream")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAbandoned(file) })
	return file
}

func brokerR4NewWriter(t *testing.T, record stagedStreamRecord) (*stagedStreamWriter, brokerR4Stream) {
	t.Helper()
	stream := brokerR4StreamPaths(t)
	file, err := os.OpenFile(stream.dataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAbandoned(file) })
	stream.file = file
	writer, err := newStagedStreamWriter(file, stream.metaPath, record)
	if err != nil {
		t.Fatal(err)
	}
	return writer, stream
}

func brokerR4NewReader(t *testing.T, record stagedStreamRecord, content []byte) (*stagedStreamReader, brokerR4Stream) {
	t.Helper()
	stream := brokerR4StreamPaths(t)
	brokerR3Write(t, stream.dataPath, content)
	file, err := os.Open(stream.dataPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeAbandoned(file) })
	stream.file = file
	reader, err := newStagedStreamReader(file, stream.metaPath, record)
	if err != nil {
		t.Fatal(err)
	}
	return reader, stream
}

// brokerR4WantMeasured checks the record Close stored: the issued record plus
// the completion flag and measurements of the bytes that crossed the stream.
func brokerR4WantMeasured(t *testing.T, metaPath string, issued stagedStreamRecord, complete bool, measured []byte) {
	t.Helper()
	want := issued
	want.Complete, want.ActualSize, want.ActualSHA256 = complete, int64(len(measured)), brokerR3Digest(measured)
	brokerR3WantRecord(t, metaPath, want)
}

func brokerR4Write(t *testing.T, writer io.Writer, data []byte) {
	t.Helper()
	if n, err := writer.Write(data); err != nil || n != len(data) {
		t.Fatalf("write = %d, %v; want %d, nil", n, err, len(data))
	}
}

func brokerR4WantIs(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestBrokerR4WriterGuards(t *testing.T) {
	file := brokerR4TempFile(t)
	valid := brokerR4StreamRecord(brokerR3Sink, nil, 8, -1)
	wrongDirection := valid
	wrongDirection.Direction = brokerR3Source
	notStored := valid
	notStored.StoresBytes = false
	cases := []struct {
		name     string
		file     *os.File
		metaPath string
		record   stagedStreamRecord
	}{
		{"nil file", nil, "meta", valid},
		{"empty meta path", file, "", valid},
		{"does not store bytes", file, "meta", notStored},
		{"source direction", file, "meta", wrongDirection},
	}
	for _, tc := range cases {
		writer, err := newStagedStreamWriter(tc.file, tc.metaPath, tc.record)
		if writer != nil {
			t.Fatalf("%s: writer = %v, want nil", tc.name, writer)
		}
		brokerR3WantErr(t, err, brokerR4ErrSinkGuard)
	}
}

func TestBrokerR4WriterCompletes(t *testing.T) {
	data := []byte("12345678")
	cases := []struct {
		name   string
		record stagedStreamRecord
	}{
		{"exact size and checksum", brokerR4StreamRecord(brokerR3Sink, data, 8, 8)},
		{"unknown size", brokerR4StreamRecord(brokerR3Sink, data, 16, -1)},
		{"no expected checksum", func() stagedStreamRecord {
			record := brokerR4StreamRecord(brokerR3Sink, data, 8, 8)
			record.ExpectedSHA256 = ""
			return record
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer, stream := brokerR4NewWriter(t, tc.record)
			brokerR4Write(t, writer, data[:3])
			brokerR4Write(t, writer, data[3:])
			brokerR3WantResult(t, writer.Close(), "")
			brokerR3WantContent(t, stream.dataPath, string(data))
			brokerR4WantMeasured(t, stream.metaPath, tc.record, true, data)
			brokerR3WantResult(t, writer.Close(), "")
			_, err := writer.Write([]byte("x"))
			brokerR4WantIs(t, err, os.ErrClosed)
		})
	}
}

func TestBrokerR4WriterRejectsMismatchAtClose(t *testing.T) {
	data := []byte("12345678")
	cases := []struct {
		name    string
		record  stagedStreamRecord
		written []byte
	}{
		{"short", brokerR4StreamRecord(brokerR3Sink, data, 8, 8), data[:7]},
		{"empty", brokerR4StreamRecord(brokerR3Sink, data, 8, 8), nil},
		{"checksum", brokerR4StreamRecord(brokerR3Sink, []byte("other..."), 8, 8), data},
		{"checksum with unknown size", brokerR4StreamRecord(brokerR3Sink, []byte("other"), 8, -1), data},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer, stream := brokerR4NewWriter(t, tc.record)
			if len(tc.written) > 0 {
				brokerR4Write(t, writer, tc.written)
			}
			brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
			brokerR4WantMeasured(t, stream.metaPath, tc.record, false, tc.written)
		})
	}
}

func TestBrokerR4WriterLimits(t *testing.T) {
	t.Run("too large", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, nil, 4, -1)
		record.ExpectedSHA256 = ""
		writer, stream := brokerR4NewWriter(t, record)
		brokerR4Write(t, writer, []byte("123"))
		n, err := writer.Write([]byte("45"))
		if n != 0 {
			t.Fatalf("oversized write = %d bytes, want 0", n)
		}
		brokerR4WantIs(t, err, ErrStreamTooLarge)
		brokerR4Write(t, writer, []byte("4"))
		brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
		brokerR3WantContent(t, stream.dataPath, "1234")
		brokerR4WantMeasured(t, stream.metaPath, record, false, []byte("1234"))
	})
	t.Run("expired write", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, nil, 4, -1)
		record.ExpiresAt = time.Now().Add(-time.Second)
		writer, stream := brokerR4NewWriter(t, record)
		n, err := writer.Write([]byte("1"))
		if n != 0 {
			t.Fatalf("expired write = %d bytes, want 0", n)
		}
		brokerR4WantIs(t, err, ErrStreamExpired)
		brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("expired before close", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, []byte("1"), 4, 1)
		writer, stream := brokerR4NewWriter(t, record)
		brokerR4Write(t, writer, []byte("1"))
		writer.record.ExpiresAt = time.Now().Add(-time.Second)
		record.ExpiresAt = writer.record.ExpiresAt
		brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, []byte("1"))
	})
}

func TestBrokerR4WriterFileFailures(t *testing.T) {
	t.Run("write and close errors fail the sink", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, nil, 4, -1)
		writer, stream := brokerR4NewWriter(t, record)
		if err := stream.file.Close(); err != nil {
			t.Fatal(err)
		}
		n, err := writer.Write([]byte("1"))
		if n != 0 || err == nil || !strings.Contains(err.Error(), "file already closed") {
			t.Fatalf("write = %d, %v; want a closed-file error", n, err)
		}
		brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("sync and close errors alone fail the sink", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, nil, 4, 0)
		writer, stream := brokerR4NewWriter(t, record)
		if err := stream.file.Close(); err != nil {
			t.Fatal(err)
		}
		brokerR3WantErr(t, writer.Close(), brokerR4ErrSinkBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("record write error wins", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Sink, nil, 4, 0)
		writer, stream := brokerR4NewWriter(t, record)
		writer.metaPath = filepath.Join(filepath.Dir(stream.metaPath), "missing", "data.bin.json")
		brokerR4WantRecordWriteErr(t, writer.Close(), writer.metaPath)
		brokerR3Exists(t, stream.metaPath, false)
		brokerR3WantResult(t, writer.Close(), "")
	})
}

// brokerR4WantRecordWriteErr pins the error writeStagedStreamRecord returns
// when metaPath's directory does not exist. Only the random temporary-file
// suffix varies between runs.
func brokerR4WantRecordWriteErr(t *testing.T, err error, metaPath string) {
	t.Helper()
	prefix := "open " + filepath.Join(filepath.Dir(metaPath), ".stream-record-")
	const suffix = ": no such file or directory"
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), suffix) ||
		!errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want %q<random>%q", err, prefix, suffix)
	}
}

func TestBrokerR4ReaderGuards(t *testing.T) {
	file := brokerR4TempFile(t)
	valid := brokerR4StreamRecord(brokerR3Source, nil, 8, 0)
	mutated := func(mutate func(*stagedStreamRecord)) stagedStreamRecord {
		record := valid
		mutate(&record)
		return record
	}
	cases := []struct {
		name     string
		file     *os.File
		metaPath string
		record   stagedStreamRecord
	}{
		{"nil file", nil, "meta", valid},
		{"empty meta path", file, "", valid},
		{"stores bytes", file, "meta", mutated(func(r *stagedStreamRecord) { r.StoresBytes = true })},
		{"sink direction", file, "meta", mutated(func(r *stagedStreamRecord) { r.Direction = brokerR3Sink })},
		{"unknown size", file, "meta", mutated(func(r *stagedStreamRecord) { r.ExpectedSize = -1 })},
	}
	for _, tc := range cases {
		reader, err := newStagedStreamReader(tc.file, tc.metaPath, tc.record)
		if reader != nil {
			t.Fatalf("%s: reader = %v, want nil", tc.name, reader)
		}
		brokerR3WantErr(t, err, brokerR4ErrSourceGuard)
	}
	if _, err := newStagedStreamReader(file, "meta", valid); err != nil {
		t.Fatalf("zero expected size: %v", err)
	}
}

func TestBrokerR4ReaderCompletes(t *testing.T) {
	data := []byte("12345678")
	withoutChecksum := brokerR4StreamRecord(brokerR3Source, data, 8, 8)
	withoutChecksum.ExpectedSHA256 = ""
	for name, record := range map[string]stagedStreamRecord{
		"exact size and checksum": brokerR4StreamRecord(brokerR3Source, data, 8, 8),
		"no expected checksum":    withoutChecksum,
	} {
		t.Run(name, func(t *testing.T) {
			reader, stream := brokerR4NewReader(t, record, data)
			content, err := io.ReadAll(reader)
			if err != nil || string(content) != string(data) {
				t.Fatalf("read = %q, %v", content, err)
			}
			brokerR3WantResult(t, reader.Close(), "")
			brokerR4WantMeasured(t, stream.metaPath, record, true, data)
			brokerR3WantResult(t, reader.Close(), "")
			_, err = reader.Read(make([]byte, 1))
			brokerR4WantIs(t, err, os.ErrClosed)
		})
	}
}

func TestBrokerR4ReaderRejectsIncompleteOrMismatched(t *testing.T) {
	data := []byte("12345678")
	cases := []struct {
		name    string
		record  stagedStreamRecord
		content []byte
		read    int
		measure []byte
	}{
		{"not read to the end", brokerR4StreamRecord(brokerR3Source, data, 8, 8), data, 3, data[:3]},
		{"never read", brokerR4StreamRecord(brokerR3Source, data, 8, 8), data, 0, nil},
		{"shorter than expected", brokerR4StreamRecord(brokerR3Source, data, 8, 8), data[:7], -1, data[:7]},
		{"longer than expected", brokerR4StreamRecord(brokerR3Source, data[:7], 8, 7), data, -1, data},
		{"checksum", brokerR4StreamRecord(brokerR3Source, []byte("other..."), 8, 8), data, -1, data},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, stream := brokerR4NewReader(t, tc.record, tc.content)
			brokerR4ReadSome(t, reader, tc.read)
			brokerR3WantErr(t, reader.Close(), brokerR4ErrSourceBad)
			brokerR4WantMeasured(t, stream.metaPath, tc.record, false, tc.measure)
		})
	}
}

// brokerR4ReadSome reads count bytes, or everything when count is negative.
func brokerR4ReadSome(t *testing.T, reader io.Reader, count int) {
	t.Helper()
	if count < 0 {
		if _, err := io.ReadAll(reader); err != nil {
			t.Fatal(err)
		}
		return
	}
	if count == 0 {
		return
	}
	if _, err := io.ReadFull(reader, make([]byte, count)); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerR4ReaderLimits(t *testing.T) {
	t.Run("too large", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("123"), 3, 3)
		reader, stream := brokerR4NewReader(t, record, []byte("12345"))
		n, err := reader.Read(make([]byte, 8))
		if n != 0 {
			t.Fatalf("oversized read = %d bytes, want 0", n)
		}
		brokerR4WantIs(t, err, ErrStreamTooLarge)
		brokerR3WantErr(t, reader.Close(), brokerR4ErrSourceBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("read at the maximum", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("123"), 3, 3)
		reader, _ := brokerR4NewReader(t, record, []byte("123"))
		if n, err := reader.Read(make([]byte, 8)); n != 3 || err != nil {
			t.Fatalf("read = %d, %v; want 3, nil", n, err)
		}
	})
	t.Run("expired read", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, nil, 3, 0)
		record.ExpiresAt = time.Now().Add(-time.Second)
		reader, stream := brokerR4NewReader(t, record, nil)
		n, err := reader.Read(make([]byte, 8))
		if n != 0 {
			t.Fatalf("expired read = %d bytes, want 0", n)
		}
		brokerR4WantIs(t, err, ErrStreamExpired)
		brokerR3WantErr(t, reader.Close(), brokerR4ErrSourceBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("expired before close", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("1"), 3, 1)
		reader, stream := brokerR4NewReader(t, record, []byte("1"))
		brokerR4ReadSome(t, reader, -1)
		reader.record.ExpiresAt = time.Now().Add(-time.Second)
		record.ExpiresAt = reader.record.ExpiresAt
		brokerR3WantErr(t, reader.Close(), brokerR4ErrSourceBad)
		brokerR4WantMeasured(t, stream.metaPath, record, false, []byte("1"))
	})
}

func TestBrokerR4ReaderFileFailures(t *testing.T) {
	t.Run("close error is returned after a full read", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("1"), 3, 1)
		reader, stream := brokerR4NewReader(t, record, []byte("1"))
		brokerR4ReadSome(t, reader, -1)
		if err := stream.file.Close(); err != nil {
			t.Fatal(err)
		}
		brokerR3WantErr(t, reader.Close(), "close "+stream.dataPath+": file already closed")
		brokerR4WantMeasured(t, stream.metaPath, record, false, []byte("1"))
	})
	t.Run("read error fails the source", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("1"), 3, 1)
		reader, stream := brokerR4NewReader(t, record, []byte("1"))
		if err := stream.file.Close(); err != nil {
			t.Fatal(err)
		}
		n, err := reader.Read(make([]byte, 8))
		if n != 0 || err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("read = %d, %v; want a read error", n, err)
		}
		brokerR3WantErr(t, reader.Close(), "close "+stream.dataPath+": file already closed")
		brokerR4WantMeasured(t, stream.metaPath, record, false, nil)
	})
	t.Run("record write error wins", func(t *testing.T) {
		record := brokerR4StreamRecord(brokerR3Source, []byte("1"), 3, 1)
		reader, stream := brokerR4NewReader(t, record, []byte("1"))
		reader.metaPath = filepath.Join(filepath.Dir(stream.metaPath), "missing", "data.bin.json")
		brokerR4WantRecordWriteErr(t, reader.Close(), reader.metaPath)
		brokerR3Exists(t, stream.metaPath, false)
		brokerR3WantResult(t, reader.Close(), "")
	})
}
