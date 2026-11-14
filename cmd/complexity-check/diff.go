package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// changedLines maps a repository-relative Go file to the line numbers that
// were added or changed since base, including uncommitted changes.
type changedLines map[string]map[int]bool

// touched reports whether any line from start to end changed in path.
func (c changedLines) touched(path string, start, end int) bool {
	for line := range c[path] {
		if line >= start && line <= end {
			return true
		}
	}
	return false
}

// gitChangedLines runs git diff against base for Go files.
func gitChangedLines(base string) (changedLines, error) {
	out, err := exec.Command("git", "diff", "--unified=0", "--no-color", "--no-ext-diff", base, "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff against %s failed: %w", base, err)
	}
	return parseUnifiedDiff(out)
}

// parseUnifiedDiff reads "+++ b/<path>" file headers and "@@ -a,b +c,d @@"
// hunk headers. A pure deletion (d = 0) marks line c, so the function around
// the removed lines still counts as changed.
func parseUnifiedDiff(diff []byte) (changedLines, error) {
	changed := changedLines{}
	current := ""
	scanner := bufio.NewScanner(bytes.NewReader(diff))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "+++ "):
			current = diffTarget(line)
		case strings.HasPrefix(line, "@@ ") && current != "":
			if err := markHunk(changed, current, line); err != nil {
				return nil, err
			}
		}
	}
	return changed, scanner.Err()
}

// diffTarget is the new path of a "+++" header, or "" for a deleted file.
func diffTarget(header string) string {
	path := strings.TrimPrefix(header, "+++ ")
	if path == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(path, "b/")
}

func markHunk(changed changedLines, path, header string) error {
	start, count, err := hunkNewRange(header)
	if err != nil {
		return err
	}
	if changed[path] == nil {
		changed[path] = map[int]bool{}
	}
	if count == 0 {
		changed[path][start] = true
		return nil
	}
	for line := start; line < start+count; line++ {
		changed[path][line] = true
	}
	return nil
}

// hunkNewRange parses the "+c,d" part of a hunk header; d defaults to 1.
func hunkNewRange(header string) (int, int, error) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, 0, fmt.Errorf("unexpected hunk header %q", header)
	}
	startText, countText, hasCount := strings.Cut(strings.TrimPrefix(fields[2], "+"), ",")
	start, err := strconv.Atoi(startText)
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected hunk header %q", header)
	}
	if !hasCount {
		return start, 1, nil
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return 0, 0, fmt.Errorf("unexpected hunk header %q", header)
	}
	return start, count, nil
}
