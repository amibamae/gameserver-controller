// Command complexity-check enforces the code-health limits in CLAUDE.md on
// Go functions: cyclomatic complexity, nesting depth, and the number of &&
// and || operators in one condition.
//
//	complexity-check [flags] [path ...]
//
// With -base REF it checks only functions touched since REF (as git diff
// REF reports, including uncommitted changes to tracked files; stage a new
// file with git add so it is included), which is how CI runs it on pull
// requests. Without -base it checks every function under the paths
// (default "."). A function whose doc comment contains
// "complexity-check:allow <reason>" is reported as allowed instead.
//
// It exits 0 when every checked function is within the limits, 1 when any
// is over, and 2 on a usage or read error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// limits are the most each checked function may have.
type limits struct {
	Complexity int
	Nesting    int
	BoolOps    int
}

type options struct {
	limits
	Base  string
	Tests bool
	Paths []string
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "complexity-check:", err)
		return 2
	}
	violations, err := check(opts)
	if err != nil {
		fmt.Fprintln(stderr, "complexity-check:", err)
		return 2
	}
	for _, v := range violations {
		fmt.Fprintln(stdout, v)
	}
	if countFailures(violations) > 0 {
		fmt.Fprintf(stderr, "complexity-check: %d function(s) over the limits in CLAUDE.md (complexity %d, nesting %d, %d boolean operators per condition)\n",
			countFailures(violations), opts.Complexity, opts.Nesting, opts.BoolOps)
		return 1
	}
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("complexity-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	opts := options{}
	flags.IntVar(&opts.Complexity, "max-complexity", 15, "highest allowed cyclomatic complexity")
	flags.IntVar(&opts.Nesting, "max-nesting", 3, "deepest allowed nesting of control statements")
	flags.IntVar(&opts.BoolOps, "max-bool", 3, "most && and || operators allowed in one condition")
	flags.StringVar(&opts.Base, "base", "", "check only functions touched since this git ref")
	flags.BoolVar(&opts.Tests, "tests", false, "also check _test.go files")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	opts.Paths = flags.Args()
	if len(opts.Paths) == 0 {
		opts.Paths = []string{"."}
	}
	return opts, nil
}

func countFailures(violations []violation) int {
	failures := 0
	for _, v := range violations {
		if !v.Allowed {
			failures++
		}
	}
	return failures
}
