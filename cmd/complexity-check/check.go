package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// violation is one function over at least one limit.
type violation struct {
	Path    string
	Line    int
	Name    string
	Reasons []string
	Allowed bool
}

func (v violation) String() string {
	status := ""
	if v.Allowed {
		status = " (allowed by " + allowMarker + ")"
	}
	return fmt.Sprintf("%s:%d: %s: %s%s", v.Path, v.Line, v.Name, strings.Join(v.Reasons, "; "), status)
}

// check measures the files selected by opts and returns the functions over
// the limits, sorted by path and line.
func check(opts options) ([]violation, error) {
	files, err := goFiles(opts.Paths, opts.Tests)
	if err != nil {
		return nil, err
	}
	var changed changedLines
	if opts.Base != "" {
		if changed, err = gitChangedLines(opts.Base); err != nil {
			return nil, err
		}
	}
	var violations []violation
	for _, path := range files {
		found, err := checkFile(path, opts.limits, changed)
		if err != nil {
			return nil, err
		}
		violations = append(violations, found...)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Path != violations[j].Path {
			return violations[i].Path < violations[j].Path
		}
		return violations[i].Line < violations[j].Line
	})
	return violations, nil
}

// checkFile measures one file. With changed set, only functions that
// contain a changed line are checked.
func checkFile(path string, max limits, changed changedLines) ([]violation, error) {
	if changed != nil && changed[filepath.ToSlash(path)] == nil {
		return nil, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []violation
	for _, m := range measureFile(fset, file) {
		if changed != nil && !changed.touched(filepath.ToSlash(path), m.StartLine, m.EndLine) {
			continue
		}
		if reasons := overLimits(m, max); len(reasons) > 0 {
			out = append(out, violation{Path: filepath.ToSlash(path), Line: m.StartLine, Name: m.Name, Reasons: reasons, Allowed: m.Allowed})
		}
	}
	return out, nil
}

func overLimits(m funcMetrics, max limits) []string {
	var reasons []string
	if m.Complexity > max.Complexity {
		reasons = append(reasons, fmt.Sprintf("complexity %d > %d", m.Complexity, max.Complexity))
	}
	if m.Nesting > max.Nesting {
		reasons = append(reasons, fmt.Sprintf("nesting %d > %d", m.Nesting, max.Nesting))
	}
	if m.BoolOps > max.BoolOps {
		reasons = append(reasons, fmt.Sprintf("%d boolean operators in one condition at line %d > %d", m.BoolOps, m.BoolLine, max.BoolOps))
	}
	return reasons
}

// goFiles lists the Go files under paths, skipping hidden directories,
// vendor and testdata, and _test.go files unless tests is set.
func goFiles(paths []string, tests bool) ([]string, error) {
	var files []string
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return skipDir(path, root, entry.Name())
			}
			if wantFile(entry.Name(), tests) {
				files = append(files, filepath.Clean(path))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func skipDir(path, root, name string) error {
	if path == root {
		return nil
	}
	if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" {
		return filepath.SkipDir
	}
	return nil
}

func wantFile(name string, tests bool) bool {
	if !strings.HasSuffix(name, ".go") {
		return false
	}
	return tests || !strings.HasSuffix(name, "_test.go")
}
