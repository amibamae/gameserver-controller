package controllersetup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// DefaultAuditLogPath is where controller-setup records each mutating
// command on the local machine.
const DefaultAuditLogPath = "/var/log/gameserver-controller/setup-audit.jsonl"

// AuditRecord is one JSON line in the setup audit log. Detail carries the
// command's error text, which the CLI already keeps free of secret values.
type AuditRecord struct {
	Time    string `json:"time"`
	Command string `json:"command"`
	Event   string `json:"event"`
	Outcome string `json:"outcome,omitempty"`
	Detail  string `json:"detail,omitempty"`
	UID     int    `json:"uid"`
}

// AuditLog appends records to a local JSON-lines file. The file and its
// directory are private to the account running the setup command.
type AuditLog struct {
	Path string
	Now  func() time.Time
}

// Append writes one record and flushes it to disk before returning, so a
// command that later fails or crashes still leaves its start record.
func (l AuditLog) Append(record AuditRecord) error {
	if !filepath.IsAbs(l.Path) || filepath.Clean(l.Path) != l.Path {
		return errors.New("audit log path must be clean and absolute")
	}
	if err := ensureAuditDirectory(filepath.Dir(l.Path)); err != nil {
		return err
	}
	record.Time = l.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(record)
	if err != nil {
		return errors.New("encode audit record failed")
	}
	return appendAuditLine(l.Path, append(line, '\n'))
}

func (l AuditLog) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

func ensureAuditDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return errors.New("create audit log directory failed")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return errors.New("audit log directory is unavailable")
	}
	return nil
}

// appendAuditLine refuses a symlinked log file so the audit record cannot be
// redirected into another file.
func appendAuditLine(path string, line []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("audit log must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return errors.New("open audit log failed")
	}
	if _, err := file.Write(line); err != nil {
		_ = file.Close() // The write error is the one worth reporting.
		return errors.New("write audit log failed")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close() // The sync error is the one worth reporting.
		return errors.New("flush audit log failed")
	}
	if err := file.Close(); err != nil {
		return errors.New("close audit log failed")
	}
	return nil
}
