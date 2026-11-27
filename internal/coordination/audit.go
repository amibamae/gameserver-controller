package coordination

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// appendAudit appends one Markdown list item per event to the audit log.
// The log must already exist: a missing log is unreadable state, and the
// file is only ever opened for appending.
func appendAudit(path string, rev int64, now time.Time, events []Event) error {
	prefix, err := auditSeparator(path)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(prefix)
	for _, e := range events {
		fmt.Fprintf(&b, "- %s | rev %d | %s | %s | %s | %s\n", formatTime(now), rev,
			auditField(e.Actor), auditField(e.Action), auditField(e.Subject), auditField(e.Detail))
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	_, werr := f.WriteString(b.String())
	serr := f.Sync()
	return errors.Join(werr, serr, f.Close())
}

// auditSeparator returns what must precede the new entries so they form a
// list: a newline if the file does not end in one, and a blank line if the
// last line is not already an audit entry.
func auditSeparator(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read audit log: %w", err)
	}
	if len(data) == 0 {
		return "", nil
	}
	sep := ""
	if !bytes.HasSuffix(data, []byte("\n")) {
		sep = "\n"
	}
	trimmed := bytes.TrimRight(data, "\n")
	last := trimmed[bytes.LastIndexByte(trimmed, '\n')+1:]
	if !bytes.HasPrefix(last, []byte("- ")) {
		sep += "\n"
	}
	return sep, nil
}

// auditField keeps one entry on one line and its columns apart.
func auditField(s string) string {
	if s == "" {
		return "-"
	}
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "|", "/")
}
