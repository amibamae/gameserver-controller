package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// stateDirName is the coordination directory at the repository root.
const stateDirName = "COORDINATION"

// gitWorktreeList runs git worktree list --porcelain in the current
// directory. Tests replace it.
var gitWorktreeList = func() ([]byte, error) {
	return exec.Command("git", "worktree", "list", "--porcelain").Output()
}

// mainWorktreeDir returns COORDINATION in the clone's main worktree, so
// every linked worktree of one clone shares one state directory and the
// lock file in it.
func mainWorktreeDir() (string, error) {
	out, err := gitWorktreeList()
	if err != nil {
		return "", fmt.Errorf("git worktree list: %w", err)
	}
	root, err := firstWorktree(out)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.FromSlash(root), stateDirName), nil
}

// firstWorktree reads the main worktree, which git lists first, from
// porcelain output.
func firstWorktree(out []byte) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	if !sc.Scan() {
		return "", errors.New("git worktree list printed nothing")
	}
	root, ok := strings.CutPrefix(sc.Text(), "worktree ")
	if !ok || root == "" {
		return "", fmt.Errorf("unexpected git worktree list output %q", sc.Text())
	}
	for sc.Scan() && sc.Text() != "" {
		if sc.Text() == "bare" {
			return "", errors.New("the main worktree is a bare repository")
		}
	}
	return root, sc.Err()
}
