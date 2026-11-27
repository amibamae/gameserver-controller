// Command coordination claims tasks, reserves files and records progress in
// the multi-agent coordination state under COORDINATION/.
//
//	coordination <command> [flags]
//
// Commands: register, status, available, claim, reserve, update, block,
// handoff, release, complete, validate, reconcile. "coordination --register
// ..." is accepted as a spelling of "coordination register ...".
//
// Every command takes --dir (default $GSC_COORDINATION_DIR, else the
// COORDINATION directory of the repository's main worktree, which all
// worktrees of one clone share). Commands that change state take --agent
// (default $GSC_AGENT_ID) and --expect-revision, which rejects the change
// as stale if the state is no longer at that revision.
//
// It exits 0 on success, 1 when a command fails or validate finds errors,
// and 2 on a usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// command runs one subcommand with its arguments.
type command func(c *cli, args []string) error

var commands = map[string]command{
	"register":  cmdRegister,
	"status":    cmdStatus,
	"available": cmdAvailable,
	"claim":     cmdClaim,
	"reserve":   cmdReserve,
	"update":    cmdUpdate,
	"block":     cmdBlock,
	"handoff":   cmdHandoff,
	"release":   cmdRelease,
	"complete":  cmdComplete,
	"validate":  cmdValidate,
	"reconcile": cmdReconcile,
}

// cli carries the output streams and the process environment, so tests
// can run commands without touching the real ones.
type cli struct {
	stdout, stderr io.Writer
	getenv         func(string) string
	findDir        func() (string, error)
}

// usageError is a bad invocation; run exits 2 for it.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

// errValidation reports that validate found errors; they are already
// printed.
var errValidation = errors.New("validation found errors")

func run(args []string, stdout, stderr io.Writer) int {
	c := &cli{stdout: stdout, stderr: stderr, getenv: os.Getenv, findDir: mainWorktreeDir}
	return c.run(args)
}

func (c *cli) run(args []string) int {
	err := c.dispatch(args)
	var uerr usageError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &uerr):
		fmt.Fprintln(c.stderr, "coordination:", err)
		fmt.Fprintln(c.stderr, "usage: coordination <command> [flags]; commands:", strings.Join(commandNames(), ", "))
		return 2
	default:
		fmt.Fprintln(c.stderr, "coordination:", err)
		return 1
	}
}

func (c *cli) dispatch(args []string) error {
	if len(args) == 0 {
		return usagef("missing command")
	}
	name := args[0]
	if name == "--register" || name == "-register" {
		name = "register"
	}
	cmd, ok := commands[name]
	if !ok {
		return usagef("unknown command %q", args[0])
	}
	return cmd(c, args[1:])
}

func commandNames() []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
