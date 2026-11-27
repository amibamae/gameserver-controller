package coordination

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// renameAttempts bounds retries of the final rename. On Windows a reader
// outside this CLI (an editor, git) can briefly hold the target open.
const renameAttempts = 5

// decodeStrict parses one JSON document into v and rejects unknown fields
// and trailing data, so a state file this version does not understand
// fails closed instead of losing fields on the next write.
func decodeStrict(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse %s: unexpected data after the JSON document", filepath.Base(path))
	}
	return nil
}

// writeJSONAtomic writes v to path through a temporary file in the same
// directory and renames it over path, so readers see the old or the new
// file and never a partial one.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", filepath.Base(path), err)
	}
	if err := fillAndClose(tmp, data); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", filepath.Base(path), err), removeTemp(tmp.Name()))
	}
	if err := renameWithRetry(tmp.Name(), path); err != nil {
		return errors.Join(fmt.Errorf("replace %s: %w", filepath.Base(path), err), removeTemp(tmp.Name()))
	}
	return nil
}

func fillAndClose(f *os.File, data []byte) error {
	_, werr := f.Write(data)
	serr := f.Sync()
	return errors.Join(werr, serr, f.Close())
}

func renameWithRetry(from, to string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(lockPoll)
	}
	return err
}

func removeTemp(name string) error {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove temporary file: %w", err)
	}
	return nil
}
