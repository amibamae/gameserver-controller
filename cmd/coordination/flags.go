package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/coordination"
)

// Environment variables that supply defaults for --dir and --agent.
const (
	envDir   = "GSC_COORDINATION_DIR"
	envAgent = "GSC_AGENT_ID"
)

// options holds the flags every command shares.
type options struct {
	c         *cli
	fs        *flag.FlagSet
	dir       string
	agent     string
	expect    int64
	allowArgs bool
}

// required names a flag that must not be empty.
type required struct {
	flag  string
	value *string
}

// newOptions registers --dir and, for commands that change state,
// --expect-revision.
func (c *cli) newOptions(name string, mutating bool) *options {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	o := &options{c: c, fs: fs, expect: coordination.AnyRevision}
	fs.StringVar(&o.dir, "dir", "", "coordination directory (default $"+envDir+", else COORDINATION in the main worktree)")
	if mutating {
		fs.Int64Var(&o.expect, "expect-revision", coordination.AnyRevision,
			"reject the change as stale unless the state is at this revision (default: any, retrying lost races)")
	}
	return o
}

// withAgent registers --agent.
func (o *options) withAgent() *options {
	o.fs.StringVar(&o.agent, "agent", "", "acting agent ID (default $"+envAgent+")")
	return o
}

func (o *options) parse(args []string, reqs ...required) error {
	if err := o.fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usagef("%s: %v", o.fs.Name(), err)
	}
	if !o.allowArgs && o.fs.NArg() > 0 {
		return usagef("%s: unexpected argument %q", o.fs.Name(), o.fs.Arg(0))
	}
	if o.expect < coordination.AnyRevision {
		return usagef("%s: --expect-revision must be 0 or more", o.fs.Name())
	}
	for _, r := range reqs {
		if strings.TrimSpace(*r.value) == "" {
			return usagef("%s needs --%s", o.fs.Name(), r.flag)
		}
	}
	return nil
}

// parseWithAgent parses args and returns the acting agent, which must be
// given.
func (o *options) parseWithAgent(args []string, reqs ...required) (string, error) {
	if err := o.parse(args, reqs...); err != nil {
		return "", err
	}
	agent := o.agentOrEnv()
	if agent == "" {
		return "", usagef("%s needs --agent or $%s", o.fs.Name(), envAgent)
	}
	return agent, nil
}

func (o *options) agentOrEnv() string {
	if o.agent != "" {
		return o.agent
	}
	return o.c.getenv(envAgent)
}

func (o *options) store() (*coordination.Store, error) {
	dir := o.dir
	if dir == "" {
		dir = o.c.getenv(envDir)
	}
	if dir == "" {
		found, err := o.c.findDir()
		if err != nil {
			return nil, fmt.Errorf("locate the coordination directory: %w; pass --dir or set $%s", err, envDir)
		}
		dir = found
	}
	return coordination.NewStore(dir), nil
}

// apply commits m and prints what it did.
func (o *options) apply(m coordination.Mutation) error {
	store, err := o.store()
	if err != nil {
		return err
	}
	res, err := store.Apply(o.expect, m)
	if err != nil {
		return err
	}
	for _, e := range res.Events {
		fmt.Fprintf(o.c.stdout, "%s %s: %s\n", e.Action, e.Subject, e.Detail)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(o.c.stdout, n)
	}
	if !res.Committed {
		fmt.Fprintf(o.c.stdout, "nothing to change; state stays at revision %d\n", res.Revision)
		return nil
	}
	fmt.Fprintf(o.c.stdout, "state revision %d\n", res.Revision)
	return nil
}

// splitList splits comma-separated values and drops empty items.
func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
