package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amibamae/gameserver-controller/internal/coordination"
)

// load parses a read-only command's flags and reads one snapshot.
func (c *cli) load(name string, args []string) (*coordination.Store, *coordination.State, error) {
	o := c.newOptions(name, false)
	if err := o.parse(args); err != nil {
		return nil, nil, err
	}
	store, err := o.store()
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Load()
	if err != nil {
		return nil, nil, err
	}
	return store, st, nil
}

func cmdStatus(c *cli, args []string) error {
	store, st, err := c.load("status", args)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.stdout, "coordination state %s at revision %d\n", store.Dir, st.Revision())
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\nAGENT\tSTATUS\tTASK\tHEARTBEAT\tNAME")
	for _, a := range st.Agents.Agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Status, dash(a.CurrentTask), dash(a.LastHeartbeat), a.Name)
	}
	fmt.Fprintln(tw, "\nTASK\tSTATUS\tOWNER\tEXPIRES\tTITLE")
	for _, t := range st.Registry.Tasks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, dash(t.AssignedTo), dash(t.ExpiresAt), t.Title)
	}
	writeReservations(tw, st.Files.CurrentOwners)
	return tw.Flush()
}

func writeReservations(w io.Writer, owners map[string]coordination.Reservation) {
	fmt.Fprintln(w, "\nFILES\tAGENT\tTASK\tEXPIRES")
	if len(owners) == 0 {
		fmt.Fprintln(w, "(none reserved)\t\t\t")
		return
	}
	keys := make([]string, 0, len(owners))
	for key := range owners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := owners[key]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", key, r.Agent, r.Task, r.ExpiresAt)
	}
}

func cmdAvailable(c *cli, args []string) error {
	_, st, err := c.load("available", args)
	if err != nil {
		return err
	}
	tasks, err := coordination.Available(st, time.Now())
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		fmt.Fprintln(c.stdout, "no tasks available")
		return nil
	}
	for _, t := range tasks {
		line := fmt.Sprintf("%s\t%s\t%s", t.Task.ID, t.Task.Phase, t.Task.Title)
		if t.ExpiredOwner != "" {
			line += "\t(expired claim by " + t.ExpiredOwner + ")"
		}
		fmt.Fprintln(c.stdout, line)
		for _, w := range t.Warnings {
			fmt.Fprintln(c.stdout, "  "+w)
		}
	}
	return nil
}

func cmdValidate(c *cli, args []string) error {
	_, st, err := c.load("validate", args)
	if err != nil {
		return err
	}
	findings := coordination.Validate(st, time.Now())
	for _, f := range findings {
		fmt.Fprintln(c.stdout, f)
	}
	errorsFound := 0
	for _, f := range findings {
		if f.Level == coordination.LevelError {
			errorsFound++
		}
	}
	fmt.Fprintf(c.stdout, "%d error(s), %d warning(s) at revision %d\n", errorsFound, len(findings)-errorsFound, st.Revision())
	if errorsFound > 0 {
		return errValidation
	}
	return nil
}

func dash(p *string) string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return "-"
	}
	return *p
}
