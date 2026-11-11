// Package adapterprocess implements the jsonrpc-stdio-v1 process transport
// from protocol/rpc/transport.md: a controller-side Process that runs an
// adapter executable, and Serve, which runs a Go adapter behind stdio.
package adapterprocess

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// MaxFrameBytes is the jsonrpc-stdio-v1 line limit, excluding the newline.
const MaxFrameBytes = 4 << 20

var (
	errBlankFrame     = errors.New("blank frame")
	errOversizedFrame = errors.New("frame exceeds the 4 MiB limit")
	errInvalidUTF8    = errors.New("frame is not valid UTF-8")
	errTruncatedFrame = errors.New("stream ended inside a frame")
)

// frameReader reads newline-delimited frames without buffering more than
// MaxFrameBytes plus a small line terminator.
type frameReader struct {
	reader *bufio.Reader
	limit  int
}

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{reader: bufio.NewReaderSize(r, 64<<10), limit: MaxFrameBytes}
}

// next returns one frame without its LF or CRLF terminator. A clean end of
// input before any byte of a frame returns io.EOF.
func (f *frameReader) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := f.reader.ReadSlice('\n')
		if len(line)+len(chunk) > f.limit+2 {
			f.discardLine(err)
			return nil, errOversizedFrame
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return f.finish(line)
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errTruncatedFrame
		default:
			return nil, err
		}
	}
}

func (f *frameReader) finish(line []byte) ([]byte, error) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	switch {
	case len(line) == 0:
		return nil, errBlankFrame
	case len(line) > f.limit:
		return nil, errOversizedFrame
	case !utf8.Valid(line):
		return nil, errInvalidUTF8
	}
	return line, nil
}

// discardLine skips the rest of an oversized line so the next frame can be read.
func (f *frameReader) discardLine(err error) {
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = f.reader.ReadSlice('\n')
	}
}

// writeFrame encodes value as one JSON line. encoding/json escapes newlines
// inside strings, so the only LF is the terminator.
func writeFrame(w io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > MaxFrameBytes {
		return errOversizedFrame
	}
	_, err = w.Write(append(encoded, '\n'))
	return err
}

// envelope is the strict shape of any jsonrpc-stdio-v1 message.
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("adapter returned JSON-RPC error %d: %s", e.Code, e.Message)
}

// Standard JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	codeServerError    = -32000
)

// parseEnvelope strictly parses one frame. It reports a JSON-RPC code so a
// server can answer parse and shape errors distinctly.
func parseEnvelope(frame []byte) (envelope, string, int, error) {
	value, err := schemaregistry.ParseStrictJSON(frame)
	if err != nil {
		return envelope{}, "", codeParseError, err
	}
	if _, ok := value.(map[string]any); !ok {
		return envelope{}, "", codeInvalidRequest, errors.New("message must be one JSON-RPC object; batches are not supported")
	}
	var message envelope
	if err := schemaregistry.DecodeStrictJSON(frame, &message); err != nil {
		return envelope{}, "", codeInvalidRequest, err
	}
	if message.JSONRPC != "2.0" {
		return message, "", codeInvalidRequest, errors.New("jsonrpc must be \"2.0\"")
	}
	if len(message.ID) == 0 || message.ID[0] != '"' {
		return message, "", codeInvalidRequest, errors.New("id must be a string; notifications and numeric IDs are not supported")
	}
	var id string
	if err := json.Unmarshal(message.ID, &id); err != nil || id == "" {
		return message, "", codeInvalidRequest, errors.New("id must be a non-empty string")
	}
	return message, id, 0, nil
}
